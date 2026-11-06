package sysapps

import (
	"os"
	"path/filepath"
	"testing"
)

func TestScan(t *testing.T) {
	meta, apps := t.TempDir(), t.TempDir()
	write := func(dir, name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(meta, "com.valvesoftware.Steam.metainfo.xml", `<?xml version="1.0"?>
<component type="desktop-application"><id>com.valvesoftware.Steam</id>
  <name>Steam</name><name xml:lang="de">Steam DE</name>
  <launchable type="desktop-id">steam.desktop</launchable>
  <releases><release version="1.0.0.87"/><release version="1.0.0.86"/></releases>
</component>`)
	write(meta, "old.appdata.xml", `<component type="desktop"><id>old.desktop</id><name>Old</name></component>`)
	write(meta, "font.metainfo.xml", `<component type="font"><id>org.example.font</id></component>`)
	write(meta, "hidden.metainfo.xml", `<component type="desktop-application"><id>org.example.Hidden</id></component>`)
	write(meta, "nolauncher.metainfo.xml", `<component type="desktop-application"><id>org.example.Gone</id></component>`)
	write(meta, "broken.metainfo.xml", `<component`)
	write(apps, "steam.desktop", "[Desktop Entry]\nName=Steam\n")
	write(apps, "old.desktop", "[Desktop Entry]\nName=Old\n")
	write(apps, "org.example.Hidden.desktop", "[Desktop Entry]\nNoDisplay=true\n")

	got := Scan([]string{meta}, []string{apps})
	if len(got) != 2 {
		t.Fatalf("got %+v", got)
	}
	s := got["com.valvesoftware.Steam"]
	if s.Name != "Steam" || s.Version != "1.0.0.87" || s.Desktop != filepath.Join(apps, "steam.desktop") {
		t.Errorf("steam = %+v", s)
	}
	if o := got["old"]; o.Desktop != filepath.Join(apps, "old.desktop") {
		t.Errorf("old = %+v", o)
	}
}
