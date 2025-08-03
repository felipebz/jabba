package command

import (
	"bytes"
	"github.com/felipebz/javm/discovery"
	"strings"
	"testing"
	"time"
)

type fakeManager struct {
	jdks []discovery.JDK
	err  error
}

func (f *fakeManager) DiscoverAll() ([]discovery.JDK, error) {
	return f.jdks, f.err
}

func TestDiscoverRefreshCommand_Success(t *testing.T) {
	newManagerWithAllSources = func(cacheFile string, ttl time.Duration) discoverRunner {
		return &fakeManager{}
	}

	cmd := newDiscoverRefreshCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := out.String()

	if !strings.Contains(got, "Discovery cache refreshed successfully") {
		t.Errorf("expected success message, got: %s", got)
	}
}

func TestDiscoverListCommand_SimpleOutput(t *testing.T) {
	jdks := []discovery.JDK{
		{Source: "sdkman", Version: "17.0.2"},
		{Source: "system", Version: "21.0.1"},
	}
	newManagerWithAllSources = func(cacheFile string, ttl time.Duration) discoverRunner {
		return &fakeManager{jdks: jdks}
	}

	cmd := newDiscoverListCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := out.String()

	if !strings.Contains(got, "sdkman@17.0.2") {
		t.Errorf("expected sdkman entry, got: %s", got)
	}
	if !strings.Contains(got, "system@21.0.1") {
		t.Errorf("expected system entry, got: %s", got)
	}
}

func TestDiscoverListCommand_DetailsFlag(t *testing.T) {
	jdks := []discovery.JDK{
		{
			Source:         "system",
			Version:        "21.0.1",
			Vendor:         "Temurin",
			Implementation: "JDK",
			Architecture:   "x64",
			Path:           "/opt/jdk21",
		},
	}
	newManagerWithAllSources = func(cacheFile string, ttl time.Duration) discoverRunner {
		return &fakeManager{jdks: jdks}
	}

	cmd := newDiscoverListCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)

	cmd.SetArgs([]string{"--details"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := out.String()

	if !strings.Contains(got, "SOURCE  VERSION  VENDOR   IMPLEMENTATION  ARCHITECTURE  PATH") {
		t.Errorf("expected table header, got: %s", got)
	}
	if !strings.Contains(got, "system  21.0.1   Temurin  JDK             x64           /opt/jdk21") {
		t.Errorf("expected detailed system entry, got: %s", got)
	}
}
