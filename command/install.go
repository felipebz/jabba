package command

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"errors"
	"fmt"
	"github.com/felipebz/javm/cfg"
	"github.com/felipebz/javm/command/fileiter"
	"github.com/felipebz/javm/semver"
	"github.com/goccy/go-yaml"
	"github.com/schollz/progressbar/v3"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/ulikunitz/xz"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

func NewInstallCommand(client PackagesWithInfoClient) *cobra.Command {
	var customInstallDestination string

	cmd := &cobra.Command{
		Use:   "install [version to install]",
		Short: "Download and install JDK",
		RunE: func(cmd *cobra.Command, args []string) error {
			var ver string
			if len(args) == 0 {
				ver = rc().JDK
				if ver == "" {
					return pflag.ErrHelp
				}
			} else {
				ver = args[0]
			}
			ver, err := runInstall(client, ver, customInstallDestination)
			if err != nil {
				log.Fatal(err)
			}
			if customInstallDestination == "" {
				if err := LinkLatest(); err != nil {
					log.Fatal(err)
				}
				// TODO change to call the "use" command after it's refactored
				//return use(ver)
				return nil
			} else {
				return nil
			}
		},
		Example: "  javm install 1.8\n" +
			"  javm install ~1.8.73 # same as \">=1.8.73 <1.9.0\"\n" +
			"  javm install 1.8.73=dmg+http://.../jdk-9-ea+110_osx-x64_bin.dmg",
	}
	cmd.Flags().StringVarP(&customInstallDestination, "output", "o", "",
		"Custom destination (any JDK outside of $JABBA_HOME/jdk is considered to be unmanaged, i.e. not available to javm ls, use, etc. (unless `javm link`ed))")
	return cmd
}

func runInstall(client PackagesWithInfoClient, selector string, dst string) (string, error) {
	var releaseMap map[*semver.Version]string
	var ver *semver.Version
	var err error
	// selector can be in form of <version>=<url>
	if strings.Contains(selector, "=") && strings.Contains(selector, "://") {
		split := strings.SplitN(selector, "=", 2)
		selector = split[0]
		// <version> has to be valid per semver
		ver, err = semver.ParseVersion(selector)
		if err != nil {
			return "", err
		}
		releaseMap = map[*semver.Version]string{ver: split[1]}
	} else {
		// ... or a version (range will be tried over remote targets)
		ver, _ = semver.ParseVersion(selector)
	}
	// ... apparently it's not
	if releaseMap == nil {
		ver = nil
		rng, err := semver.ParseRange(selector)
		if err != nil {
			return "", err
		}
		distribution := rng.Qualifier
		if distribution == "" {
			distribution = "temurin"
		}
		packageIndex, err := makePackageIndex(client, runtime.GOOS, runtime.GOARCH, distribution)
		if err != nil {
			return "", err
		}
		sort.Sort(sort.Reverse(semver.VersionSlice(packageIndex.Sorted)))
		for _, v := range packageIndex.Sorted {
			if rng.Contains(v) {
				ver = v
				packageInfo, err := client.GetPackageInfo(packageIndex.ByVersion[ver].Id)
				if err != nil {
					return "", err
				}

				downloadUri := packageInfo.DirectDownloadUri
				releaseMap = map[*semver.Version]string{ver: downloadUri}
				break
			}
		}
		if ver == nil {
			tt := make([]string, len(packageIndex.Sorted))
			for i, v := range packageIndex.Sorted {
				tt[i] = v.String()
			}
			return "", errors.New("No compatible version found for " + selector +
				"\nValid install targets: " + strings.Join(tt, ", "))
		}
	}
	// check whether requested version is already installed
	if ver != nil && dst == "" {
		local, err := Ls()
		if err != nil {
			return "", err
		}
		for _, v := range local {
			if ver.Equals(v) {
				return ver.String(), nil
			}
		}
	}
	url := releaseMap[ver]
	if dst == "" {
		dst = filepath.Join(cfg.Dir(), "jdk", ver.String())
	} else {
		if _, err := os.Stat(dst); !os.IsNotExist(err) {
			if err == nil { // dst exists
				if empty, _ := isEmptyDir(dst); !empty {
					err = fmt.Errorf("\"%s\" is not empty", dst)
				}
			} // or is inaccessible
			if err != nil {
				return "", err
			}
		}
	}
	url = url[strings.Index(url, "+")+1:]
	var file string
	var deleteFileWhenFinnished bool
	if strings.HasPrefix(url, "file://") {
		file = strings.TrimPrefix(url, "file://")
		if runtime.GOOS == "windows" {
			// file:///C:/path/...
			file = strings.Replace(strings.TrimPrefix(file, "/"), "/", "\\", -1)
		}
	} else {
		log.Info("Downloading ", ver, " (", url, ")")
		file, err = download(url)
		if err != nil {
			return "", err
		}
		deleteFileWhenFinnished = true
	}
	switch runtime.GOOS {
	case "darwin", "linux", "windows":
		err = install(file, dst)
	default:
		err = errors.New(runtime.GOOS + " OS is not supported")
	}
	if err == nil && deleteFileWhenFinnished {
		os.Remove(file)
	}
	return ver.String(), err
}

func isEmptyDir(name string) (bool, error) {
	entries, err := os.ReadDir(name)
	if err != nil {
		return false, err
	}
	return len(entries) == 0, nil
}

type RedirectTracer struct {
	Transport http.RoundTripper
}

func (self RedirectTracer) RoundTrip(req *http.Request) (resp *http.Response, err error) {
	transport := self.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	resp, err = transport.RoundTrip(req)
	if err != nil {
		return
	}
	switch resp.StatusCode {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther, http.StatusTemporaryRedirect:
		log.Debug("Following ", resp.StatusCode, " redirect to ", resp.Header.Get("Location"))
	}
	return
}

func download(url string) (file string, err error) {
	ext := getFileExtension(url)
	tmp, err := os.CreateTemp("", "jabba-d-*"+ext)
	if err != nil {
		return
	}
	defer tmp.Close()

	file = tmp.Name()
	log.Debug("Saving ", url, " to ", file)
	client := http.Client{Transport: RedirectTracer{}}
	res, err := client.Get(url)
	if err != nil {
		return
	}
	defer res.Body.Close()

	bar := progressbar.DefaultBytes(
		res.ContentLength,
		"downloading",
	)
	_, err = io.Copy(io.MultiWriter(tmp, bar), res.Body)
	if err != nil {
		return
	}
	return
}

func getFileExtension(file string) string {
	if strings.HasSuffix(file, ".tar.gz") {
		return ".tar.gz"
	}
	if strings.HasSuffix(file, ".tar.xz") {
		return ".tar.xz"
	}
	return filepath.Ext(file)
}

func install(file string, dst string) (err error) {
	ext := getFileExtension(file)
	switch ext {
	case ".zip":
		err = installFromZip(file, dst)
	case ".tar.gz":
		err = installFromTgz(file, dst)
	case ".tar.xz":
		err = installFromTgx(file, dst)
	default:
		return errors.New("Unsupported file type: " + file)
	}
	if err == nil {
		err = normalizePathToBinJava(dst, runtime.GOOS)
	}
	if err != nil {
		os.RemoveAll(dst)
	}
	return
}

// **/{Contents/Home,Home,}bin/java -> <dir>/Contents/Home/bin/java
func normalizePathToBinJava(dir string, goos string) error {
	dir = filepath.Clean(dir)
	if _, err := os.Stat(expectedJavaPath(dir, goos)); os.IsNotExist(err) {
		java := "java"
		if goos == "windows" {
			java = "java.exe"
		}
		var javaPath string
		for it := fileiter.New(dir, fileiter.BreadthFirst()); it.Next(); {
			if err := it.Err(); err != nil {
				return err
			}
			if !it.IsDir() && filepath.Base(it.Dir()) == "bin" && it.Name() == java {
				javaPath = filepath.Join(it.Dir(), it.Name())
				break
			}
		}
		if javaPath != "" {
			log.Debugf("Found %s", javaPath)
			tmp := dir + "~"
			javaPath = strings.Replace(javaPath, dir, tmp, 1)
			log.Debugf("Moving %s to %s", dir, tmp)
			if err := os.Rename(dir, tmp); err != nil {
				return err
			}
			defer func() {
				log.Debugf("Removing %s", tmp)
				os.RemoveAll(tmp)
			}()
			homeDir := filepath.Dir(filepath.Dir(javaPath))
			var src, dst string
			if goos == "darwin" {
				if filepath.Base(homeDir) == "Home" {
					src = filepath.Dir(homeDir)
					dst = filepath.Join(dir, "Contents")
				} else {
					src = homeDir
					dst = filepath.Join(dir, "Contents", "Home")
				}
			} else {
				src = homeDir
				dst = dir
			}
			log.Debugf("Moving %s to %s", src, dst)
			if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
				return err
			}
			if err = os.Rename(src, dst); err != nil {
				return err
			}
		}
		return assertJavaDistribution(dir, goos)
	}
	return nil
}

func expectedJavaPath(dir string, goos string) string {
	var osSpecificSubDir = ""
	if goos == "darwin" {
		osSpecificSubDir = filepath.Join("Contents", "Home")
	}
	java := "java"
	if goos == "windows" {
		java = "java.exe"
	}
	return filepath.Join(dir, osSpecificSubDir, "bin", java)
}

func assertJavaDistribution(dir string, goos string) error {
	var path = expectedJavaPath(dir, goos)
	var err error
	if _, err = os.Stat(path); os.IsNotExist(err) {
		err = errors.New(path + " wasn't found. " +
			"If you believe this is an error - please create a ticket at https://github.com/felipebz/javm/issues " +
			"(specify OS and command that was used)")
	}
	return err
}

func installFromTgz(src string, dst string) error {
	log.Info("Extracting " + src + " to " + dst)
	return untgz(src, dst, true)
}

func untgz(src string, dst string, strip bool) error {
	gzFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer gzFile.Close()
	var prefixToStrip string
	if strip {
		gzr, err := gzip.NewReader(gzFile)
		if err != nil {
			return err
		}
		defer gzr.Close()
		r := tar.NewReader(gzr)
		var prefix []string
		for {
			header, err := r.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				return err
			}
			var dir string
			if header.Typeflag != tar.TypeDir {
				dir = filepath.Dir(header.Name)
			} else {
				continue
			}
			if prefix != nil {
				dirSplit := strings.Split(dir, string(filepath.Separator))
				i, e, dse := 0, len(prefix), len(dirSplit)
				if dse < e {
					e = dse
				}
				for i < e {
					if prefix[i] != dirSplit[i] {
						prefix = prefix[0:i]
						break
					}
					i++
				}
			} else {
				prefix = strings.Split(dir, string(filepath.Separator))
			}
		}
		prefixToStrip = strings.Join(prefix, string(filepath.Separator))
	}
	gzFile.Seek(0, 0)
	gzr, err := gzip.NewReader(gzFile)
	if err != nil {
		return err
	}
	defer gzr.Close()
	r := tar.NewReader(gzr)
	dirCache := make(map[string]bool) // todo: radix tree would perform better here
	if err := os.MkdirAll(dst, 0755); err != nil {
		return err
	}
	for {
		header, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		var dir string
		if header.Typeflag != tar.TypeDir {
			dir = filepath.Dir(header.Name)
		} else {
			dir = filepath.Clean(header.Name)
			if !strings.HasPrefix(dir, prefixToStrip) {
				continue
			}
		}
		dir = strings.TrimPrefix(dir, prefixToStrip)
		if dir != "" && dir != "." {
			cached := dirCache[dir]
			if !cached {
				if err := os.MkdirAll(filepath.Join(dst, dir), 0755); err != nil {
					return err
				}
				dirCache[dir] = true
			}
		}
		target := filepath.Join(dst, dir, filepath.Base(header.Name))
		switch header.Typeflag {
		case tar.TypeReg:
			d, err := os.OpenFile(target,
				os.O_WRONLY|os.O_CREATE|os.O_TRUNC, os.FileMode(header.Mode|0600)&0777)
			if err != nil {
				return err
			}
			_, err = io.Copy(d, r)
			d.Close()
			if err != nil {
				return err
			}
		case tar.TypeSymlink:
			if err = os.Symlink(header.Linkname, target); err != nil {
				return err
			}
		}
	}
	return nil
}

func installFromTgx(src string, dst string) error {
	log.Info("Extracting " + src + " to " + dst)
	return untgx(src, dst, true)
}

func untgx(src string, dst string, strip bool) error {
	xzFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer xzFile.Close()
	var prefixToStrip string
	if strip {
		xzr, err := xz.NewReader(xzFile)
		if err != nil {
			return err
		}
		r := tar.NewReader(xzr)
		var prefix []string
		for {
			header, err := r.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				return err
			}
			var dir string
			if header.Typeflag != tar.TypeDir {
				dir = filepath.Dir(header.Name)
			} else {
				continue
			}
			if prefix != nil {
				dirSplit := strings.Split(dir, string(filepath.Separator))
				i, e, dse := 0, len(prefix), len(dirSplit)
				if dse < e {
					e = dse
				}
				for i < e {
					if prefix[i] != dirSplit[i] {
						prefix = prefix[0:i]
						break
					}
					i++
				}
			} else {
				prefix = strings.Split(dir, string(filepath.Separator))
			}
		}
		prefixToStrip = strings.Join(prefix, string(filepath.Separator))
	}
	xzFile.Seek(0, 0)
	xzr, err := xz.NewReader(xzFile)
	if err != nil {
		return err
	}
	r := tar.NewReader(xzr)
	dirCache := make(map[string]bool) // todo: radix tree would perform better here
	if err := os.MkdirAll(dst, 0755); err != nil {
		return err
	}
	for {
		header, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		var dir string
		if header.Typeflag != tar.TypeDir {
			dir = filepath.Dir(header.Name)
		} else {
			dir = filepath.Clean(header.Name)
			if !strings.HasPrefix(dir, prefixToStrip) {
				continue
			}
		}
		dir = strings.TrimPrefix(dir, prefixToStrip)
		if dir != "" && dir != "." {
			cached := dirCache[dir]
			if !cached {
				if err := os.MkdirAll(filepath.Join(dst, dir), 0755); err != nil {
					return err
				}
				dirCache[dir] = true
			}
		}
		target := filepath.Join(dst, dir, filepath.Base(header.Name))
		switch header.Typeflag {
		case tar.TypeReg:
			d, err := os.OpenFile(target,
				os.O_WRONLY|os.O_CREATE|os.O_TRUNC, os.FileMode(header.Mode|0600)&0777)
			if err != nil {
				return err
			}
			_, err = io.Copy(d, r)
			d.Close()
			if err != nil {
				return err
			}
		case tar.TypeSymlink:
			if err = os.Symlink(header.Linkname, target); err != nil {
				return err
			}
		}
	}
	return nil
}

func installFromZip(src string, dst string) error {
	log.Info("Extracting " + src + " to " + dst)
	return unzip(src, dst, true)
}

func unzip(src string, dst string, strip bool) error {
	r, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer r.Close()
	var prefixToStrip string
	if strip {
		var prefix []string
		for _, f := range r.File {
			var dir string
			if !f.Mode().IsDir() {
				dir = filepath.Dir(f.Name)
			} else {
				continue
			}
			if prefix != nil {
				dirSplit := strings.Split(dir, string(filepath.Separator))
				i, e, dse := 0, len(prefix), len(dirSplit)
				if dse < e {
					e = dse
				}
				for i < e {
					if prefix[i] != dirSplit[i] {
						prefix = prefix[0:i]
						break
					}
					i++
				}
			} else {
				prefix = strings.Split(dir, string(filepath.Separator))
			}
		}
		prefixToStrip = strings.Join(prefix, string(filepath.Separator))
	}
	dirCache := make(map[string]bool) // todo: radix tree would perform better here
	if err := os.MkdirAll(dst, 0755); err != nil {
		return err
	}
	for _, f := range r.File {
		var dir string
		if !f.Mode().IsDir() {
			dir = filepath.Dir(f.Name)
		} else {
			dir = filepath.Clean(f.Name)
			if !strings.HasPrefix(dir, prefixToStrip) {
				continue
			}
		}
		dir = strings.TrimPrefix(dir, prefixToStrip)
		if dir != "" && dir != "." {
			cached := dirCache[dir]
			if !cached {
				if err := os.MkdirAll(filepath.Join(dst, dir), 0755); err != nil {
					return err
				}
				dirCache[dir] = true
			}
		}
		if !f.Mode().IsDir() {
			name := filepath.Base(f.Name)
			fr, err := f.Open()
			if err != nil {
				return err
			}
			d, err := os.OpenFile(filepath.Join(dst, dir, name),
				os.O_WRONLY|os.O_CREATE|os.O_TRUNC, (f.Mode()|0600)&0777)
			if err != nil {
				return err
			}
			_, err = io.Copy(d, fr)
			d.Close()
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// TODO copied from javm.go, it should be moved to the config package
type jabbarc struct {
	JDK string
}

func rc() (rc jabbarc) {
	b, err := os.ReadFile(".jabbarc")
	if err != nil {
		return
	}
	// content can be a string (jdk version)
	err = yaml.Unmarshal(b, &rc.JDK)
	if err != nil {
		// or a struct
		err = yaml.Unmarshal(b, &rc)
		if err != nil {
			log.Fatal(".jabbarc is not valid")
		}
	}
	return
}
