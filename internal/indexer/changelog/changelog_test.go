package changelog

import (
	"strings"
	"testing"
	"time"

	"github.com/tyler-builds/uas/internal/catalog"
	"github.com/tyler-builds/uas/internal/indexer/appstream"
)

func TestClean(t *testing.T) {
	in := "<!-- Thanks for releasing! -->\r\n## What's Changed\r\n" +
		"* Fix crash on start by @someone in https://github.com/o/r/pull/1\r\n" +
		"![screenshot](https://example.com/a.png)\r\n" +
		"<details><summary>More</summary>\r\nHidden fixes\r\n</details>\r\n\r\n\r\n\r\n" +
		"See <https://example.com/notes> for a < b > c.\r\n" +
		"**Full Changelog**: https://github.com/o/r/compare/v1.0...v1.1\r\n"
	want := "**What's Changed**\n" +
		"* Fix crash on start by @someone in https://github.com/o/r/pull/1\n\n" +
		"More\nHidden fixes\n\n" +
		"See <https://example.com/notes> for a < b > c."
	got, cut := Clean(in)
	if got != want || cut {
		t.Errorf("Clean = %q, %v\nwant %q", got, cut, want)
	}
}

func TestCleanCuts(t *testing.T) {
	var b strings.Builder
	b.WriteString("Intro\n\n```\n")
	for range 200 {
		b.WriteString("a line of code\n")
	}
	b.WriteString("```\n")
	got, cut := Clean(b.String())
	if !cut || len(got) > MaxChars+10 {
		t.Fatalf("not cut: %d chars, cut %v", len(got), cut)
	}
	if strings.Count(got, "```")%2 != 0 || !strings.HasSuffix(got, "```\n\n…") {
		t.Errorf("code block left open:\n%s", got[len(got)-40:])
	}

	long := strings.Repeat("word ", 1000)
	got, cut = Clean(long)
	if !cut || strings.Contains(got, "wor\n") || !strings.HasSuffix(got, "word\n\n…") {
		t.Errorf("one paragraph cut mid-word: %q", got[len(got)-20:])
	}
}

func TestFromAppStream(t *testing.T) {
	day := func(d int) time.Time { return time.Date(2026, 10, d, 0, 0, 0, 0, time.UTC) }
	rels := []appstream.Release{
		{Version: "2.0~beta", Type: "development", Time: day(9), Notes: "Testing"},
		{Version: "1.6", Time: day(8)}, // no notes
		{Version: "1.5", Time: day(7), Notes: "Faster", URL: "https://example.com/1.5"},
		{Version: "1.4", Time: day(6), Notes: "1.4"},
		{Version: "1.3", Time: day(5), Notes: "1.3"},
		{Version: "1.2", Time: day(4), Notes: "1.2"},
		{Version: "1.1", Time: day(3), Notes: "Too old to be what's new"},
	}
	var l catalog.Listing
	FromAppStream(&l, rels)
	var got []string
	for _, c := range l.Changelog {
		got = append(got, c.Version)
	}
	if strings.Join(got, " ") != "1.5 1.4 1.3 1.2" {
		t.Errorf("versions = %v", got)
	}
	if l.Changelog[0].URL != "https://example.com/1.5" || !l.Changelog[0].Date.Equal(day(7)) {
		t.Errorf("first = %+v", l.Changelog[0])
	}
}
