// Package appimage reads the desktop entry and icon out of an AppImage without running it.
//
// A type 2 AppImage is a small ELF runtime followed by a squashfs image of the app. The image starts where
// the ELF file ends (after its section header table), and holds the app's .desktop file and icon at its root.
package appimage

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"

	"github.com/CalebQ42/squashfs"
)

// Info is what Inspect found.
type Info struct {
	// Desktop holds the unlocalized keys of the [Desktop Entry] group, such as "Name" and "Exec".
	Desktop map[string]string
	Icon    []byte
	IconExt string // ".png" or ".svg"
}

const maxIcon = 4 << 20
const maxDesktop = 256 << 10

// Inspect reads an AppImage file. It never executes it.
func Inspect(name string) (*Info, error) {
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	off, err := squashfsOffset(f)
	if err != nil {
		return nil, err
	}
	r, err := squashfs.NewReaderAtOffset(f, off)
	if err != nil {
		return nil, fmt.Errorf("appimage: %w", err)
	}
	return fromFS(sqfs{r.FS})
}

// squashfsOffset returns where the squashfs image starts: the end of the ELF section header table.
func squashfsOffset(r io.ReaderAt) (int64, error) {
	var h [64]byte
	if _, err := r.ReadAt(h[:], 0); err != nil {
		return 0, fmt.Errorf("appimage: read ELF header: %w", err)
	}
	if !bytes.Equal(h[:4], []byte("\x7fELF")) {
		return 0, errors.New("appimage: not an ELF file")
	}
	if h[4] != 2 { // ELFCLASS64
		return 0, errors.New("appimage: only 64-bit AppImages are supported")
	}
	var bo binary.ByteOrder = binary.LittleEndian
	if h[5] == 2 {
		bo = binary.BigEndian
	}
	shoff := bo.Uint64(h[0x28:])
	shentsize := uint64(bo.Uint16(h[0x3a:]))
	shnum := uint64(bo.Uint16(h[0x3c:]))
	end := shoff + shentsize*shnum
	if end == 0 || end > 1<<32 {
		return 0, errors.New("appimage: bad ELF section header table")
	}
	var magic [4]byte
	if _, err := r.ReadAt(magic[:], int64(end)); err != nil || string(magic[:]) != "hsqs" {
		return 0, errors.New("appimage: no squashfs image after the ELF runtime (type 1 AppImages are not supported)")
	}
	return int64(end), nil
}

// linkFS is a filesystem that can say where a symlink points.
type linkFS interface {
	fs.FS
	ReadLink(name string) (string, error)
}

var errNotLink = errors.New("not a symlink")

// sqfs adds ReadLink to the squashfs filesystem.
type sqfs struct{ squashfs.FS }

func (s sqfs) ReadLink(name string) (string, error) {
	f, err := s.FS.OpenFile(name)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if !f.IsSymlink() {
		return "", errNotLink
	}
	return f.SymlinkPath(), nil
}

// fromFS finds the desktop entry and icon at the root of an AppImage's filesystem.
func fromFS(fsys fs.FS) (*Info, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, fmt.Errorf("appimage: %w", err)
	}
	info := &Info{}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".desktop") {
			b, err := readFile(fsys, e.Name(), maxDesktop)
			if err != nil {
				return nil, fmt.Errorf("appimage: %s: %w", e.Name(), err)
			}
			info.Desktop = parseDesktop(string(b))
			break
		}
	}
	if info.Desktop == nil {
		return nil, errors.New("appimage: no .desktop file at the image root")
	}

	var candidates []string
	if icon := info.Desktop["Icon"]; icon != "" && !strings.ContainsAny(icon, "/\\") {
		candidates = append(candidates, icon+".svg", icon+".png", icon)
	}
	candidates = append(candidates, ".DirIcon")
	for _, c := range candidates {
		b, err := readFile(fsys, c, maxIcon)
		if err != nil {
			continue
		}
		if ext := iconExt(b); ext != "" {
			info.Icon, info.IconExt = b, ext
			break
		}
	}
	return info, nil
}

// readFile reads a file, following up to five relative symlinks that stay inside the image.
func readFile(fsys fs.FS, name string, limit int64) ([]byte, error) {
	for hop := 0; hop < 5; hop++ {
		if lfs, ok := fsys.(linkFS); ok {
			target, err := lfs.ReadLink(name)
			if err == nil {
				if path.IsAbs(target) {
					return nil, fmt.Errorf("symlink %s points outside the image", name)
				}
				name = path.Clean(path.Join(path.Dir(name), target))
				if !fs.ValidPath(name) {
					return nil, fmt.Errorf("symlink %s points outside the image", name)
				}
				continue
			}
			// Any error means "not a symlink" here; opening the file reports real problems.
		}
		f, err := fsys.Open(name)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		b, err := io.ReadAll(io.LimitReader(f, limit+1))
		if err != nil {
			return nil, err
		}
		if int64(len(b)) > limit {
			return nil, fmt.Errorf("%s is too large", name)
		}
		return b, nil
	}
	return nil, fmt.Errorf("too many symlinks at %s", name)
}

func iconExt(b []byte) string {
	switch {
	case bytes.HasPrefix(b, []byte("\x89PNG\r\n\x1a\n")):
		return ".png"
	case bytes.Contains(b[:min(len(b), 512)], []byte("<svg")):
		return ".svg"
	}
	return ""
}

// parseDesktop reads the unlocalized keys of the [Desktop Entry] group.
func parseDesktop(s string) map[string]string {
	vals := map[string]string{}
	inEntry := false
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			inEntry = line == "[Desktop Entry]"
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		k = strings.TrimSpace(k)
		if !inEntry || !ok || strings.Contains(k, "[") {
			continue
		}
		if _, dup := vals[k]; !dup {
			vals[k] = strings.TrimSpace(v)
		}
	}
	return vals
}
