package manifest

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Releases of uas itself are GitHub releases of github.com/tyler-builds/uas. Each carries the program for
// every architecture and a signed description of the release (ReleaseAsset), signed with the catalog's key
// under its own signing context, so a catalog manifest's signature can never pass as a release's, or the other
// way round. uas trusts a release's checksums only from that signature, not from GitHub.

// ReleaseAsset is the name of a release's signed description.
const ReleaseAsset = "uas-release.json"

// ChecksumsAsset is the name of a release's SHA256SUMS file, for people who check a download by hand.
const ChecksumsAsset = "SHA256SUMS"

// releaseContext is v2 since releases moved to GitHub: a v1 release (served by the catalog Worker to uas 0.5.0
// and older, with paths on the Worker) can never pass as one.
const releaseContext = "uas-release-v2\n"

// Release describes a published version of uas: one program per architecture, which is the CLI and the app
// and installs itself.
type Release struct {
	Version  string        `json:"version"` // "0.2.0", without a "v"
	Released time.Time     `json:"released"`
	Programs []ReleaseFile `json:"programs"`
}

// ReleaseFile is a release's program for one architecture.
type ReleaseFile struct {
	Arch   string `json:"arch"`
	Name   string `json:"name"` // the release asset's name, AssetName(Arch)
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// AssetName returns the name of a release's program for arch, such as "uas-linux-x86_64". It has no version,
// so https://github.com/tyler-builds/uas/releases/latest/download/<name> always downloads the latest.
func AssetName(arch string) string {
	return "uas-linux-" + arch
}

// Validate checks the fields a client relies on.
func (r *Release) Validate() error {
	if _, err := ParseVersion(r.Version); err != nil {
		return err
	}
	if r.Released.IsZero() || len(r.Programs) == 0 {
		return errors.New("release: missing date or programs")
	}
	seen := map[string]bool{}
	for _, f := range r.Programs {
		if (f.Arch != "x86_64" && f.Arch != "aarch64") || f.Name != AssetName(f.Arch) || seen[f.Arch] {
			return fmt.Errorf("release: bad program %q for %q", f.Name, f.Arch)
		}
		seen[f.Arch] = true
		if len(f.SHA256) != 64 || f.Size <= 0 {
			return fmt.Errorf("release: %s has no checksum or size", f.Name)
		}
	}
	return nil
}

// File returns the release's program for arch.
func (r *Release) File(arch string) (ReleaseFile, bool) {
	for _, f := range r.Programs {
		if f.Arch == arch {
			return f, true
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
