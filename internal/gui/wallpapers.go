package gui

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/binary"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"image"
	"image/draw"
	_ "image/gif" // pictures of other kinds than jamb decodes are decoded here
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/kpfaulkner/jxl-go/core"
	"github.com/tyler-builds/jamb/access"
	"github.com/tyler-builds/jamb/dialog"
	"github.com/tyler-builds/jamb/icons"
	"github.com/tyler-builds/jamb/ui"

	"github.com/tyler-builds/uas/internal/client/appearance"
)

// Wallpapers: thumbnails of pictures, the Wallpaper row of a preset's page, and the chooser of the system's
// wallpapers. See appearance.Wallpaper.

// thumbWidth is the width thumbnails are scaled down to, in pixels: enough for a preset card's preview at
// twice the scale. Wallpapers are often 4K or more, so they are never shown at their own size.
const thumbWidth = 520

// maxThumbSource is the largest picture file decoded for a thumbnail, and maxThumbPixels its most pixels.
const (
	maxThumbSource = 96 << 20
	maxThumbPixels = 120_000_000
)

// thumbSlots limits how many pictures are decoded at once, so a grid of them doesn't take every core.
var thumbSlots = make(chan struct{}, 2)

// errNoThumb is a picture whose kind uas can't draw.
var errNoThumb = errors.New("uas can't draw this kind of picture")

// thumbnail decodes a picture into a small one for the screen, off the UI thread: from the desktop's own
// thumbnail cache when it has a fresh one, else by decoding the file and scaling it down. An SVG is drawn
// as vectors. A GNOME slideshow is shown by its first picture.
func thumbnail(path string) (picture, error) {
	if strings.EqualFold(filepath.Ext(path), ".xml") {
		first, err := slideshowPicture(path)
		if err != nil {
			return picture{}, err
		}
		path = first
	}
	if img, ok := cachedThumbnail(path); ok {
		return picture{img: ui.ImageFromImage(img)}, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return picture{}, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxThumbSource+1))
	if err != nil {
		return picture{}, err
	}
	if len(b) > maxThumbSource {
		return picture{}, errors.New("the picture is too large to preview")
	}
	if strings.EqualFold(filepath.Ext(path), ".svg") {
		svg, err := ui.SVGFromBytes(b)
		return picture{svg: svg}, err
	}
	thumbSlots <- struct{}{}
	defer func() { <-thumbSlots }()
	var img image.Image
	if bytes.HasPrefix(b, []byte{0xff, 0x0a}) || bytes.HasPrefix(b, []byte("\x00\x00\x00\x0cJXL ")) {
		img, err = decodeJXL(b)
	} else {
		cfg, _, cerr := image.DecodeConfig(bytes.NewReader(b))
		switch {
		case cerr != nil:
			return picture{}, errNoThumb
		case cfg.Width*cfg.Height > maxThumbPixels:
			return picture{}, errors.New("the picture is too large to preview")
		}
		img, _, err = image.Decode(bytes.NewReader(b))
	}
	if err != nil {
		return picture{}, errNoThumb
	}
	return picture{img: ui.ImageFromImage(shrink(img, thumbWidth))}, nil
}

// decodeJXL decodes a JPEG XL picture. The decoder is pure Go and doesn't support every picture (GNOME's
// with noise, for one), and can panic on those.
func decodeJXL(b []byte) (img image.Image, err error) {
	defer func() {
		if r := recover(); r != nil {
			img, err = nil, fmt.Errorf("jxl: %v", r)
		}
	}()
	j, err := core.NewJXLDecoder(bytes.NewReader(b), nil).Decode()
	if err != nil {
		return nil, err
	}
	return j.ToImage()
}

// shrink scales a picture down to a width, by averaging the pixels each new one covers.
func shrink(src image.Image, width int) image.Image {
	sb := src.Bounds()
	sw, sh := sb.Dx(), sb.Dy()
	if sw <= width || sw == 0 || sh == 0 {
		return src
	}
	rgba := image.NewRGBA(image.Rect(0, 0, sw, sh))
	draw.Draw(rgba, rgba.Bounds(), src, sb.Min, draw.Src)
	dw := width
	dh := max(1, sh*dw/sw)
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	for y := 0; y < dh; y++ {
		y0, y1 := y*sh/dh, max((y+1)*sh/dh, y*sh/dh+1)
		for x := 0; x < dw; x++ {
			x0, x1 := x*sw/dw, max((x+1)*sw/dw, x*sw/dw+1)
			var r, g, b, a, n uint32
			for sy := y0; sy < y1; sy++ {
				row := rgba.Pix[sy*rgba.Stride+x0*4 : sy*rgba.Stride+x1*4]
				for i := 0; i < len(row); i += 4 {
					r += uint32(row[i])
					g += uint32(row[i+1])
					b += uint32(row[i+2])
					a += uint32(row[i+3])
					n++
				}
			}
			o := y*dst.Stride + x*4
			dst.Pix[o], dst.Pix[o+1], dst.Pix[o+2], dst.Pix[o+3] = uint8(r/n), uint8(g/n), uint8(b/n), uint8(a/n)
		}
	}
	return dst
}

// cachedThumbnail reads the desktop's thumbnail of a picture, as the freedesktop.org thumbnail spec keeps
// them (~/.cache/thumbnails/<size>/<md5 of the URI>.png), when it is as new as the picture.
func cachedThumbnail(path string) (image.Image, bool) {
	cache, err := os.UserCacheDir()
	if err != nil {
		return nil, false
	}
	fi, err := os.Stat(path)
	if err != nil {
		return nil, false
	}
	sum := md5.Sum([]byte(appearance.PathURI(path)))
	name := hex.EncodeToString(sum[:]) + ".png"
	for _, size := range []string{"large", "x-large", "xx-large", "normal"} {
		b, err := os.ReadFile(filepath.Join(cache, "thumbnails", size, name))
		if err != nil {
			continue
		}
		if mtime, ok := pngText(b, "Thumb::MTime"); !ok || mtime != strconv.FormatInt(fi.ModTime().Unix(), 10) {
			continue
		}
		if img, _, err := image.Decode(bytes.NewReader(b)); err == nil {
			return img, true
		}
	}
	return nil, false
}

// pngText returns a tEXt field of a PNG file.
func pngText(b []byte, key string) (string, bool) {
	if !bytes.HasPrefix(b, []byte("\x89PNG\r\n\x1a\n")) {
		return "", false
	}
	b = b[8:]
	for len(b) >= 12 {
		n := int(binary.BigEndian.Uint32(b))
		typ := string(b[4:8])
		if n < 0 || len(b) < 12+n {
			return "", false
		}
		if typ == "tEXt" {
			k, v, _ := bytes.Cut(b[8:8+n], []byte{0})
			if string(k) == key {
				return string(v), true
			}
		}
		if typ == "IDAT" || typ == "IEND" {
			return "", false
		}
		b = b[12+n:]
	}
	return "", false
}

// slideshowPicture is the first picture of a GNOME slideshow (a timed background's .xml).
func slideshowPicture(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	dec := xml.NewDecoder(io.LimitReader(f, 1<<20))
	dec.Strict = false
	inFile := false
	for {
		tok, err := dec.Token()
		if err != nil {
			return "", errNoThumb
		}
		switch t := tok.(type) {
		case xml.StartElement:
			inFile = t.Name.Local == "file" || t.Name.Local == "size"
		case xml.EndElement:
			inFile = false
		case xml.CharData:
			if p := strings.TrimSpace(string(t)); inFile && filepath.IsAbs(p) && filepath.Ext(p) != ".xml" {
				return p, nil
			}
		}
	}
}

// wallThumb shows a picture, cropped to fill w by h, or while it loads a skeleton, or when it can't be
// drawn a quiet tile with its name.
func (a *App) wallThumb(ctx *ui.Context, path string, w ui.Length, h, radius float32) ui.Element {
	t := ctx.Theme()
	name := filepath.Base(path)
	placeholder := func(icon *icons.Icon, label string) ui.Element {
		box := ui.Div().Column().Align(ui.Center).Justify(ui.Center).Gap(space1).Size(w, ui.Px(h)).
			Radius(radius).Background(t.SurfaceHover).Padding(space2).ClipContent().
			Child(ui.Icon(icon).Size(min(22, h/3)).Color(t.TextSubtle))
		if label != "" && h >= 60 {
			box = box.Child(ui.Text(label).FontSize(11).TextColor(t.TextSubtle).Truncate())
		}
		return box
	}
	if path == "" {
		return placeholder(icons.Image, "")
	}
	return ui.Component(func(ctx *ui.Context) ui.Element {
		res := ui.Query(ctx, "wall-thumb:"+path, func(c context.Context) (picture, error) {
			return thumbnail(path)
		}, ui.QueryStaleTime(time.Hour), ui.QueryCacheTime(5*time.Minute))
		switch {
		case res.Data.img != nil:
			return ui.Image(res.Data.img).Size(w, ui.Px(h)).Fit(ui.FitCover).Radius(radius).Alt(name)
		case res.Data.svg != nil:
			return ui.SVG(res.Data.svg).Size(w, ui.Px(h)).Fit(ui.FitCover).Radius(radius).Alt(name)
		case res.Loading:
			return ui.Skeleton().Width(w).Height(ui.Px(h))
		}
		return placeholder(icons.ImageOff, name)
	}).Key(fmt.Sprintf("wall-thumb:%s:%v:%.0f", path, w, h))
}

// wallSlot is a slot of a preset's wallpaper: the light picture or the dark one.
type wallSlot struct {
	preset string
	dark   bool
}

// presetWallpaperRow is the Wallpaper row of a preset's page: the light and dark pictures, each with
// Choose…, "Same for dark", the fit, and Leave as is.
func (a *App) presetWallpaperRow(ctx *ui.Context, p appearance.Preset) ui.Element {
	return ui.Component(func(ctx *ui.Context) ui.Element {
		t := ctx.Theme()
		part, set := p.Parts[appearance.PartWallpaper]
		sys := a.systemWallpapers(ctx).Data
		desk := a.s.Looks.Desktop
		resolve := func(ref string) (string, bool) {
			if ref == "" {
				return "", false
			}
			return a.s.Looks.WallpaperPath(ref)
		}
		split := ui.Local(ctx, "split", false) // the dark slot is open, waiting for a picture
		sameDark := part.Dark == "" && !split.Get()

		fit := part.Fit
		if fit == "" {
			fit = appearance.FitZoom
		}
		fitOpts := make([]ui.Option[string], 0, len(appearance.WallpaperFits))
		for _, f := range appearance.WallpaperFits {
			fitOpts = append(fitOpts, ui.Option[string]{Value: f, Label: appearance.ValueLabel(appearance.KeyWallpaperFit, f)})
		}
		fitSig := ui.Local(ctx, "fit", fit)
		if fitSig.Peek() != fit {
			fitSig.Set(fit)
		}
		fitSelect := ui.Select(fitSig, fitOpts...).Small().Width(ui.Px(140)).Label("Fit").Key("fit").
			Disabled(!set).OnChange(func(f string) {
			a.changeWallpaper(ctx, p.Name, func(w *appearance.Part) { w.Fit = f })
		})

		slot := func(dark bool) ui.Element {
			label, ref := "Light", part.Light
			if dark {
				label, ref = "Dark", part.Dark
			}
			path, ok := resolve(ref)
			var thumb ui.Element
			name := "None"
			switch {
			case dark && sameDark:
				lp, _ := resolve(part.Light)
				thumb = a.wallThumb(ctx, lp, ui.Px(192), 108, radiusM)
				name = "Same as light"
			case ref == "":
				thumb = a.wallThumb(ctx, "", ui.Px(192), 108, radiusM)
				if dark {
					name = "Choose a picture"
				}
			case !ok:
				thumb = ui.Div().Size(ui.Px(192), ui.Px(108)).Radius(radiusM).Background(t.SurfaceHover).Center().
					Child(ui.Icon(icons.ImageOff).Size(22).Color(t.Warning))
				name = appearance.WallpaperName(sys, ref) + " (can't be found)"
			default:
				thumb = a.wallThumb(ctx, path, ui.Px(192), 108, radiusM)
				name = appearance.WallpaperName(sys, path)
			}
			choose := ui.DropdownMenu(ui.Button("Choose…").Secondary().Small().Label("Choose the "+
				strings.ToLower(label)+" picture"), func() []ui.MenuItem {
				return []ui.MenuItem{
					{Label: "From a file…", Icon: icons.FolderOpen, OnClick: func() { a.pickWallpaperFile(ctx, p.Name, dark) }},
					{Label: "System wallpapers…", Icon: icons.Images, OnClick: func() {
						a.wallChooser.Set(&wallSlot{preset: p.Name, dark: dark})
					}},
				}
			}).Key("choose:" + label)
			return ui.Div().Column().Gap(space2).Width(ui.Px(192)).
				Child(ui.Div().Radius(radiusM).Border(1, t.Border).ClipContent().Child(thumb)).
				Child(ui.Div().Column().Gap(1).MinWidth(ui.Px(0)).
					Child(ui.Text(label).FontSize(12).Medium()).
					Child(ui.Text(name).FontSize(12).TextColor(t.TextSubtle).Truncate())).
				Child(ui.Div().Row().Child(choose))
		}
		sameSig := ui.Local(ctx, "same", sameDark)
		if sameSig.Peek() != sameDark {
			sameSig.Set(sameDark)
		}
		same := ui.Checkbox("Same for dark", sameSig).Key("same").Disabled(!set).OnChange(func(on bool) {
			if on {
				split.Set(false)
				a.changeWallpaper(ctx, p.Name, func(w *appearance.Part) { w.Dark = "" })
			} else {
				split.Set(true)
			}
		})

		left := ui.Div().Column().Gap(3).Grow(1).Basis(ui.Px(0)).MinWidth(ui.Px(0)).
			Child(ui.Text(appearance.PartLabel(appearance.PartWallpaper)).FontSize(14).Medium()).
			Child(ui.Text("The picture behind the desktop, with the light style and the dark one").FontSize(12).
				TextColor(t.TextSubtle).Truncate())
		if desk == nil || !desk.Supports(appearance.KeyWallpaper) {
			left = left.Child(caption(ctx, "This desktop can't set it from uas; it is kept in the preset.").
				TextColor(t.Warning))
		}
		top := ui.Div().Row().Align(ui.Center).Gap(space4).
			Child(left).
			Child(ui.Div().Row().Align(ui.Center).Gap(space3).Shrink(0).
				Child(filterChip(ctx, "Leave as is", !set, func() { a.setWallpaperPart(ctx, p.Name, nil) })).
				Child(fitSelect))
		slots := ui.Div().Row().Align(ui.Start).Gap(space5).Wrap().
			Child(slot(false)).
			Child(ui.Div().Column().Gap(space2).Child(slot(true)).Child(same))
		return ui.Div().Column().Gap(space4).PaddingX(space4).PaddingY(space4).Child(top).Child(slots)
	}).Key("wallpaper-row:" + p.Name)
}

// systemWallpapers are the system's wallpapers, as GNOME's settings list them.
func (a *App) systemWallpapers(ctx *ui.Context) ui.QueryResult[[]appearance.SystemWallpaper] {
	return ui.Query(ctx, "system-wallpapers", func(c context.Context) ([]appearance.SystemWallpaper, error) {
		if a.s.Looks == nil {
			return nil, nil
		}
		return a.s.Looks.SystemWallpapers(), nil
	}, ui.QueryStaleTime(time.Minute))
}

// wallpaperFilter offers the kinds of picture the store takes.
func wallpaperFilter() []dialog.Filter {
	var pats []string
	for _, e := range appearance.WallpaperExts {
		pats = append(pats, "*"+e, "*"+strings.ToUpper(e))
	}
	return []dialog.Filter{{Name: "Pictures", Patterns: pats}}
}

// pickWallpaperFile asks for a picture and puts it in a slot of a preset: copied into uas's store, unless
// it is one of the system's.
func (a *App) pickWallpaperFile(ctx *ui.Context, preset string, dark bool) {
	home, _ := os.UserHomeDir()
	folder := ""
	if home != "" {
		if fi, err := os.Stat(filepath.Join(home, "Pictures")); err == nil && fi.IsDir() {
			folder = filepath.Join(home, "Pictures")
		}
	}
	ctx.Window().OpenFiles(dialog.FileOptions{Title: "Choose a wallpaper", Folder: folder, Filters: wallpaperFilter()},
		func(r dialog.FileResult) {
			if r.Err != nil {
				ctx.Toast(ui.ToastError, errText(r.Err)).Title("Could not open the file picker")
				return
			}
			if r.Canceled || len(r.Paths) == 0 {
				return
			}
			path := r.Paths[0]
			go func() {
				ref, err := a.wallpaperRef(path)
				ctx.Post(func() {
					if err != nil {
						ctx.Toast(ui.ToastError, errText(err)).Title("Could not use " + filepath.Base(path))
						return
					}
					a.putWallpaper(ctx, preset, dark, ref, "", "")
				})
			}()
		})
}

// wallpaperRef is how a preset names a picture the user chose: a system one by its path, any other copied
// into the store.
func (a *App) wallpaperRef(path string) (string, error) {
	if !appearance.IsWallpaperFile(path) && !a.s.Looks.SystemPicture(path) {
		return "", fmt.Errorf("%s isn't a picture uas can use as a wallpaper", filepath.Base(path))
	}
	return a.s.Looks.WallpaperRef(path)
}

// putWallpaper puts a picture in a slot of a preset. A light picture with its own dark one (a system
// wallpaper's pair) sets both; fit is the fit to use when the preset had no wallpaper.
func (a *App) putWallpaper(ctx *ui.Context, preset string, dark bool, ref, pairDark, fit string) {
	a.changeWallpaper(ctx, preset, func(w *appearance.Part) {
		if dark {
			w.Dark = ref
			if w.Dark == w.Light {
				w.Dark = ""
			}
			return
		}
		if w.Light == "" && fit != "" {
			w.Fit = fit
		}
		w.Light = ref
		if pairDark != "" || w.Dark == ref {
			w.Dark = pairDark
		}
	})
}

// changeWallpaper changes a preset's wallpaper, making it when the preset had none. One without a light
// picture yet is only the dark one, so the light one takes it.
func (a *App) changeWallpaper(ctx *ui.Context, preset string, change func(*appearance.Part)) {
	if a.s.Presets == nil {
		return
	}
	_, err := a.s.Presets.Update(preset, func(p *appearance.Preset) error {
		w := p.Parts[appearance.PartWallpaper]
		change(&w)
		if w.Light == "" {
			w.Light, w.Dark = w.Dark, ""
		}
		if w.Light == "" {
			return nil // nothing chosen yet: left as it is
		}
		if w.Fit == "" {
			w.Fit = appearance.FitZoom
		}
		p.Parts[appearance.PartWallpaper] = w
		return nil
	})
	if err != nil {
		ctx.Toast(ui.ToastError, errText(err)).Title("Could not change " + preset)
	}
	refreshPresets(ctx)
}

// setWallpaperPart replaces a preset's wallpaper, or with nil leaves it out.
func (a *App) setWallpaperPart(ctx *ui.Context, preset string, w *appearance.Part) {
	if a.s.Presets == nil {
		return
	}
	_, err := a.s.Presets.Update(preset, func(p *appearance.Preset) error {
		if w == nil {
			delete(p.Parts, appearance.PartWallpaper)
		} else {
			p.Parts[appearance.PartWallpaper] = *w
		}
		return nil
	})
	if err != nil {
		ctx.Toast(ui.ToastError, errText(err)).Title("Could not change " + preset)
	}
	refreshPresets(ctx)
}

// wallChooserDialog shows the system's wallpapers, to put one in a slot of a preset.
func (a *App) wallChooserDialog(ctx *ui.Context) ui.Element {
	s := a.wallChooser.Get()
	if s == nil {
		return ui.Div().Display(false)
	}
	return ui.Component(func(ctx *ui.Context) ui.Element {
		t := ctx.Theme()
		close := func() { a.wallChooser.Set(nil) }
		res := a.systemWallpapers(ctx)
		var current string
		if pr := a.presets(ctx); !pr.Loading {
			if p, ok := pr.Data.find(s.preset); ok {
				w := p.Parts[appearance.PartWallpaper]
				current = w.Light
				if s.dark {
					current = w.Wallpaper().DarkOrLight()
				}
			}
		}
		var body ui.Element
		switch {
		case res.Loading:
			body = ui.Skeleton().Height(ui.Px(300))
		case len(res.Data) == 0:
			body = ui.Empty("No system wallpapers").Icon(icons.Images).
				Description("This system doesn't list any wallpapers. Choose a picture from a file instead.")
		default:
			grid := ui.Div().Grid(repeatFr(3)...).Gap(space3)
			for _, w := range res.Data {
				pic := w.Light
				if s.dark && w.Dark != "" {
					pic = w.Dark
				}
				chosen := current != "" && current == pic
				tile := ui.Div().Column().Gap(space2).Padding(space1).Radius(radiusM + 2).Cursor(ui.CursorPointer).
					Role(access.RoleButton).Label(w.Name).TabIndex(0).AccessSelected(chosen).
					Hover(ui.Style{Background: t.SurfaceHover}).
					OnClick(func() {
						close()
						if s.dark {
							a.putWallpaper(ctx, s.preset, true, pic, "", "")
						} else {
							a.putWallpaper(ctx, s.preset, false, w.Light, w.Dark, w.Fit)
						}
					}).
					Child(ui.Div().Radius(radiusM).Border(1, t.Border).ClipContent().
						Child(a.wallThumb(ctx, pic, ui.Full, 95, radiusM))).
					Child(ui.Div().Row().Align(ui.Center).Gap(space1).MinWidth(ui.Px(0)).
						Child(ui.Text(w.Name).FontSize(12).Truncate().Grow(1).Basis(ui.Px(0)).MinWidth(ui.Px(0))).
						Child(ui.If(w.Dark != "", ui.Icon(icons.Moon).Size(12).Color(t.TextSubtle))))
				if chosen {
					tile = tile.Border(2, t.Accent)
				}
				grid = grid.Child(tile.Key("wall:" + w.Light))
			}
			body = ui.Div().MaxHeight(ui.Px(460)).ScrollY().Child(grid)
		}
		title, msg := "System wallpapers", "Wallpapers that came with the system. One with a moon has its own "+
			"picture for the dark style, which is used too."
		if s.dark {
			title, msg = "Dark wallpaper", "The picture for the dark style. A wallpaper with its own dark "+
				"picture gives that one."
		}
		return ui.Modal(title, ui.Div().Column().Gap(space3).Child(muted(ctx, msg)).Child(body)).
			Width(ui.Px(640)).OnClose(close).Footer(ui.Button("Cancel").Ghost().OnClick(close))
	}).Key("wall-chooser")
}

// presetWallpaper is the picture a preset's card shows: its dark one for a dark preset, else its light one.
func (a *App) presetWallpaper(p appearance.Preset) (string, bool) {
	part, ok := p.Parts[appearance.PartWallpaper]
	if !ok || a.s.Looks == nil {
		return "", false
	}
	ref := part.Light
	if p.Parts[appearance.PartScheme].Value == appearance.SchemeDark {
		ref = part.Wallpaper().DarkOrLight()
	}
	return a.s.Looks.WallpaperPath(ref)
}

// savedWallpaperRow is the wallpaper's row of the Save current look dialog: its checkbox, with the
// pictures' names and a thumbnail.
func (a *App) savedWallpaperRow(ctx *ui.Context, part appearance.Part, keep *ui.Signal[bool]) ui.Element {
	t := ctx.Theme()
	sys := a.systemWallpapers(ctx).Data
	// Named as the desktop has them: a picture of the user's is in the store by its hash, which says
	// nothing to them.
	cur := a.looks(ctx).Data.current
	name := func(ref, desktop string) string {
		if desktop != "" && !strings.HasPrefix(ref, "/") {
			return appearance.WallpaperName(sys, desktop)
		}
		p, _ := a.s.Looks.WallpaperPath(ref)
		return appearance.WallpaperName(sys, p)
	}
	light, _ := a.s.Looks.WallpaperPath(part.Light)
	label := "Wallpaper: " + name(part.Light, cur[appearance.KeyWallpaper])
	if part.Dark != "" {
		label += ", dark: " + name(part.Dark, cur[appearance.KeyWallpaperDark])
	}
	return ui.Div().Row().Align(ui.Center).Gap(space3).
		Child(ui.Div().Grow(1).Basis(ui.Px(0)).MinWidth(ui.Px(0)).
			Child(ui.Checkbox(label, keep).Key("keep:" + appearance.PartWallpaper))).
		Child(ui.Div().Radius(radiusS).Border(1, t.Border).ClipContent().Shrink(0).
			Child(a.wallThumb(ctx, light, ui.Px(64), 36, radiusS)))
}

// savedWallpaperSetting is the wallpaper's row of "Changed by uas": what it is now, what it was before uas,
// and Revert, which puts back each of its settings uas changed.
func (a *App) savedWallpaperSetting(ctx *ui.Context, d looks) ui.Element {
	t := ctx.Theme()
	sys := a.systemWallpapers(ctx).Data
	describe := func(v func(key string) string) string {
		light, dark, fit := v(appearance.KeyWallpaper), v(appearance.KeyWallpaperDark), v(appearance.KeyWallpaperFit)
		s := orDefault(appearance.WallpaperName(sys, light))
		if dark != "" && dark != light {
			s += ", dark: " + appearance.WallpaperName(sys, dark)
		}
		if fit != "" {
			s += " · " + appearance.ValueLabel(appearance.KeyWallpaperFit, fit)
		}
		return s
	}
	now := describe(func(key string) string { return d.current[key] })
	was := describe(func(key string) string {
		if s, ok := d.savedFor(key); ok {
			return s.Previous
		}
		return d.current[key]
	})
	_, busy := a.running("revert:" + appearance.KeyWallpaper)
	return ui.Div().Row().Align(ui.Center).Gap(space3).MinHeight(ui.Px(56)).PaddingX(space4).PaddingY(space2).
		Child(ui.Div().Radius(radiusS).Border(1, t.Border).ClipContent().Shrink(0).
			Child(a.wallThumb(ctx, d.current[appearance.KeyWallpaper], ui.Px(64), 36, radiusS))).
		Child(ui.Div().Column().Gap(3).Grow(1).Basis(ui.Px(0)).MinWidth(ui.Px(0)).
			Child(ui.Text("Wallpaper: " + now).FontSize(14).Medium().Truncate()).
			Child(ui.Text("Before uas: " + was).FontSize(12).TextColor(t.TextSubtle).Truncate())).
		Child(ui.Button("Revert").Icon(icons.Undo2).Secondary().Small().Loading(busy).
			Tooltip("Go back to " + was).OnClick(func() { a.revertLook(ctx, appearance.KeyWallpaper) }))
}
