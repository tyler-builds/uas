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

func TestElectronFeed(t *testing.T) {
	for cfg, want := range map[string]string{
		"provider: generic\nurl: https://example.com/app/\n":             "electron-updater|https://example.com/app/latest-linux.yml",
		"provider: generic\nurl: https://example.com/a\nchannel: beta\n": "electron-updater|https://example.com/a/beta-linux.yml",
		"provider: github\nowner: o\nrepo: r\n":                          "electron-updater|https://github.com/o/r/releases/latest/download/latest-linux.yml",
		"provider: s3\nbucket: b\nregion: us-east-1\npath: /app\n":       "electron-updater|https://b.s3.us-east-1.amazonaws.com/app/latest-linux.yml",
	} {
		i := &Info{ElectronUpdate: parseYAMLKeys(cfg)}
		u, err := i.ElectronFeed("x86_64")
		if err != nil || u.String() != want {
			t.Errorf("%q: %q, %v; want %q", cfg, u.String(), err, want)
		}
	}
	gh, _ := ParseUpdate("electron-updater|https://github.com/o/r/releases/latest/download/latest-linux.yml")
	if gh.GitHubRepo() != "o/r" {
		t.Errorf("GitHubRepo = %q", gh.GitHubRepo())
	}
	for _, cfg := range []string{"provider: gitlab\n", "provider: generic\nurl: http://example.com\n", ""} {
		i := &Info{ElectronUpdate: parseYAMLKeys(cfg)}
		if _, err := i.ElectronFeed("x86_64"); err == nil {
			t.Errorf("%q: no error", cfg)
		}
	}
}

func TestParseElectronFeed(t *testing.T) {
	feed := `version: 1.325.0-41594
files:
  - url: CurseForge-1.325.0-41594.AppImage
    sha512: xroE1D25RawsjSmWqOepS9CV+ilbgk+0E3L2UeDeyE3Wz+lEks2psBpVDWT871ax+8VnLecf5KN6XETkciMkIw==
    size: 150469174
    blockMapSize: 158282
  - url: CurseForge_1.325.0-41594_amd64.deb
    sha512: MkbXtcn8M9IcMXJZFdnKGyQGrF5Ta3GQAcFD8VXx5Q8Vfv87jMDdyL7owMzHaNe+txytt2Fa0Q3akJv1Q+/2Ow==
    size: 118112656
path: CurseForge-1.325.0-41594.AppImage
sha512: xroE1D25RawsjSmWqOepS9CV+ilbgk+0E3L2UeDeyE3Wz+lEks2psBpVDWT871ax+8VnLecf5KN6XETkciMkIw==
releaseDate: '2026-10-20T12:48:49.418Z'
`
	r, err := ParseElectronFeed(feed)
	if err != nil {
		t.Fatal(err)
	}
	if r.Version != "1.325.0-41594" || r.URL != "CurseForge-1.325.0-41594.AppImage" || r.Size != 150469174 ||
		r.SHA512 != "xroE1D25RawsjSmWqOepS9CV+ilbgk+0E3L2UeDeyE3Wz+lEks2psBpVDWT871ax+8VnLecf5KN6XETkciMkIw==" {
		t.Errorf("release = %+v", r)
	}
	if _, err := ParseElectronFeed("version: 1\npath: app.deb\nsha512: x\n"); err == nil {
		t.Error("accepted a feed with no AppImage")
	}
}

func TestNameFromFile(t *testing.T) {
	for file, want := range map[string]string{
		"/home/u/AppImages/ghostty.appimage":     "Ghostty",
		"Ghostty-1.4.0-anylinux-x86_64.AppImage": "Ghostty",
		"obs-studio_32.0-x86_64.AppImage":        "Obs Studio",
		"CurseForge-1.325.0-41594.AppImage":      "CurseForge",
	} {
		if got := NameFromFile(file); got != want {
			t.Errorf("NameFromFile(%q) = %q, want %q", file, got, want)
		}
	}
}
