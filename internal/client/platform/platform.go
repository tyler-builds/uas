// Package platform detects the system uas runs on and which install tools it has.
package platform

import (
	"bufio"
	"io/fs"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
)

// Info describes the running system.
type Info struct {
	// From os-release.
	OSID       string   `json:"os_id"`
	OSIDLike   []string `json:"os_id_like,omitempty"`
	VersionID  string   `json:"version_id,omitempty"`
	VariantID  string   `json:"variant_id,omitempty"`
	ImageID    string   `json:"image_id,omitempty"`
	PrettyName string   `json:"pretty_name,omitempty"`

	// Arch is the CPU architecture in the names Flatpak and RPM use, such as "x86_64".
	Arch string `json:"arch"`
	// Atomic is true on image-based systems (rpm-ostree or bootc), where /usr is read-only.
	Atomic bool `json:"atomic"`

	// Paths to the tools uas drives; empty when the tool is missing.
	Flatpak   string `json:"flatpak,omitempty"`
	Brew      string `json:"brew,omitempty"`
	RPMOSTree string `json:"rpm_ostree,omitempty"`
	Pkexec    string `json:"pkexec,omitempty"`

	// LibFUSE2 is true when AppImages built for FUSE 2 can run.
	LibFUSE2 bool `json:"libfuse2"`

	Desktop     string `json:"desktop,omitempty"`      // $XDG_CURRENT_DESKTOP
	SessionType string `json:"session_type,omitempty"` // $XDG_SESSION_TYPE
}

// FedoraRelease returns the Fedora release the system is built on (such as 44), or 0 if it is not Fedora-based.
func (i Info) FedoraRelease() int {
	if i.OSID != "fedora" && !contains(i.OSIDLike, "fedora") {
		return 0
	}
	n, err := strconv.Atoi(i.VersionID)
	if err != nil {
		return 0
	}
	return n
}

// Env is what Detect reads, so tests can supply a fake system.
type Env struct {
	Root     fs.FS                             // the filesystem root, "/" on a real system
	LookPath func(name string) (string, error) // like exec.LookPath
	Getenv   func(key string) string
	GOARCH   string
}

// System returns the Env of the running system.
func System() Env {
	return Env{Root: os.DirFS("/"), LookPath: exec.LookPath, Getenv: os.Getenv, GOARCH: runtime.GOARCH}
}

// Detect inspects the system described by env.
func Detect(env Env) Info {
	info := Info{
		Arch:        archName(env.GOARCH),
		Desktop:     env.Getenv("XDG_CURRENT_DESKTOP"),
		SessionType: env.Getenv("XDG_SESSION_TYPE"),
	}

	osr := readOSRelease(env.Root)
	info.OSID = osr["ID"]
	info.OSIDLike = strings.Fields(osr["ID_LIKE"])
	info.VersionID = osr["VERSION_ID"]
	info.VariantID = osr["VARIANT_ID"]
	info.ImageID = osr["IMAGE_ID"]
	info.PrettyName = osr["PRETTY_NAME"]

	// ostree and bootc systems both create this marker at boot.
	info.Atomic = exists(env.Root, "run/ostree-booted")

	info.Flatpak = lookPath(env, "flatpak")
	info.RPMOSTree = lookPath(env, "rpm-ostree")
	info.Pkexec = lookPath(env, "pkexec")
	info.Brew = lookPath(env, "brew")
	if info.Brew == "" && exists(env.Root, "home/linuxbrew/.linuxbrew/bin/brew") {
		// Homebrew's default Linux prefix; it is often not on PATH for GUI sessions.
		info.Brew = "/home/linuxbrew/.linuxbrew/bin/brew"
	}

	for _, p := range []string{
		"usr/lib64/libfuse.so.2",
		"usr/lib/libfuse.so.2",
		"usr/lib/x86_64-linux-gnu/libfuse.so.2",
		"usr/lib/aarch64-linux-gnu/libfuse.so.2",
	} {
		if exists(env.Root, p) {
			info.LibFUSE2 = true
			break
		}
	}
	return info
}

func readOSRelease(root fs.FS) map[string]string {
	vals := map[string]string{}
	for _, p := range []string{"etc/os-release", "usr/lib/os-release"} {
		f, err := root.Open(p)
		if err != nil {
			continue
		}
		defer f.Close()
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			k, v, ok := strings.Cut(line, "=")
			if !ok {
				continue
			}
			vals[k] = unquote(v)
		}
		return vals
	}
	return vals
}

func unquote(v string) string {
	if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
		if s, err := strconv.Unquote(`"` + v[1:len(v)-1] + `"`); err == nil {
			return s
		}
		return v[1 : len(v)-1]
	}
	return v
}

func archName(goarch string) string {
	switch goarch {
	case "amd64":
		return "x86_64"
	case "arm64":
		return "aarch64"
	}
	return goarch
}

func lookPath(env Env, name string) string {
	p, err := env.LookPath(name)
	if err != nil {
		return ""
	}
	return p
}

func exists(root fs.FS, name string) bool {
	_, err := fs.Stat(root, name)
	return err == nil
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
