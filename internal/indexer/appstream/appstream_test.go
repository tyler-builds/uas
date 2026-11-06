package appstream

import (
	"strings"
	"testing"
)

func TestBestRemoteIcon(t *testing.T) {
	c := &Component{Icons: []Icon{
		{Type: "cached", Value: "x.png", Width: 256},
		{Type: "remote", Value: "https://e/64.png", Width: 64, Scale: 1},
		{Type: "remote", Value: "https://e/128@2.png", Width: 128, Scale: 2},
		{Type: "remote", Value: "https://e/128.png", Width: 128},
	}}
	if got := c.BestRemoteIcon(); got != "https://e/128.png" {
		t.Errorf("BestRemoteIcon = %q, want the largest scale-1 icon", got)
	}
	c.Icons = []Icon{{Type: "remote", Value: "https://e/64@2.png", Width: 64, Scale: 2},
		{Type: "remote", Value: "https://e/128@2.png", Width: 128, Scale: 2}}
	if got := c.BestRemoteIcon(); got != "https://e/128@2.png" {
		t.Errorf("BestRemoteIcon with only high-DPI icons = %q", got)
	}
	if got := (&Component{}).BestRemoteIcon(); got != "" {
		t.Errorf("BestRemoteIcon with none = %q", got)
	}
}

func TestReleaseNotes(t *testing.T) {
	const doc = `<components><component type="desktop-application"><id>org.example.App</id><name>App</name>
<releases>
  <release version="1.0" date="2026-01-01"><description><p>First.</p></description></release>
  <release version="1.1" timestamp="1780000000">
    <description><p>Second:</p><ul><li>Faster</li></ul></description>
    <description xml:lang="de"><p>Zweite</p></description>
    <url type="details">https://example.org/1.1</url>
  </release>
</releases></component></components>`
	var c *Component
	if err := Parse(strings.NewReader(doc), func(got *Component) error { c = got; return nil }); err != nil {
		t.Fatal(err)
	}
	if len(c.Releases) != 2 {
		t.Fatalf("releases = %+v", c.Releases)
	}
	if r := c.Releases[0]; r.Version != "1.1" || r.Notes != "Second:\n\n- Faster" || r.URL != "https://example.org/1.1" {
		t.Errorf("newest = %+v", r)
	}
	if r := c.Releases[1]; r.Notes != "First." || r.URL != "" {
		t.Errorf("oldest = %+v", r)
	}
}
