package manifest

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Releases of uas itself are published beside the catalog and signed with the same key, under their own
// signing context, so a catalog manifest's signature can never pass as a release's, or the other way round.

// ReleaseName is the object name of the latest release's signed description.
const ReleaseName = "releases/latest.json"

const releaseContext = "uas-release-v1\n"

// releasePath and programPath match the only object names a release may point at.
var (
	releasePath = regexp.MustCompile(`^releases/uas-(\d+\.\d+\.\d+)-linux-(x86_64|aarch64)\.tar\.gz$`)
	programPath = regexp.MustCompile(`^releases/uas-(\d+\.\d+\.\d+)-linux-(x86_64|aarch64)$`)
)

// Release describes a published version of uas.
//
// A release is one program per architecture (Programs), which is the CLI and the app and installs itself.
// uas 0.5.0 and older read only Files, archives holding uas and uas-gui, and refuse a release whose
// files they don't know; so a release also lists those archives, with the program as both, for them to update
// to it. Once updated, they use Programs.
type Release struct {
	Version  string        `json:"version"` // "0.2.0", without a "v"
	Released time.Time     `json:"released"`
	Files    []ReleaseFile `json:"files"`
	Programs []ReleaseFile `json:"programs,omitempty"`
}

// ReleaseFile is a release's program or archive for one architecture.
type ReleaseFile struct {
	Arch string `json:"arch"`
	// Path is relative to the base URL: "releases/uas-0.2.0-linux-x86_64" for a program,
	// "releases/uas-0.2.0-linux-x86_64.tar.gz" for an archive.
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// Archive reports whether the file is an archive (of the programs) rather than the program itself.
func (f ReleaseFile) Archive() bool { return strings.HasSuffix(f.Path, ".tar.gz") }

// ReleasePath returns the object name of a release archive.
func ReleasePath(version, arch string) string {
	return ProgramPath(version, arch) + ".tar.gz"
}

// ProgramPath returns the object name of a release's program.
func ProgramPath(version, arch string) string {
	return "releases/uas-" + version + "-linux-" + arch
}

// Validate checks the fields a client relies on.
func (r *Release) Validate() error {
	if _, err := ParseVersion(r.Version); err != nil {
		return err
	}
	if r.Released.IsZero() || len(r.Files)+len(r.Programs) == 0 {
		return errors.New("release: missing date or files")
	}
	for _, set := range []struct {
		files []ReleaseFile
		path  *regexp.Regexp
	}{{r.Files, releasePath}, {r.Programs, programPath}} {
		for _, f := range set.files {
			m := set.path.FindStringSubmatch(f.Path)
			if m == nil || m[1] != r.Version || m[2] != f.Arch {
				return fmt.Errorf("release: bad file path %q", f.Path)
			}
			if len(f.SHA256) != 64 || f.Size <= 0 {
				return fmt.Errorf("release: %s has no checksum or size", f.Path)
			}
		}
	}
	return nil
}

// File returns what to update to on arch: the release's program, else its archive.
func (r *Release) File(arch string) (ReleaseFile, bool) {
	for _, set := range [][]ReleaseFile{r.Programs, r.Files} {
		for _, f := range set {
			if f.Arch == arch {
				return f, true
			}
		}
	}
	return ReleaseFile{}, false
}

// SignRelease returns the published envelope for r.
func SignRelease(r Release, priv ed25519.PrivateKey) ([]byte, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	payload, err := json.Marshal(r)
	if err != nil {
		return nil, err
	}
	env := Envelope{
		KeyID:     KeyID(priv.Public().(ed25519.PublicKey)),
		Payload:   payload,
		Signature: ed25519.Sign(priv, append([]byte(releaseContext), payload...)),
	}
	return json.MarshalIndent(env, "", "  ")
}

// VerifyRelease checks a release envelope against the trusted keys and returns the release inside.
func VerifyRelease(data []byte, trusted []ed25519.PublicKey) (Release, error) {
	var env Envelope
	if err := json.Unmarshal(data, &env); err != nil {
		return Release{}, fmt.Errorf("release: %w", err)
	}
	var key ed25519.PublicKey
	for _, k := range trusted {
		if KeyID(k) == env.KeyID {
			key = k
			break
		}
	}
	if key == nil {
		return Release{}, fmt.Errorf("release: signed by unknown key %q", env.KeyID)
	}
	if !ed25519.Verify(key, append([]byte(releaseContext), env.Payload...), env.Signature) {
		return Release{}, errors.New("release: bad signature")
	}
	var r Release
	if err := json.Unmarshal(env.Payload, &r); err != nil {
		return Release{}, fmt.Errorf("release: %w", err)
	}
	if err := r.Validate(); err != nil {
		return Release{}, err
	}
	return r, nil
}

// Version is a parsed "major.minor.patch" version.
type Version [3]int

// ParseVersion reads "1.2.3" or "v1.2.3".
func ParseVersion(s string) (Version, error) {
	parts := strings.Split(strings.TrimPrefix(s, "v"), ".")
	if len(parts) != 3 {
		return Version{}, fmt.Errorf("version %q is not major.minor.patch", s)
	}
	var v Version
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || (len(p) > 1 && p[0] == '0') {
			return Version{}, fmt.Errorf("version %q is not major.minor.patch", s)
		}
		v[i] = n
	}
	return v, nil
}

// Newer reports whether v is newer than o.
func (v Version) Newer(o Version) bool {
	for i := range v {
		if v[i] != o[i] {
			return v[i] > o[i]
		}
	}
	return false
}
