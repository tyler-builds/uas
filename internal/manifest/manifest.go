// Package manifest describes a published catalog and signs it, so clients can trust a catalog no matter
// where they downloaded it from.
//
// The published file is an Envelope: the manifest JSON as an opaque payload plus an ed25519 signature over
// it. Clients verify the signature against keys built into uas before reading the payload.
package manifest

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/tyler-builds/uas/internal/catalog"
)

// Name is the object name of the published manifest.
const Name = "manifest.json"

// signingContext is prefixed to the payload before signing, so a uas signature can never be replayed as a
// signature over some other kind of message.
const signingContext = "uas-manifest-v1\n"

// catalogPath matches the only object names a manifest may point at.
var catalogPath = regexp.MustCompile(`^catalog-[0-9a-f]{64}\.db\.zst$`)

// Manifest says which catalog file is current.
type Manifest struct {
	SchemaVersion int                    `json:"schema_version"`
	BuiltAt       time.Time              `json:"built_at"`
	Apps          int                    `json:"apps"`
	Sources       []catalog.SourceStatus `json:"sources"`
	Catalog       File                   `json:"catalog"`
}

// File is a published, zstd-compressed catalog database.
type File struct {
	// Path is relative to the base URL the manifest was fetched from.
	Path string `json:"path"`
	// SHA256 is the hex hash of the compressed file.
	SHA256           string `json:"sha256"`
	Size             int64  `json:"size"`
	UncompressedSize int64  `json:"uncompressed_size"`
}

// CatalogPath returns the object name for a compressed catalog with the given hex SHA-256.
func CatalogPath(sha256Hex string) string { return "catalog-" + sha256Hex + ".db.zst" }

// Validate checks the fields a client relies on.
func (m *Manifest) Validate() error {
	if !catalogPath.MatchString(m.Catalog.Path) {
		return fmt.Errorf("manifest: bad catalog path %q", m.Catalog.Path)
	}
	if m.Catalog.Path != CatalogPath(m.Catalog.SHA256) {
		return errors.New("manifest: catalog path does not match its hash")
	}
	if m.Catalog.Size <= 0 || m.Catalog.UncompressedSize <= 0 {
		return errors.New("manifest: missing catalog size")
	}
	if m.BuiltAt.IsZero() {
		return errors.New("manifest: missing build time")
	}
	return nil
}

// Envelope is the signed form of a manifest, as published.
type Envelope struct {
	KeyID     string `json:"key_id"`
	Payload   []byte `json:"payload"`   // the Manifest as JSON; base64 in the envelope
	Signature []byte `json:"signature"` // ed25519 over signingContext + Payload
}

// KeyID is a short, stable name for a public key, so clients know which trusted key to check against.
func KeyID(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return hex.EncodeToString(sum[:8])
}

// Sign returns the published envelope for m.
func Sign(m Manifest, priv ed25519.PrivateKey) ([]byte, error) {
	if err := m.Validate(); err != nil {
		return nil, err
	}
	payload, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	env := Envelope{
		KeyID:     KeyID(priv.Public().(ed25519.PublicKey)),
		Payload:   payload,
		Signature: ed25519.Sign(priv, append([]byte(signingContext), payload...)),
	}
	return json.MarshalIndent(env, "", "  ")
}

// Verify checks an envelope against the trusted keys and returns the manifest inside.
func Verify(data []byte, trusted []ed25519.PublicKey) (Manifest, error) {
	var env Envelope
	if err := json.Unmarshal(data, &env); err != nil {
		return Manifest{}, fmt.Errorf("manifest: %w", err)
	}
	var key ed25519.PublicKey
	for _, k := range trusted {
		if KeyID(k) == env.KeyID {
			key = k
			break
		}
	}
	if key == nil {
		return Manifest{}, fmt.Errorf("manifest: signed by unknown key %q", env.KeyID)
	}
	if !ed25519.Verify(key, append([]byte(signingContext), env.Payload...), env.Signature) {
		return Manifest{}, errors.New("manifest: bad signature")
	}
	var m Manifest
	if err := json.Unmarshal(env.Payload, &m); err != nil {
		return Manifest{}, fmt.Errorf("manifest: %w", err)
	}
	if err := m.Validate(); err != nil {
		return Manifest{}, err
	}
	return m, nil
}

// GenerateKey makes a new signing key. It returns the private key in the encoding ParsePrivateKey reads.
func GenerateKey() (private string, public ed25519.PublicKey, err error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", nil, err
	}
	return base64.StdEncoding.EncodeToString(priv.Seed()), pub, nil
}

// ParsePrivateKey reads a base64-encoded 32-byte ed25519 seed.
func ParsePrivateKey(s string) (ed25519.PrivateKey, error) {
	seed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, errors.New("signing key must be a base64-encoded 32-byte ed25519 seed")
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

// ParsePublicKey reads a base64-encoded ed25519 public key.
func ParsePublicKey(s string) (ed25519.PublicKey, error) {
	b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil || len(b) != ed25519.PublicKeySize {
		return nil, errors.New("public key must be a base64-encoded 32-byte ed25519 key")
	}
	return ed25519.PublicKey(b), nil
}
