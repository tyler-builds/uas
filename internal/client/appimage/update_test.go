package appimage

import "testing"

func TestParseUpdate(t *testing.T) {
	good := map[string]Update{
		"gh-releases-zsync|owner|repo|latest|App-*x86_64.AppImage.zsync": {Kind: UpdateGitHubZsync, Owner: "owner",
			Repo: "repo", Release: "latest", Pattern: "App-*x86_64.AppImage.zsync"},
		"gh-releases-direct|o|r|v1.2|App.AppImage": {Kind: UpdateGitHubDirect, Owner: "o", Repo: "r",
			Release: "v1.2", Pattern: "App.AppImage"},
		"zsync|https://example.com/App.AppImage.zsync": {Kind: UpdateZsync, URL: "https://example.com/App.AppImage.zsync"},
	}
	for s, want := range good {
		got, err := ParseUpdate(s)
		if err != nil || got != want {
			t.Errorf("ParseUpdate(%q) = %+v, %v", s, got, err)
		}
		if got.String() != s {
			t.Errorf("String() = %q, want %q", got.String(), s)
		}
	}
	for _, s := range []string{
		"",
		"zsync|http://example.com/App.zsync",
		"gh-releases-zsync|owner|repo|latest",
		"gh-releases-zsync|../x|repo|latest|*.zsync",
		"gh-releases-zsync|owner|repo|latest|../*.zsync",
		"pling-v1-zsync|123|*.zsync",
		"bintray-zsync|a|b|c|d",
	} {
		if _, err := ParseUpdate(s); err == nil {
			t.Errorf("ParseUpdate(%q) succeeded", s)
		}
	}
}

func TestAppID(t *testing.T) {
	i := &Info{DesktopName: "org.example.App.desktop"}
	if i.AppID() != "org.example.App" {
		t.Errorf("from the desktop entry: %q", i.AppID())
	}
	i.Metainfo = []byte(`<component type="desktop-application"><id>org.example.Real.desktop</id></component>`)
	if i.AppID() != "org.example.Real" {
		t.Errorf("from the metainfo: %q", i.AppID())
	}
}
