package gui

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"math"

	"github.com/go-text/typesetting/di"
	gtfont "github.com/go-text/typesetting/font"
	ot "github.com/go-text/typesetting/font/opentype"
	"github.com/go-text/typesetting/language"
	"github.com/go-text/typesetting/shaping"
	fx "golang.org/x/image/math/fixed"
	"golang.org/x/image/vector"

	"github.com/tyler-builds/jamb/ui"
)

// Font previews. Jamb draws text only in the fonts the app registers when it starts and the desktop's own
// families, so a font that isn't installed (or was installed after uas started) can't be set on a Text.
// Instead uas draws the sample itself, from the font file, into an image: go-text shapes it (as Jamb does,
// with kerning and ligatures) and golang.org/x/image/vector fills the glyphs' outlines. A variable font is
// drawn at its regular weight.

// errNoGlyphs is returned when a font lacks letters of the sample, as a font for another script does.
var errNoGlyphs = errors.New("the font has no letters for this sample")

// previewScale is how many image pixels are drawn per point of the sample, so it stays sharp on a HiDPI
// screen; the image is shown at 1/previewScale of its size.
const previewScale = 2

// sample is a rendered font sample and its size in points.
type sample struct {
	img  *ui.ImageSource
	w, h float32
}

// parseFace reads a font file, or the first font of a collection, at its regular weight.
func parseFace(data []byte) (*gtfont.Face, error) {
	var face *gtfont.Face
	if bytes.HasPrefix(data, []byte("ttcf")) {
		faces, err := gtfont.ParseTTC(bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		if len(faces) == 0 {
			return nil, errors.New("an empty font collection")
		}
		face = faces[0]
	} else {
		var err error
		if face, err = gtfont.ParseTTF(bytes.NewReader(data)); err != nil {
			return nil, err
		}
	}
	face.SetVariations([]gtfont.Variation{{Tag: ot.MustNewTag("wght"), Value: 400}})
	return face, nil
}

func shape(face *gtfont.Face, text []rune, px float64) shaping.Output {
	return (&shaping.HarfbuzzShaper{}).Shape(shaping.Input{Text: text, RunEnd: len(text), Direction: di.DirectionLTR,
		Face: face, Size: fx.Int26_6(math.Round(px * 64)), Script: language.Latin,
		Language: language.NewLanguage("en")})
}

// renderSample draws text in the font at size points, in color c, no wider than maxW points: a line that is
// too long is drawn smaller, down to two thirds of size, and then cut.
func renderSample(data []byte, text string, size float64, c ui.Color, maxW float32) (s sample, err error) {
	defer func() {
		// A damaged font must not take the app down: the font comes from the network, even if its checksum
		// is pinned.
		if r := recover(); r != nil {
			err = errors.New("the font could not be read")
		}
	}()
	face, err := parseFace(data)
	if err != nil {
		return sample{}, err
	}
	runes := []rune(text)
	for _, r := range runes {
		if r == ' ' {
			continue
		}
		if _, ok := face.NominalGlyph(r); !ok {
			return sample{}, errNoGlyphs
		}
	}
	px := size * previewScale
	limit := float64(maxW) * previewScale
	out := shape(face, runes, px)
	if w := float64(out.Advance) / 64; w+4 > limit {
		px = max(px*(limit-4)/w, px*2/3)
		out = shape(face, runes, px)
	}
	ascent, descent := float64(out.LineBounds.Ascent)/64, float64(out.LineBounds.Descent)/64
	width := min(int(math.Ceil(float64(out.Advance)/64))+4, int(limit))
	height := int(math.Ceil(ascent-descent)) + 4
	if width <= 4 || height <= 4 || width > 8000 || height > 2000 {
		return sample{}, errors.New("the sample has an odd size")
	}
	upem := float64(face.Upem())
	if upem == 0 {
		return sample{}, errors.New("the font has no units per em")
	}
	scale := px / upem
	r := vector.NewRasterizer(width, height)
	x, baseline := 2.0, 2+ascent
	for _, g := range out.Glyphs {
		if outline, ok := face.GlyphData(g.GlyphID).(gtfont.GlyphOutline); ok {
			ox, oy := x+float64(g.XOffset)/64, baseline-float64(g.YOffset)/64
			pt := func(p ot.SegmentPoint) (float32, float32) {
				return float32(ox + float64(p.X)*scale), float32(oy - float64(p.Y)*scale)
			}
			for _, seg := range outline.Segments {
				switch seg.Op {
				case ot.SegmentOpMoveTo:
					r.MoveTo(pt(seg.Args[0]))
				case ot.SegmentOpLineTo:
					r.LineTo(pt(seg.Args[0]))
				case ot.SegmentOpQuadTo:
					bx, by := pt(seg.Args[0])
					cx, cy := pt(seg.Args[1])
					r.QuadTo(bx, by, cx, cy)
				case ot.SegmentOpCubeTo:
					bx, by := pt(seg.Args[0])
					cx, cy := pt(seg.Args[1])
					dx, dy := pt(seg.Args[2])
					r.CubeTo(bx, by, cx, cy, dx, dy)
				}
			}
			r.ClosePath()
		}
		x += float64(g.Advance) / 64
	}
	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	ink := color.NRGBA{R: to8(c.R), G: to8(c.G), B: to8(c.B), A: to8(c.A)}
	r.Draw(img, img.Bounds(), image.NewUniform(ink), image.Point{})
	return sample{img: ui.ImageFromImage(img), w: float32(width) / previewScale,
		h: float32(height) / previewScale}, nil
}

func to8(v float32) uint8 { return uint8(math.Round(float64(min(max(v, 0), 1)) * 255)) }
