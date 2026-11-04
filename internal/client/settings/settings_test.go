package settings

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadMissingGivesDefaults(t *testing.T) {
	s, err := Load(filepath.Join(t.TempDir(), "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	if s != Default() || s.FlatpakScope != ScopeUser || s.Telemetry {
		t.Errorf("settings = %+v", s)
	}
}

func TestSaveLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "uas", "settings.json")
	want := Settings{FlatpakScope: ScopeSystem}
	if err := Save(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestLoadRejectsUnknownScope(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(`{"flatpak_scope":"everyone"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected an error")
	}
}
