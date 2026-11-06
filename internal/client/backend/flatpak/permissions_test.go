package flatpak

import (
	"context"
	"errors"
	"slices"
	"testing"
)

func TestPermissions(t *testing.T) {
	r := &fakeRunner{outputs: map[string]string{
		"info --show-permissions org.kde.okular": "[Context]\nshared=ipc;network;\nsockets=fallback-x11;pulseaudio;wayland;\n" +
			"devices=dri;\nfeatures=bluetooth;\nfilesystems=xdg-config/kdeglobals:ro;home:ro;\n\n" +
			"[Session Bus Policy]\norg.kde.KGlobalSettings=talk\n",
		"override --user --show org.kde.okular": "[Context]\nfilesystems=home:ro;!host;\n",
	}}
	b := &Backend{Runner: r, Path: "flatpak"}
	ps, err := b.Permissions(context.Background(), "org.kde.okular")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Permission{}
	for _, p := range ps {
		got[p.Key] = p
	}
	want := map[string][2]bool{ // allowed, changed
		"network": {true, false}, "home": {true, true}, "host": {false, true}, "downloads": {true, false},
		"devices": {false, false}, "audio": {true, false}, "bluetooth": {true, false}, "x11": {false, false},
	}
	for k, w := range want {
		p := got[k]
		if p.Allowed != w[0] || p.Changed != w[1] {
			t.Errorf("%s: allowed %v changed %v, want %v %v", k, p.Allowed, p.Changed, w[0], w[1])
		}
	}
	if got["downloads"].Note == "" {
		t.Error("Downloads should say it comes with the home folder")
	}
}

func TestSetPermission(t *testing.T) {
	r := &fakeRunner{}
	b := &Backend{Runner: r, Path: "flatpak"}
	ctx := context.Background()
	if err := b.SetPermission(ctx, "org.kde.okular", "network", false); err != nil {
		t.Fatal(err)
	}
	if err := b.SetPermission(ctx, "org.kde.okular", "home", true); err != nil {
		t.Fatal(err)
	}
	if err := b.ResetPermissions(ctx, "org.kde.okular"); err != nil {
		t.Fatal(err)
	}
	want := []string{"flatpak override --user --unshare=network org.kde.okular",
		"flatpak override --user --filesystem=home org.kde.okular", "flatpak override --user --reset org.kde.okular"}
	if !slices.Equal(r.calls, want) {
		t.Errorf("calls = %q", r.calls)
	}
	if err := b.SetPermission(ctx, "org.kde.okular", "root", true); !errors.Is(err, ErrUnknownPermission) {
		t.Errorf("unknown key: %v", err)
	}
	if err := b.SetPermission(ctx, "--filesystem=/", "home", true); err == nil {
		t.Error("an option as the app ID should be refused")
	}
}
