// Package appimagetest builds AppImage files for tests.
package appimagetest

import (
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Build makes an AppImage-shaped file: an ELF64 header with a .upd_info section holding update, followed by
// a squashfs image with a desktop entry named name. It has no AppImage magic; Mark adds it. extra are more files for the
// image: path, contents, and so on. It needs mksquashfs, and skips the test without it.
func Build(t testing.TB, name, version, update string, extra ...string) []byte {
	t.Helper()
	mksquashfs, err := exec.LookPath("mksquashfs")
	if err != nil {
		t.Skip("mksquashfs not installed")
	}
	dir := t.TempDir()
	root := filepath.Join(dir, "root")
	os.MkdirAll(root, 0o755)
	desktop := fmt.Sprintf("[Desktop Entry]\nType=Application\nName=%s\nExec=app %%F\nIcon=app\n"+
		"X-AppImage-Version=%s\n", name, version)
	os.WriteFile(filepath.Join(root, "org.example.App.desktop"), []byte(desktop), 0o644)
	os.WriteFile(filepath.Join(root, "app.svg"), []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`), 0o644)
	for i := 0; i+1 < len(extra); i += 2 { // extra files: path, contents, ...
		os.MkdirAll(filepath.Join(root, filepath.Dir(extra[i])), 0o755)
		os.WriteFile(filepath.Join(root, extra[i]), []byte(extra[i+1]), 0o644)
	}
	img := filepath.Join(dir, "img.sqfs")
	if out, err := exec.Command(mksquashfs, root, img, "-quiet", "-noappend", "-comp", "gzip").CombinedOutput(); err != nil {
		t.Fatalf("mksquashfs: %v\n%s", err, out)
	}
	sq, _ := os.ReadFile(img)

	le := binary.LittleEndian
	strtab := []byte("\x00.upd_info\x00.shstrtab\x00")
	upd := make([]byte, 512)
	copy(upd, update)
	updOff := uint64(64)
	strOff := updOff + uint64(len(upd))
	shoff := strOff + uint64(len(strtab))
	buf := make([]byte, shoff+3*64)
	copy(buf, "\x7fELF")
	buf[4], buf[5], buf[6] = 2, 1, 1
	le.PutUint16(buf[0x10:], 2)  // ET_EXEC
	le.PutUint16(buf[0x12:], 62) // x86-64
	le.PutUint32(buf[0x14:], 1)
	le.PutUint64(buf[0x28:], shoff)
	le.PutUint16(buf[0x34:], 64)
	le.PutUint16(buf[0x36:], 56)
	le.PutUint16(buf[0x3a:], 64)
	le.PutUint16(buf[0x3c:], 3)
	le.PutUint16(buf[0x3e:], 2)
	copy(buf[updOff:], upd)
	copy(buf[strOff:], strtab)
	section := func(i int, name, typ uint32, off, size uint64) {
		h := buf[shoff+uint64(i)*64:]
		le.PutUint32(h[0:], name)
		le.PutUint32(h[4:], typ)
		le.PutUint64(h[0x18:], off)
		le.PutUint64(h[0x20:], size)
		le.PutUint64(h[0x30:], 1)
	}
	section(1, 1, 1, updOff, uint64(len(upd)))     // .upd_info, PROGBITS
	section(2, 11, 3, strOff, uint64(len(strtab))) // .shstrtab, STRTAB
	return append(buf, sq...)
}

// Mark stamps the AppImage type 2 magic ("AI\x02", in the ELF header's padding) on a file from Build, as
// real AppImages have, and returns it.
func Mark(b []byte) []byte {
	copy(b[8:], "AI\x02")
	return b
}
