package refresh

import (
	"crypto/ed25519"

	"github.com/tyler-builds/uas/internal/manifest"
)

// DefaultBaseURL is where the published catalog lives.
const DefaultBaseURL = "https://uas-catalog.tyler-builds.workers.dev"

// trustedKeys are the public keys whose signatures uas accepts on a manifest. To rotate, add the new key here,
// ship a release, then switch the indexer to the new key and later drop the old one.
var trustedKeys = []string{
	"H337J/k45clvvLip7yVfQkhjOYZhszHwQljCwh52ins=", // key id 0eb8a96d744c0095
}

// TrustedKeys returns the built-in manifest signing keys.
func TrustedKeys() []ed25519.PublicKey {
	keys := make([]ed25519.PublicKey, 0, len(trustedKeys))
	for _, s := range trustedKeys {
		k, err := manifest.ParsePublicKey(s)
		if err != nil {
			panic("refresh: bad built-in key: " + err.Error())
		}
		keys = append(keys, k)
	}
	return keys
}
