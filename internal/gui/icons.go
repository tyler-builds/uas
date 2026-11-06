package gui

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tyler-builds/jamb/ui"
)

// iconLoader downloads app icons and screenshots and keeps them on disk, so a scroll through the catalog
// fetches each image once.
type iconLoader struct {
	client *http.Client
	dir    string // ~/.cache/uas/images
}

const maxImageBytes = 8 << 20

func newIconLoader() *iconLoader {
	dir := ""
	if c, err := os.UserCacheDir(); err == nil {
		dir = filepath.Join(c, "uas", "images")
	}
	return &iconLoader{client: &http.Client{Timeout: time.Minute}, dir: dir}
}

// picture is a decoded image: a raster image or an SVG.
type picture struct {
	img *ui.ImageSource
	svg *ui.SVGSource
}

// load returns the bytes of an image URL, from the disk cache when it has them.
func (l *iconLoader) load(ctx context.Context, url string) ([]byte, error) {
	if path, ok := strings.CutPrefix(url, "file://"); ok {
		// An icon uas saved from an AppImage it installed.
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		return io.ReadAll(io.LimitReader(f, 4<<20))
	}
	if !strings.HasPrefix(url, "https://") {
		return nil, fmt.Errorf("not an https URL: %q", url)
	}
	sum := sha256.Sum256([]byte(url))
	path := ""
	if l.dir != "" {
		path = filepath.Join(l.dir, hex.EncodeToString(sum[:12]))
		if b, err := os.ReadFile(path); err == nil {
			return b, nil
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "uas")
	resp, err := l.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound && strings.Contains(url, "/128x128@2/") {
		// Catalogs built before uas preferred scale-1 icons can point at high-DPI icons Flathub doesn't
		// have; its scale-1 icon sits next to it.
		resp.Body.Close()
		return l.load(ctx, strings.Replace(url, "/128x128@2/", "/128x128/", 1))
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxImageBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxImageBytes {
		return nil, errors.New("image too large")
	}
	if path != "" && os.MkdirAll(l.dir, 0o755) == nil {
		_ = os.WriteFile(path, b, 0o644)
	}
	return b, nil
}

func decodePicture(b []byte) (picture, error) {
	head := b[:min(len(b), 512)]
	if bytes.Contains(head, []byte("<svg")) || bytes.HasPrefix(bytes.TrimSpace(head), []byte("<?xml")) {
		svg, err := ui.SVGFromBytes(b)
		return picture{svg: svg}, err
	}
	img, err := ui.ImageFromBytes(b)
	return picture{img: img}, err
}

// appIcon shows an app's icon at size, or a placeholder while it loads or when there is none.
func (a *App) appIcon(ctx *ui.Context, url string, size float32, name string) ui.Element {
	placeholder := monogram(ctx, name, size)
	if url == "" {
		return placeholder
	}
	return ui.Component(func(ctx *ui.Context) ui.Element {
		res := ui.Query(ctx, "image:"+url, func(c context.Context) (picture, error) {
			b, err := a.icons.load(c, url)
			if err != nil {
				return picture{}, err
			}
			return decodePicture(b)
		}, ui.QueryStaleTime(24*time.Hour), ui.QueryCacheTime(2*time.Minute))
		switch {
		case res.Data.img != nil:
			return ui.Image(res.Data.img).Size(ui.Px(size), ui.Px(size)).Fit(ui.FitContain).Alt(name + " icon")
		case res.Data.svg != nil:
			return ui.SVG(res.Data.svg).Size(ui.Px(size), ui.Px(size)).Alt(name + " icon")
		}
		return placeholder
	}).Key("icon:" + url)
}

// monogramTints are the hues of app monograms, picked by name so an app keeps its color.
var monogramTints = []string{"#5eead4", "#93c5fd", "#c4b5fd", "#fcd34d", "#fda4af", "#86efac"}

// monogram stands in for a missing app icon: the name's first letter on a tile tinted by the name.
func monogram(ctx *ui.Context, name string, size float32) ui.Element {
	t := ctx.Theme()
	letter := "?"
	for _, r := range name {
		letter = strings.ToUpper(string(r))
		break
	}
	sum := 0
	for _, r := range name {
		sum += int(r)
	}
	c := ui.Hex(monogramTints[sum%len(monogramTints)])
	if !t.Dark {
		c = c.Darken(0.45)
	}
	return ui.Div().Size(ui.Px(size), ui.Px(size)).Radius(size*0.24).Background(c.Alpha(0.14)).
		Border(1, c.Alpha(0.22)).Center().Shrink(0).
		Child(ui.Text(letter).FontSize(size * 0.44).Semibold().TextColor(c))
}

// screenshot shows an app screenshot scaled to width, keeping its aspect ratio.
func (a *App) screenshot(url string, width float32, caption string) ui.Element {
	return ui.Component(func(ctx *ui.Context) ui.Element {
		t := ctx.Theme()
		res := ui.Query(ctx, "image:"+url, func(c context.Context) (picture, error) {
			b, err := a.icons.load(c, url)
			if err != nil {
				return picture{}, err
			}
			return decodePicture(b)
		}, ui.QueryStaleTime(24*time.Hour), ui.QueryCacheTime(10*time.Minute))
		if res.Data.img == nil {
			if res.Err != nil {
				return ui.Div()
			}
			return ui.Skeleton().Width(ui.Px(width)).Height(ui.Px(width * 9 / 16))
		}
		w, h := res.Data.img.Size()
		height := width
		if w > 0 {
			height = width * float32(h) / float32(w)
		}
		return ui.Div().Column().Gap(6).
			Child(ui.Div().Radius(radiusM).Border(1, t.Border).ClipContent().
				Child(ui.Image(res.Data.img).Size(ui.Px(width), ui.Px(height)).Fit(ui.FitContain).Alt(caption))).
			Child(ui.If(caption != "", ui.Text(caption).FontSize(12).TextColor(t.TextMuted)))
	}).Key("shot:" + url)
}
