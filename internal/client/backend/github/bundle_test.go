package github

import (
	"context"
	"strings"
	"testing"

	"github.com/tyler-builds/uas/internal/catalog"
	"github.com/tyler-builds/uas/internal/client/backend"
)

// commands records what would have been run.
type commands struct{ ran []string }

func (c *commands) Run(_ context.Context, name string, args []string, _ func(string)) (string, error) {
	c.ran = append(c.ran, name+" "+strings.Join(args, " "))
	return "", nil
}

// bundle is a stand-in Flatpak bundle: a header that names the ref, as real ones do.
func bundle(ref string) []byte {
	return []byte("flatpak\x00\x00" + ref + "\x00metadata\x00[Application]\nname=org.example.Notes\n")
}

func bundleListing(version string, body []byte) catalog.Listing {
	url := "https://github.com/example/notes/releases/download/v" + version + "/notes.flatpak"
	return catalog.Listing{Source: catalog.SourceGitHub, Ref: "example/notes", Name: "Notes", Version: version,
		Assets: []catalog.Asset{{Kind: catalog.AssetFlatpak, Name: "notes.flatpak", URL: url,
			Size: int64(len(body)), SHA256: sum(body)}}}
}

func TestBundleInstallUpdateRemove(t *testing.T) {
	v1, v2 := bundle("app/org.example.Notes/x86_64/v1"), bundle("app/org.example.Notes/x86_64/v2")
	l1, l2 := bundleListing("1", v1), bundleListing("2", v2)
	b := newBackend(t, files{l1.Assets[0].URL: v1, l2.Assets[0].URL: v2}, l1)
	run := &commands{}
	b.Runner = run

	b.Flatpak = ""
	if b.Supports(l1) {
		t.Fatal("a bundle is installable without flatpak")
	}
	b.Flatpak = "flatpak"
	if !b.Supports(l1) {
		t.Fatal("a bundle with a checksum is not installable")
	}
	if err := b.Install(context.Background(), l1, nil); err != nil {
		t.Fatal(err)
	}
	if !b.BundleApps()["org.example.Notes"] {
		t.Errorf("bundle apps = %v", b.BundleApps())
	}
	installed, _ := b.Installed(context.Background())
	if len(installed) != 1 || installed[0].Version != "1" {
		t.Errorf("installed = %+v", installed)
	}

	b.Latest = func(context.Context, string) (catalog.Listing, error) { return l2, nil }
	ups, _ := b.Updates(context.Background())
	if len(ups) != 1 {
		t.Fatalf("updates = %+v", ups)
	}
	run.ran = nil
	if err := b.Update(context.Background(), installed[0], nil); err != nil {
		t.Fatal(err)
	}
	// The new branch goes in, then the old one comes out.
	if len(run.ran) != 3 || !strings.HasPrefix(run.ran[1], "flatpak install --user --noninteractive --reinstall --bundle ") ||
		run.ran[2] != "flatpak uninstall --user --noninteractive app/org.example.Notes/x86_64/v1" {
		t.Errorf("ran = %q", run.ran)
	}

	run.ran = nil
	if err := b.Remove(context.Background(), backend.InstalledApp{Source: catalog.SourceGitHub,
		Ref: "example/notes"}, nil); err != nil {
		t.Fatal(err)
	}
	if len(run.ran) != 1 || run.ran[0] != "flatpak uninstall --user --noninteractive app/org.example.Notes/x86_64/v2" {
		t.Errorf("ran = %q", run.ran)
	}
	if len(b.BundleApps()) != 0 {
		t.Errorf("bundle apps after removal = %v", b.BundleApps())
	}
}

func TestBundleRefRejectsOtherFiles(t *testing.T) {
	l := bundleListing("1", []byte("not a bundle at all"))
	b := newBackend(t, files{l.Assets[0].URL: []byte("not a bundle at all")}, l)
	b.Runner, b.Flatpak = &commands{}, "flatpak"
	if err := b.Install(context.Background(), l, nil); err == nil || !strings.Contains(err.Error(), "not a Flatpak bundle") {
		t.Errorf("err = %v", err)
	}
}
