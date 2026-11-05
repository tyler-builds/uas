package session

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/tyler-builds/uas/internal/client/backend"
)

func TestLauncher(t *testing.T) {
	home, sys := t.TempDir(), t.TempDir()
	t.Setenv("XDG_DATA_HOME", home)
	t.Setenv("XDG_DATA_DIRS", sys)
	write := func(dir, name, body string) string {
		p := filepath.Join(dir, "applications", name)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(body), 0o644)
		return p
	}
	app := "[Desktop Entry]\nType=Application\nName=Notes\nExec=notes\n"
	sysNotes := write(sys, "org.example.Notes.desktop", app)
	write(sys, "org.example.Hidden.desktop", app+"NoDisplay=true\n")
	flatpak := write(filepath.Join(home, "flatpak", "exports", "share"), "org.example.Flat.desktop", app)
	s := &Session{}

	for _, c := range []struct {
		in   backend.InstalledApp
		ids  []string
		want string
	}{
		{backend.InstalledApp{AppID: "org.example.Notes"}, nil, sysNotes},
		{backend.InstalledApp{AppID: "notes", Package: "notes"}, []string{"org.example.Notes"}, sysNotes},
		{backend.InstalledApp{AppID: "org.example.Flat"}, nil, flatpak},
		{backend.InstalledApp{AppID: "org.example.Hidden"}, nil, ""},
		{backend.InstalledApp{AppID: "tree"}, nil, ""},
		{backend.InstalledApp{AppID: "org.example.Notes", Pending: true}, nil, ""},
		{backend.InstalledApp{AppID: "x", Desktop: sysNotes}, nil, sysNotes},
		{backend.InstalledApp{AppID: "../etc/passwd"}, nil, ""},
	} {
		if got := s.Launcher(c.in, c.ids...); got != c.want {
			t.Errorf("Launcher(%+v, %v) = %q, want %q", c.in, c.ids, got, c.want)
		}
	}
}
