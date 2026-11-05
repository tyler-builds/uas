package github

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"io"
	"net/http"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/tyler-builds/uas/internal/indexer/fetch"
)

// Where repos keep their app's icon, best first: the Linux icon theme folders packagers install from
// (data/icons/hicolor/256x256/apps/org.example.App.png), Tauri's and electron-builder's standard places, then
// files named icon or logo near the top. notRepoIcon leaves out other projects' files, tests and the like.
var (
	themeIcon   = regexp.MustCompile(`(?i)(^|/)hicolor/(\d+)x\d+/apps/[^/]+\.png$|(^|/)hicolor/scalable/apps/[^/]+\.svg$`)
	builderIcon = regexp.MustCompile(`(?i)^(src-tauri/icons/(icon|128x128@2x|128x128)\.png|build/icon\.(png|svg)|` +
		`build/icons/(512x512|256x256)\.png|(resources|assets|res|public)/(icon|app-?icon)\.(png|svg))$`)
	namedIcon   = regexp.MustCompile(`(?i)^(icon|app[-_]?icon|logo|logo[-_]?(square|icon)|appicon)$`)
	notRepoIcon = regexp.MustCompile(`(?i)(^|/)(node_modules|vendor|3rdparty|third_party|thirdparty|external|extern|` +
		`deps|tests?|testdata|fixtures?|examples?|samples?|screenshots?|badges?|flags?|emoji|fonts?|` +
		`\.repos|\.yarn|website/blog|docs/blog|plugins|extensions)/|symbolic|favicon`)
)

// maxIconCandidates is how many likely icon files are downloaded before giving up.
const maxIconCandidates = 4

// repoIcon finds the app's icon among a repo's files, saves it in the icon directory and returns its file
// name, or "". An icon must be roughly square: PNGs at least 64 pixels, SVGs no larger than maxSVGIcon.
func (f *Fetcher) repoIcon(ctx context.Context, repo string, paths []string) string {
	type cand struct {
		path  string
		score int
	}
	var cands []cand
	for _, p := range paths {
		ext := strings.ToLower(path.Ext(p))
		if (ext != ".png" && ext != ".svg") || notRepoIcon.MatchString(p) {
			continue
		}
		depth := strings.Count(p, "/")
		score := 0
		switch m := themeIcon.FindStringSubmatch(p); {
		case m != nil:
			size, _ := strconv.Atoi(m[2])
			switch {
			case ext == ".svg":
				score = 300
			case size >= 128 && size <= 512:
				score = 290 + min(size, 256)/32 // 256 beats 128, and 512 is scaled down anyway
			default:
				score = 200 + size/16
			}
		case builderIcon.MatchString(p):
			score = 250
		case namedIcon.MatchString(strings.TrimSuffix(path.Base(p), path.Ext(p))) && depth <= 3:
			score = 100 - 10*depth
			if strings.HasPrefix(strings.ToLower(path.Base(p)), "logo") {
				score -= 5 // logos are often wordmarks; the square check weeds most out
			}
		}
		if score > 0 {
			cands = append(cands, cand{p, score})
		}
	}
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].score != cands[j].score {
			return cands[i].score > cands[j].score
		}
		return cands[i].path < cands[j].path
	})
	for i, c := range cands {
		if i == maxIconCandidates {
			break
		}
		b, err := f.download(ctx, f.rawURL(repo, c.path), 1<<20)
		if err != nil {
			continue
		}
		ext := strings.ToLower(path.Ext(c.path))
		if !squareIcon(b, ext) {
			continue
		}
		if name, err := f.saveIcon(b, ext); err == nil {
			return name
		}
	}
	return ""
}

// squareIcon reports whether b is a usable icon: a PNG at least 64 pixels with sides within 10% of each
// other, or an SVG whose viewBox (or width and height) is about square, or says nothing about its shape.
func squareIcon(b []byte, ext string) bool {
	var w, h float64
	switch ext {
	case ".png":
		cfg, _, err := image.DecodeConfig(bytes.NewReader(b))
		if err != nil || cfg.Width < 64 {
			return false
		}
		w, h = float64(cfg.Width), float64(cfg.Height)
	case ".svg":
		if len(b) > maxSVGIcon || !bytes.Contains(b, []byte("<svg")) {
			return false
		}
		var ok bool
		if w, h, ok = svgSize(b); !ok {
			return true
		}
	default:
		return false
	}
	return w > 0 && h > 0 && w <= 1.1*h && h <= 1.1*w
}

var (
	svgViewBox = regexp.MustCompile(`<svg[^>]*?\sviewBox\s*=\s*["']\s*[-\d.]+[\s,]+[-\d.]+[\s,]+([\d.]+)[\s,]+([\d.]+)`)
	svgWidth   = regexp.MustCompile(`<svg[^>]*?\swidth\s*=\s*["']([\d.]+)(px)?["']`)
	svgHeight  = regexp.MustCompile(`<svg[^>]*?\sheight\s*=\s*["']([\d.]+)(px)?["']`)
)

// svgSize reads an SVG's shape from its viewBox, or its width and height.
func svgSize(b []byte) (w, h float64, ok bool) {
	if m := svgViewBox.FindSubmatch(b); m != nil {
		w, _ = strconv.ParseFloat(string(m[1]), 64)
		h, _ = strconv.ParseFloat(string(m[2]), 64)
		return w, h, true
	}
	mw, mh := svgWidth.FindSubmatch(b), svgHeight.FindSubmatch(b)
	if mw == nil || mh == nil {
		return 0, 0, false
	}
	w, _ = strconv.ParseFloat(string(mw[1]), 64)
	h, _ = strconv.ParseFloat(string(mh[1]), 64)
	return w, h, true
}

// download gets a small file, at most limit bytes.
func (f *Fetcher) download(ctx context.Context, u string, limit int64) ([]byte, error) {
	c, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(c, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", fetch.UserAgent)
	resp, err := f.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", u, resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err == nil && int64(len(b)) > limit {
		err = fmt.Errorf("%s: larger than %d bytes", u, limit)
	}
	return b, err
}
