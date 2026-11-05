package gui

import (
	"embed"

	"github.com/tyler-builds/jamb/ui"
)

// The app's UI font is Inter (SIL Open Font License; see fonts/OFL.txt), bundled so the app looks the same
// on every desktop.
//
//go:embed fonts/*.ttf
var fontFiles embed.FS

// Font is a bundled font face: its weight and its file.
type Font struct {
	Weight ui.FontWeight
	Data   []byte
}

// Fonts returns the faces of the UI font, for the app to register as its UI family.
func Fonts() []Font {
	faces := []struct {
		file   string
		weight ui.FontWeight
	}{
		{"Inter-Regular.ttf", ui.FontRegular},
		{"Inter-Medium.ttf", ui.FontMedium},
		{"Inter-SemiBold.ttf", ui.FontSemibold},
		{"Inter-Bold.ttf", ui.FontBold},
	}
	out := make([]Font, 0, len(faces))
	for _, f := range faces {
		data, err := fontFiles.ReadFile("fonts/" + f.file)
		if err != nil {
			panic(err) // embedded; can't happen
		}
		out = append(out, Font{f.weight, data})
	}
	return out
}
