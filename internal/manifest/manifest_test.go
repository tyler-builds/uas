package manifest

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func testManifest() Manifest {
	sum := strings.Repeat("ab", 32)
	return Manifest{SchemaVersion: 1, BuiltAt: time.Unix(1770000000, 0).UTC(), Apps: 2,
		Catalog: File{Path: CatalogPath(sum), SHA256: sum, Size: 10, UncompressedSize: 20}}
}

func newKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	s, _, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	k, err := ParsePrivateKey(s)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestSignVerify(t *testing.T) {
	key := newKey(t)
	pub := key.Public().(ed25519.PublicKey)
	data, err := Sign(testManifest(), key)
	if err != nil {
		t.Fatal(err)
	}
	m, err := Verify(data, []ed25519.PublicKey{newKey(t).Public().(ed25519.PublicKey), pub})
	if err != nil {
		t.Fatal(err)
	}
	if m.Apps != 2 || m.Catalog.Size != 10 {
		t.Errorf("manifest = %+v", m)
	}

	pubB64 := base64.StdEncoding.EncodeToString(pub)
	if got, err := ParsePublicKey(pubB64); err != nil || !got.Equal(pub) {
		t.Errorf("ParsePublicKey = %v, %v", got, err)
	}
}

func TestVerifyRejects(t *testing.T) {
	key := newKey(t)
	pub := key.Public().(ed25519.PublicKey)
	data, err := Sign(testManifest(), key)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := Verify(data, []ed25519.PublicKey{newKey(t).Public().(ed25519.PublicKey)}); err == nil {
		t.Error("verified with an untrusted key")
	}

	var env Envelope
	if err := json.Unmarshal(data, &env); err != nil {
		t.Fatal(err)
	}
	env.Payload = []byte(strings.Replace(string(env.Payload), `"apps":2`, `"apps":3`, 1))
	tampered, _ := json.Marshal(env)
	if _, err := Verify(tampered, []ed25519.PublicKey{pub}); err == nil {
		t.Error("verified a tampered payload")
	}

	// A correctly signed manifest pointing outside the catalog naming scheme is still refused.
	bad := testManifest()
	bad.Catalog.Path = "../../etc/passwd"
	payload, _ := json.Marshal(bad)
	env = Envelope{KeyID: KeyID(pub), Payload: payload,
		Signature: ed25519.Sign(key, append([]byte(signingContext), payload...))}
	forged, _ := json.Marshal(env)
	if _, err := Verify(forged, []ed25519.PublicKey{pub}); err == nil {
		t.Error("accepted a bad catalog path")
	}
	if _, err := Sign(bad, key); err == nil {
		t.Error("signed a bad catalog path")
	}
}

func TestReleaseFiles(t *testing.T) {
	file := func(arch, name string) ReleaseFile {
		return ReleaseFile{Arch: arch, Name: name, SHA256: strings.Repeat("a", 64), Size: 1}
	}
	x86, arm := file("x86_64", "uas-linux-x86_64"), file("aarch64", "uas-linux-aarch64")
	at := time.Unix(1770000000, 0)
	for _, tc := range []struct {
		name string
		r    Release
		ok   bool
	}{
		{"both", Release{Version: "0.6.0", Released: at, Programs: []ReleaseFile{x86, arm}}, true},
		{"one", Release{Version: "0.6.0", Released: at, Programs: []ReleaseFile{x86}}, true},
		{"nothing", Release{Version: "0.6.0", Released: at}, false},
		{"bad version", Release{Version: "0.6", Released: at, Programs: []ReleaseFile{x86}}, false},
		{"no date", Release{Version: "0.6.0", Programs: []ReleaseFile{x86}}, false},
		{"other arch's name", Release{Version: "0.6.0", Released: at, Programs: []ReleaseFile{file("x86_64", "uas-linux-aarch64")}}, false},
		{"versioned name", Release{Version: "0.6.0", Released: at, Programs: []ReleaseFile{file("x86_64", "uas-0.6.0-linux-x86_64")}}, false},
		{"unknown arch", Release{Version: "0.6.0", Released: at, Programs: []ReleaseFile{file("riscv64", "uas-linux-riscv64")}}, false},
		{"twice", Release{Version: "0.6.0", Released: at, Programs: []ReleaseFile{x86, x86}}, false},
		{"no checksum", Release{Version: "0.6.0", Released: at, Programs: []ReleaseFile{{Arch: "x86_64", Name: "uas-linux-x86_64", Size: 1}}}, false},
	} {
		if err := tc.r.Validate(); (err == nil) != tc.ok {
			t.Errorf("%s: Validate = %v", tc.name, err)
		}
	}
	r := Release{Version: "0.6.0", Released: at, Programs: []ReleaseFile{x86}}
	if f, ok := r.File("x86_64"); !ok || f != x86 {
		t.Errorf("File = %+v, %v", f, ok)
	}
	if _, ok := r.File("aarch64"); ok {
		t.Error("a build for aarch64 that isn't there")
	}
}
