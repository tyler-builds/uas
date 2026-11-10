package backend

import (
	"testing"

	"github.com/tyler-builds/uas/internal/catalog"
)

func TestSameApp(t *testing.T) {
	added := func(id, name string) InstalledApp {
		return InstalledApp{Source: catalog.SourceAppImage, Ref: id, AppID: id, Name: name}
	}
	for _, c := range []struct {
		in   InstalledApp
		name string
		ids  []string
		want bool
	}{
		// Named by its launcher's file: the last part of the app's ID, with the same name.
		{added("curseforge", "CurseForge"), "CurseForge", []string{"com.overwolf.curseforge"}, true},
		{added("curseforge", "CurseForge"), "curseforge", []string{"com.overwolf.CurseForge.desktop"}, true},
		{added("t3code", "T3 Code (Beta)"), "T3 Code (Beta)", []string{"github:pingdotgg/t3code", "pingdotgg/t3code"}, true},
		// The same last part is not enough: "notes" names many apps.
		{added("notes", "Notes"), "Sticky Notes", []string{"org.example.notes"}, false},
		// Nor is the same name.
		{added("solo", "Solo"), "Solo", []string{"com.writerduet.writersolo"}, false},
		// Named by its AppStream ID, from its metainfo: that is the app, whatever it calls itself.
		{added("io.github.openpane.OpenPane", "OpenPane (dev)"), "OpenPane", []string{"io.github.openpane.openpane"}, true},
		{added("io.github.openpane.OpenPane", "OpenPane"), "OpenPane", []string{"io.github.other.OpenPane"}, false},
		// Only AppImages added from files: other sources' copies match by their listings.
		{InstalledApp{Source: catalog.SourceFlathub, AppID: "curseforge", Name: "CurseForge"}, "CurseForge",
			[]string{"com.overwolf.curseforge"}, false},
		{added("", "CurseForge"), "CurseForge", []string{"com.overwolf.curseforge"}, false},
		{added("curseforge", ""), "", []string{"com.overwolf.curseforge", ""}, false},
	} {
		if got := c.in.SameApp(c.name, c.ids...); got != c.want {
			t.Errorf("%+v SameApp(%q, %q) = %v, want %v", c.in, c.name, c.ids, got, c.want)
		}
	}
	for in, want := range map[string]string{"com.overwolf.curseforge": "curseforge", "github:pingdotgg/t3code": "t3code",
		"plain": "plain", "": ""} {
		if got := IDTail(in); got != want {
			t.Errorf("IDTail(%q) = %q, want %q", in, got, want)
		}
	}
}
