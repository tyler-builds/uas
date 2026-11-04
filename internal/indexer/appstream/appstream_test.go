package appstream

import "testing"

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
