// Package appearance reads the archives that fonts and icon and cursor themes come in, safely: it checks
// every entry before anything is written, finds the themes inside, and writes only those, into a folder
// the caller made for it. The indexer uses it to keep archives that can't be installed out of the catalog,
// and the client to install them.
//
// What it refuses:
//   - entries with absolute paths or paths that leave the archive ("../x"), and device files and pipes;
//   - links (symbolic or hard) that point outside the archive, or at nothing in it. Themes use links a lot
//     (a cursor theme names one cursor many ways), so a link to a file inside the archive is kept, but
//     written as a hard link between the files this package wrote, never as a symbolic link;
//   - programs and scripts anywhere in the archive: ELF files, files starting with "#!", and files named like
//     scripts, libraries, launchers, QML or JavaScript (see forbidden). Nothing in an archive is ever run;
//   - archives with more files or bytes than Limits allow;
//   - archives whose themes can't be told apart cleanly (see Inspect).
package appearance

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/bzip2"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/klauspost/compress/zstd"
	"github.com/ulikunitz/xz"
)

// RefusedError is an archive this package won't install, with why, in words for people.
type RefusedError struct{ Reason string }

func (e *RefusedError) Error() string { return e.Reason }

func refuse(format string, args ...any) error {
	return &RefusedError{Reason: fmt.Sprintf(format, args...)}
}

// IsRefused reports whether err is an archive refused for what is in it, as opposed to one that couldn't be
// read.
func IsRefused(err error) bool {
	var r *RefusedError
	return errors.As(err, &r)
}

// Limits cap what an archive may hold.
type Limits struct {
	MaxEntries   int   // files, folders and links
	MaxBytes     int64 // the files' total size, unpacked
	MaxFileBytes int64 // one file's size
}

// DefaultLimits fit the largest icon themes (Papirus unpacks to about 60,000 entries) with room to spare.
var DefaultLimits = Limits{MaxEntries: 200_000, MaxBytes: 2 << 30, MaxFileBytes: 64 << 20}

type entryType int

const (
	typeFile entryType = iota
	typeDir
	typeSymlink
	typeHardlink
)

// entry is one archive member.
type entry struct {
	name string // cleaned, relative, with forward slashes, no trailing slash
	typ  entryType
	link string // a link's target as the archive gives it
	size int64
}

// walk calls fn for each entry of the archive at p, in archive order. For a file, r reads its contents; fn
// need not read them all.
func walk(p string, fn func(e entry, r io.Reader) error) error {
	f, err := os.Open(p)
	if err != nil {
		return err
	}
	defer f.Close()
	br := bufio.NewReaderSize(f, 64<<10)
	magic, _ := br.Peek(262)
	var tr io.Reader
	switch {
	case bytes.HasPrefix(magic, []byte("PK\x03\x04")) || bytes.HasPrefix(magic, []byte("PK\x05\x06")):
		return walkZip(f, fn)
	case bytes.HasPrefix(magic, []byte{0x1f, 0x8b}):
		zr, err := gzip.NewReader(br)
		if err != nil {
			return refuse("the archive is damaged: %v", err)
		}
		defer zr.Close()
		tr = zr
	case bytes.HasPrefix(magic, []byte{0xfd, '7', 'z', 'X', 'Z', 0}):
		zr, err := xz.NewReader(br)
		if err != nil {
			return refuse("the archive is damaged: %v", err)
		}
		tr = zr
	case bytes.HasPrefix(magic, []byte{0x28, 0xb5, 0x2f, 0xfd}):
		zr, err := zstd.NewReader(br, zstd.WithDecoderMaxMemory(256<<20))
		if err != nil {
			return refuse("the archive is damaged: %v", err)
		}
		defer zr.Close()
		tr = zr
	case bytes.HasPrefix(magic, []byte("BZh")):
		tr = bzip2.NewReader(br)
	case len(magic) >= 262 && bytes.Equal(magic[257:262], []byte("ustar")):
		tr = br
	case bytes.HasPrefix(magic, []byte{'7', 'z', 0xbc, 0xaf}):
		return refuse("it is a 7-Zip archive, which uas doesn't open; it opens .tar.gz, .tar.xz, .tar.bz2, .tar.zst and .zip")
	case bytes.HasPrefix(magic, []byte("Rar!")):
		return refuse("it is a RAR archive, which uas doesn't open; it opens .tar.gz, .tar.xz, .tar.bz2, .tar.zst and .zip")
	default:
		return refuse("it isn't an archive uas can open (.tar.gz, .tar.xz, .tar.bz2, .tar.zst or .zip)")
	}
	return walkTar(tar.NewReader(tr), fn)
}

func walkTar(tr *tar.Reader, fn func(entry, io.Reader) error) error {
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return refuse("the archive is damaged: %v", err)
		}
		e := entry{size: h.Size, link: h.Linkname}
		switch h.Typeflag {
		case tar.TypeReg, tar.TypeRegA:
			e.typ = typeFile
		case tar.TypeDir:
			e.typ = typeDir
		case tar.TypeSymlink:
			e.typ = typeSymlink
		case tar.TypeLink:
			e.typ = typeHardlink
		case tar.TypeXGlobalHeader:
			continue // metadata, as git archive writes
		default:
			return refuse("it contains %q, which is a device, a pipe or another special file", h.Name)
		}
		if e.name, err = cleanName(h.Name, e.typ == typeDir); err != nil {
			return err
		}
		if e.name == "" {
			continue // "./"
		}
		if err := fn(e, tr); err != nil {
			return err
		}
	}
}

func walkZip(f *os.File, fn func(entry, io.Reader) error) error {
	st, err := f.Stat()
	if err != nil {
		return err
	}
	zr, err := zip.NewReader(f, st.Size())
	if err != nil {
		return refuse("the archive is damaged: %v", err)
	}
	for _, zf := range zr.File {
		mode := zf.Mode()
		e := entry{size: int64(zf.UncompressedSize64)}
		isDir := strings.HasSuffix(zf.Name, "/") || mode.IsDir()
		switch {
		case isDir:
			e.typ = typeDir
		case mode&os.ModeSymlink != 0:
			e.typ = typeSymlink
		case mode.IsRegular():
			e.typ = typeFile
		default:
			return refuse("it contains %q, which is a device, a pipe or another special file", zf.Name)
		}
		if e.name, err = cleanName(zf.Name, isDir); err != nil {
			return err
		}
		if e.name == "" {
			continue
		}
		var r io.Reader
		if e.typ != typeDir {
			rc, err := zf.Open()
			if err != nil {
				return refuse("the archive is damaged: %v", err)
			}
			if e.typ == typeSymlink {
				target, err := io.ReadAll(io.LimitReader(rc, 4096))
				rc.Close()
				if err != nil {
					return refuse("the archive is damaged: %v", err)
				}
				e.link = string(target)
			} else {
				r = rc
				defer rc.Close()
			}
		}
		if err := fn(e, r); err != nil {
			return err
		}
	}
	return nil
}

// cleanName checks an entry's name and returns it relative, cleaned and without a trailing slash.
func cleanName(name string, dir bool) (string, error) {
	n := strings.TrimPrefix(name, "./")
	if dir {
		n = strings.TrimSuffix(n, "/")
	}
	if n == "" || n == "." {
		return "", nil
	}
	switch {
	case strings.ContainsRune(n, 0) || strings.ContainsRune(n, '\\'):
		return "", refuse("it contains an entry with an odd name, %q", name)
	case strings.HasPrefix(n, "/"):
		return "", refuse("it contains %q, an absolute path", name)
	}
	c := path.Clean(n)
	if !filepath.IsLocal(c) {
		return "", refuse("it contains %q, which points outside the archive", name)
	}
	return c, nil
}

// forbiddenExt are file extensions of programs, scripts, libraries, launchers and plugins. A theme or a font
// needs none of them, and refusing them keeps anything runnable off the disk.
var forbiddenExt = map[string]bool{
	".sh": true, ".bash": true, ".zsh": true, ".fish": true, ".csh": true, ".ksh": true, ".py": true,
	".pyc": true, ".pl": true, ".pm": true, ".rb": true, ".php": true, ".lua": true, ".tcl": true,
	".so": true, ".a": true, ".o": true, ".ko": true, ".desktop": true, ".qml": true, ".qmlc": true,
	".js": true, ".mjs": true, ".cjs": true, ".jsc": true, ".exe": true, ".dll": true, ".bat": true,
	".cmd": true, ".ps1": true, ".vbs": true, ".msi": true, ".run": true, ".bin": true, ".appimage": true,
	".jar": true, ".deb": true, ".rpm": true, ".flatpak": true, ".snap": true, ".service": true,
	".plasmoid": true, ".kwinscript": true, ".plugin": true, ".elf": true, ".out": true, ".app": true,
	".command": true, ".tar": true, ".gz": true, ".xz": true, ".bz2": true, ".zst": true, ".zip": true,
	".7z": true, ".rar": true, ".tgz": true,
}

// forbiddenNames are build and install scripts that go by name.
var forbiddenNames = map[string]bool{
	"makefile": true, "gnumakefile": true, "cmakelists.txt": true, "meson.build": true, "pkgbuild": true,
	"configure": true,
}

// forbidden says why a file can't be in an archive, from its name and first bytes, or "".
func forbidden(name string, head []byte) string {
	base := strings.ToLower(path.Base(name))
	ext := path.Ext(base)
	switch {
	case forbiddenExt[ext]:
		if isArchiveExt(ext) {
			return "another archive inside it (" + name + "), which uas doesn't open"
		}
		return "a program or script (" + name + ")"
	case strings.Contains(base, ".so."):
		return "a library (" + name + ")"
	case forbiddenNames[base]:
		return "a build or install script (" + name + ")"
	case bytes.HasPrefix(head, []byte("\x7fELF")):
		return "a program (" + name + ")"
	case bytes.HasPrefix(head, []byte("#!")):
		return "a script (" + name + ")"
	}
	return ""
}

func isArchiveExt(ext string) bool {
	switch ext {
	case ".tar", ".gz", ".xz", ".bz2", ".zst", ".zip", ".7z", ".rar", ".tgz":
		return true
	}
	return false
}

// size names a byte count for a message: "64 MB", or "100 bytes" for the small limits of tests.
func size(n int64) string {
	if n >= 1<<20 {
		return fmt.Sprintf("%d MB", n>>20)
	}
	return fmt.Sprintf("%d bytes", n)
}
