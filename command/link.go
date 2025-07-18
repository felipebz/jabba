package command

import (
	"errors"
	"github.com/felipebz/javm/cfg"
	"github.com/felipebz/javm/semver"
	log "github.com/sirupsen/logrus"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

func Link(selector string, dir string) error {
	if !strings.HasPrefix(selector, "system@") {
		return errors.New("Name must begin with 'system@' (e.g. 'system@1.8.73')")
	}
	// <version> has to be valid per semver
	if _, err := semver.ParseVersion(selector); err != nil {
		return err
	}
	if dir == "" {
		ver, err := LsBestMatch(selector)
		if err != nil {
			return err
		}
		return os.Remove(filepath.Join(cfg.Dir(), "jdk", ver))
	} else {
		if err := assertJavaDistribution(dir, runtime.GOOS); err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Join(cfg.Dir(), "jdk"), 0755); err != nil {
			return err
		}
		return os.Symlink(dir, filepath.Join(cfg.Dir(), "jdk", selector))
	}
}

func LinkLatest() error {
	files, _ := readDir(filepath.Join(cfg.Dir(), "jdk"))
	var vs, err = Ls()
	if err != nil {
		return err
	}
	cache := make(map[string]string)
	for _, f := range files {
		info, _ := f.Info()
		if f.IsDir() || info.Mode()&os.ModeSymlink == os.ModeSymlink {
			sourceVersion := f.Name()
			if strings.Count(sourceVersion, ".") == 1 && !strings.HasPrefix(sourceVersion, "system@") {
				target := GetLink(sourceVersion)
				_, err := LsBestMatchWithVersionSlice(vs, sourceVersion)
				if err != nil {
					err := os.Remove(filepath.Join(cfg.Dir(), "jdk", sourceVersion))
					if err == nil {
						log.Info(sourceVersion + " -/> " + target)
					}
					if !os.IsNotExist(err) {
						return err
					}
				} else {
					cache[sourceVersion] = target
				}
			}
		}
	}
	for _, v := range semver.VersionSlice(vs).TrimTo(semver.VPMinor) {
		sourceVersion := v.TrimTo(semver.VPMinor)
		target := filepath.Join(cfg.Dir(), "jdk", v.String())
		if v.Prerelease() == "" && cache[sourceVersion] != target && !strings.HasPrefix(sourceVersion, "system@") {
			source := filepath.Join(cfg.Dir(), "jdk", sourceVersion)
			log.Info(sourceVersion + " -> " + target)
			os.Remove(source)
			if err := os.Symlink(target, source); err != nil {
				return err
			}
		}
	}
	return linkAlias("default", vs)
}

func LinkAlias(name string) error {
	var vs, err = Ls()
	if err != nil {
		return err
	}
	return linkAlias(name, vs)
}

func linkAlias(name string, vs []*semver.Version) error {
	defaultAlias := GetAlias(name)
	if defaultAlias != "" {
		defaultAlias, _ = LsBestMatchWithVersionSlice(vs, defaultAlias)
	}
	sourceRef := /*"alias@" + */ name
	source := filepath.Join(cfg.Dir(), "jdk", sourceRef)
	sourceTarget := GetLink(sourceRef)
	if defaultAlias != "" {
		target := filepath.Join(cfg.Dir(), "jdk", defaultAlias)
		if sourceTarget != target {
			log.Info(sourceRef + " -> " + target)
			os.Remove(source)
			if err := os.Symlink(target, source); err != nil {
				return err
			}
		}
	} else {
		err := os.Remove(source)
		if err == nil {
			log.Info(sourceRef + " -/> " + sourceTarget)
		}
		if !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func GetLink(name string) string {
	res, err := filepath.EvalSymlinks(filepath.Join(cfg.Dir(), "jdk", name))
	if err != nil {
		return ""
	}
	return res
}
