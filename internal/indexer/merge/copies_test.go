package merge

import (
	"sort"
	"strings"
	"testing"

	"github.com/tyler-builds/uas/internal/catalog"
)

func flathub(id, name, homepage, vcs string, installs int) catalog.Listing {
	return catalog.Listing{Source: catalog.SourceFlathub, Ref: "app/" + id + "/x86_64/stable", AppStreamID: id,
		Name: name, Homepage: homepage, VCSURL: vcs, Installs: installs}
}

func gh(repo, name, summary string, stars int) catalog.Listing {
	return catalog.Listing{Source: catalog.SourceGitHub, Ref: repo, Name: name, Summary: summary, Stars: stars,
		VCSURL: "https://github.com/" + repo}
}

func appsByID(apps []catalog.App) map[string][]string {
	out := map[string][]string{}
	for _, a := range apps {
		for _, l := range a.Listings {
			out[a.ID] = append(out[a.ID], string(l.Source)+":"+l.Ref)
		}
		sort.Strings(out[a.ID])
	}
	return out
}

// TestMergeAcrossThreeSources links a repo URL shared by three sources, one group each.
func TestMergeAcrossThreeSources(t *testing.T) {
	apps, _ := Merge([]catalog.Listing{
		flathub("io.neovim.nvim", "Neovim", "https://neovim.io/", "https://github.com/neovim/neovim", 100),
		{Source: catalog.SourceHomebrew, Ref: "neovim", Name: "neovim", VCSURL: "https://github.com/neovim/neovim"},
		gh("neovim/neovim", "Neovim", "Vim-fork", 100000),
	})
	if len(apps) != 1 || len(apps[0].Listings) != 3 {
		t.Errorf("apps = %v", appsByID(apps))
	}
}

func TestCopies(t *testing.T) {
	apps, hidden := Merge([]catalog.Listing{
		// The developer's own repo joins the app: its owner is in the AppStream ID.
		flathub("cc.arduino.IDE2", "Arduino IDE", "https://www.arduino.cc", "", 30000),
		gh("arduino/arduino-ide", "Arduino IDE", "Arduino IDE 2.x", 3000),
		// Someone's AppImage of an app: hidden, whatever its stars.
		flathub("com.getpostman.Postman", "Postman", "https://www.postman.com", "", 100000),
		gh("someone/postman-appimage", "Postman", "Postman as an AppImage", 5000),
		// A small repo using an established app's name: hidden.
		flathub("org.kde.pix", "Pix", "https://apps.kde.org/pix", "", 40000),
		gh("someone/PiX", "PiX", "A pixel editor", 200),
		// A popular project that shares a name: a different app, kept.
		flathub("edu.mit.Scratch", "Scratch", "https://scratch.mit.edu", "", 30000),
		gh("someone/scratch", "Scratch", "A notes app", 1600),
		// The developer's second repo, when the app already has their GitHub releases: hidden as a duplicate.
		flathub("io.neovim.nvim", "Neovim", "https://neovim.io/", "https://github.com/neovim/neovim", 500000),
		gh("neovim/neovim", "Neovim", "Vim-fork", 100000),
		gh("neovim/neovim-releases", "Neovim", "Unsupported Nvim releases", 300),
		// A COPR build of a reviewed app by someone else: hidden. By the developer: kept.
		flathub("net.cozic.joplin_desktop", "Joplin", "https://joplinapp.org", "https://github.com/laurent22/joplin", 90000),
		{Source: catalog.SourceCOPR, Ref: "fedora-45/taw/joplin/joplin", Project: "taw/joplin",
			AppStreamID: "net.cozic.joplin_desktop", Name: "Joplin"},
		flathub("org.example.Tool", "Tool", "https://tool.example", "https://github.com/toolmaker/tool", 50),
		{Source: catalog.SourceCOPR, Ref: "fedora-45/toolmaker/tool/tool", Project: "toolmaker/tool",
			AppStreamID: "org.example.Tool", Name: "Tool"},
		// A GitHub app with no reviewed namesake: untouched.
		gh("pingdotgg/t3code", "T3 Code", "", 46000),
	})
	got := appsByID(apps)
	want := map[string][]string{
		"cc.arduino.IDE2":          {"flathub:app/cc.arduino.IDE2/x86_64/stable", "github:arduino/arduino-ide"},
		"com.getpostman.Postman":   {"flathub:app/com.getpostman.Postman/x86_64/stable"},
		"org.kde.pix":              {"flathub:app/org.kde.pix/x86_64/stable"},
		"edu.mit.Scratch":          {"flathub:app/edu.mit.Scratch/x86_64/stable"},
		"github:someone/scratch":   {"github:someone/scratch"},
		"io.neovim.nvim":           {"flathub:app/io.neovim.nvim/x86_64/stable", "github:neovim/neovim"},
		"net.cozic.joplin_desktop": {"flathub:app/net.cozic.joplin_desktop/x86_64/stable"},
		"org.example.Tool":         {"copr:fedora-45/toolmaker/tool/tool", "flathub:app/org.example.Tool/x86_64/stable"},
		"github:pingdotgg/t3code":  {"github:pingdotgg/t3code"},
	}
	for id, w := range want {
		if strings.Join(got[id], " ") != strings.Join(w, " ") {
			t.Errorf("%s = %v, want %v", id, got[id], w)
		}
	}
	if len(got) != len(want) {
		t.Errorf("apps = %v", got)
	}
	var refs []string
	for _, h := range hidden {
		refs = append(refs, h.Ref)
	}
	sort.Strings(refs)
	if strings.Join(refs, " ") != "fedora-45/taw/joplin/joplin neovim/neovim-releases someone/PiX someone/postman-appimage" {
		t.Errorf("hidden = %q", refs)
	}
}

func TestOfficialHelpers(t *testing.T) {
	for id, want := range map[string]string{"io.github.JonMagon.KDiskMark": "jonmagon", "org.kde.pix": "kde",
		"com.github.app": "", "cc.arduino.IDE2": "arduino", "app": ""} {
		if got := idOwner(id); got != want {
			t.Errorf("idOwner(%q) = %q, want %q", id, got, want)
		}
	}
	for u, want := range map[string]string{"https://lmms.io/": "lmms", "https://www.arduino.cc": "arduino",
		"https://github.com/x": "", "https://foo.github.io/app": "", "": ""} {
		if got := siteName(u); got != want {
			t.Errorf("siteName(%q) = %q, want %q", u, got, want)
		}
	}
}
