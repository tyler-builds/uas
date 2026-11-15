package appearance

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"

	"github.com/tyler-builds/uas/internal/catalog"
)

// rawDir is where Extract writes the archive's files before it puts them into the themes' folders.
const rawDir = ".raw"

// Extract writes the archive's themes of kind into dir, each as dir/<Name>, and returns them. dir must be a
// new, empty folder; everything is written through an os.Root on it, so nothing can land outside it. Every
// theme of the kind in the archive is written, since a variant usually inherits from the others in its
// archive (Papirus-Dark from Papirus). A link in a theme becomes a hard link to the file it leads to, or a
// copy where hard links can't be made, never a symbolic link. Files are written without execute
// permission, and a theme's icon cache, which may not match what was written, is left out.
func Extract(p string, kind catalog.Kind, dir string, lim Limits) ([]Theme, error) {
	c, err := Inspect(p, lim)
	if err != nil {
		return nil, err
	}
	themes := c.Of(kind)
	if len(themes) == 0 {
		return nil, refuse("it holds no %s", kindName(kind))
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	if err := emptyDir(root); err != nil {
		return nil, err
	}

	// The files the themes need: their own, and those their links lead to.
	need := map[string]bool{}
	for _, t := range themes {
		for _, e := range c.within(t.Path) {
			if err := c.needs(e.name, need, 0); err != nil {
				return nil, err
			}
		}
	}
	if err := c.writeRaw(p, root, need, lim); err != nil {
		return nil, err
	}
	m := &materializer{c: c, root: root, max: lim.MaxEntries * 2}
	for _, t := range themes {
		if err := root.Mkdir(t.Name, 0o755); err != nil {
			return nil, err
		}
		for _, e := range c.within(t.Path) {
			if err := m.put(t.Name+strings.TrimPrefix(e.name, t.Path), e.name, 0); err != nil {
				return nil, err
			}
		}
	}
	if err := root.RemoveAll(rawDir); err != nil {
		return nil, err
	}
	return themes, nil
}

func kindName(k catalog.Kind) string {
	switch k {
	case catalog.KindIconTheme:
		return "icon theme"
	case catalog.KindCursorTheme:
		return "cursor theme"
	case catalog.KindFont:
		return "font"
	}
	return string(k)
}

// emptyDir checks that the folder the root opens is empty.
func emptyDir(root *os.Root) error {
	f, err := root.Open(".")
	if err != nil {
		return err
	}
	defer f.Close()
	names, err := f.Readdirnames(1)
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if len(names) > 0 {
		return fmt.Errorf("extract: %s is not empty", root.Name())
	}
	return nil
}

// needs marks the files an entry needs written: itself, or what it leads to.
func (c *Contents) needs(name string, need map[string]bool, depth int) error {
	if depth > 40 {
		return refuse("%q leads round in a circle", name)
	}
	target, err := c.resolve(name, 0)
	if err != nil {
		return err
	}
	if e, ok := c.index[target]; ok && e.typ == typeFile {
		need[target] = true
		return nil
	}
	// A folder: everything in it.
	for _, e := range c.within(target) {
		switch e.typ {
		case typeFile:
			need[e.name] = true
		case typeSymlink, typeHardlink:
			if err := c.needs(e.name, need, depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}

// writeRaw writes the needed files under rawDir, by their archive paths.
func (c *Contents) writeRaw(p string, root *os.Root, need map[string]bool, lim Limits) error {
	var total int64
	return walk(p, func(e entry, r io.Reader) error {
		if e.typ != typeFile || !need[e.name] {
			return nil
		}
		if want, ok := c.index[e.name]; !ok || want.typ != typeFile || want.size != e.size {
			return fmt.Errorf("extract: the archive changed while it was read")
		}
		name := rawDir + "/" + e.name
		if err := root.MkdirAll(path.Dir(name), 0o755); err != nil {
			return err
		}
		f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			return err
		}
		n, err := io.Copy(f, io.LimitReader(r, e.size+1))
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return refuse("the archive is damaged: %v", err)
		}
		if n != e.size {
			return refuse("the archive is damaged: %s is not the size it says", e.name)
		}
		if total += n; total > lim.MaxBytes {
			return refuse("it unpacks to more than %s", size(lim.MaxBytes))
		}
		return nil
	})
}

// materializer writes a theme's entries from the raw files.
type materializer struct {
	c     *Contents
	root  *os.Root
	count int
	max   int
}

// put writes the entry src (an archive path) at dst (relative to the root): a folder, a hard link to its raw
// file, or for a link, what the link leads to.
func (m *materializer) put(dst, src string, depth int) error {
	if m.count++; m.count > m.max {
		return refuse("its links make more than %d files", m.max)
	}
	if depth > 40 {
		return refuse("%q leads round in a circle", src)
	}
	if path.Base(dst) == "icon-theme.cache" {
		return nil
	}
	target, err := m.c.resolve(src, 0)
	if err != nil {
		return err
	}
	e, isEntry := m.c.index[target]
	if isEntry && e.typ == typeFile {
		if err := m.root.MkdirAll(path.Dir(dst), 0o755); err != nil {
			return err
		}
		return m.link(rawDir+"/"+target, dst)
	}
	// A folder, named in the archive or implied by what is in it.
	if strings.HasPrefix(src+"/", target+"/") && src != target {
		return refuse("%q is a link to a folder it is in", src)
	}
	if err := m.root.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	if target == src {
		return nil // the folder itself; its entries are put on their own
	}
	for _, child := range m.c.within(target) {
		if err := m.put(dst+strings.TrimPrefix(child.name, target), child.name, depth+1); err != nil {
			return err
		}
	}
	return nil
}

// link makes dst a hard link to src, or a copy of it where hard links can't be made. A file that is there
// already is kept: a folder link can lead to a file the theme also has.
func (m *materializer) link(src, dst string) error {
	if _, err := m.root.Lstat(dst); err == nil {
		return nil
	}
	if err := m.root.Link(src, dst); err == nil {
		return nil
	} else if errors.Is(err, fs.ErrExist) {
		return nil
	}
	in, err := m.root.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := m.root.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	return err
}
