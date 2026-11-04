package platform

import (
	"errors"
	"testing"
	"testing/fstest"
)

func TestDetectBazzite(t *testing.T) {
	root := fstest.MapFS{
		"etc/os-release": {Data: []byte(`NAME="Bazzite"
# a comment
ID=bazzite
ID_LIKE="fedora"
VERSION_ID=44
VARIANT_ID=bazzite-nvidia-open
IMAGE_ID="bazzite-nvidia-open-44.20261026"
PRETTY_NAME="Bazzite 44 (FROM Fedora Kinoite)"
`)},
		"run/ostree-booted":                  {},
		"usr/lib64/libfuse.so.2":             {},
		"home/linuxbrew/.linuxbrew/bin/brew": {},
	}
	tools := map[string]string{"flatpak": "/usr/bin/flatpak", "rpm-ostree": "/usr/bin/rpm-ostree"}
	env := Env{
		Root: root,
		LookPath: func(name string) (string, error) {
			if p, ok := tools[name]; ok {
				return p, nil
			}
			return "", errors.New("not found")
		},
		Getenv: func(k string) string { return map[string]string{"XDG_CURRENT_DESKTOP": "KDE"}[k] },
		GOARCH: "amd64",
	}

	info := Detect(env)
	if info.OSID != "bazzite" || info.VersionID != "44" || info.ImageID != "bazzite-nvidia-open-44.20261026" ||
		info.PrettyName != "Bazzite 44 (FROM Fedora Kinoite)" {
		t.Errorf("os-release fields = %+v", info)
	}
	if !info.Atomic || !info.LibFUSE2 || info.Arch != "x86_64" || info.Desktop != "KDE" {
		t.Errorf("system fields = %+v", info)
	}
	if info.Flatpak != "/usr/bin/flatpak" || info.Pkexec != "" {
		t.Errorf("tools = %+v", info)
	}
	if info.Brew != "/home/linuxbrew/.linuxbrew/bin/brew" {
		t.Errorf("Brew = %q, want the default Linux prefix", info.Brew)
	}
	if got := info.FedoraRelease(); got != 44 {
		t.Errorf("FedoraRelease = %d, want 44", got)
	}
}

func TestDetectFallbackOSRelease(t *testing.T) {
	env := Env{
		Root:     fstest.MapFS{"usr/lib/os-release": {Data: []byte("ID=debian\nVERSION_ID=\"13\"\n")}},
		LookPath: func(string) (string, error) { return "", errors.New("not found") },
		Getenv:   func(string) string { return "" },
		GOARCH:   "arm64",
	}
	info := Detect(env)
	if info.OSID != "debian" || info.Atomic || info.Arch != "aarch64" || info.FedoraRelease() != 0 {
		t.Errorf("info = %+v", info)
	}
}
