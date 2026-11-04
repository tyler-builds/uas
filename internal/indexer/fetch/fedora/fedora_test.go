package fedora

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"

	"github.com/tyler-builds/uas/internal/catalog"
)

// cpioEntry writes one newc entry.
func cpioEntry(w *bytes.Buffer, name string, mode int64, data []byte) {
	nameSize := int64(len(name) + 1)
	fmt.Fprintf(w, "070701%08x%08x%08x%08x%08x%08x%08x%08x%08x%08x%08x%08x%08x",
		1, mode, 0, 0, 1, 0, len(data), 0, 0, 0, 0, nameSize, 0)
	w.WriteString(name + "\x00")
	w.Write(make([]byte, pad4(110+nameSize)))
	w.Write(data)
	w.Write(make([]byte, pad4(int64(len(data)))))
}

// header encodes an RPM header with string tags.
func header(tags map[int32]string) []byte {
	var index, data bytes.Buffer
	for tag, val := range tags {
		binary.Write(&index, binary.BigEndian, []int32{tag, typeString, int32(data.Len()), 1})
		data.WriteString(val + "\x00")
	}
	var h bytes.Buffer
	h.Write(headerMagic)
	h.Write(make([]byte, 4))
	binary.Write(&h, binary.BigEndian, []uint32{uint32(len(tags)), uint32(data.Len())})
	h.Write(index.Bytes())
	h.Write(data.Bytes())
	return h.Bytes()
}

// buildRPM makes a minimal RPM whose zstd payload holds the given files.
func buildRPM(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var archive bytes.Buffer
	cpioEntry(&archive, "./usr/share/swcatalog", 0o040755, nil)
	for name, data := range files {
		cpioEntry(&archive, name, 0o100644, data)
	}
	cpioEntry(&archive, "TRAILER!!!", 0, nil)

	var rpm bytes.Buffer
	lead := make([]byte, leadSize)
	copy(lead, leadMagic)
	rpm.Write(lead)
	sig := header(map[int32]string{1000: "abc"}) // 16 + 16 + 4 bytes: needs padding to 8
	rpm.Write(sig)
	rpm.Write(make([]byte, (8-len(sig)%8)%8))
	rpm.Write(header(map[int32]string{tagPayloadCompressor: "zstd"}))
	zw, err := zstd.NewWriter(&rpm)
	if err != nil {
		t.Fatal(err)
	}
	zw.Write(archive.Bytes())
	zw.Close()
	return rpm.Bytes()
}

func gz(s string) []byte {
	var b bytes.Buffer
	zw := gzip.NewWriter(&b)
	zw.Write([]byte(s))
	zw.Close()
	return b.Bytes()
}

const appstreamXML = `<?xml version="1.0"?>
<components origin="fedora">
  <component type="desktop-application">
    <id>org.example.Editor</id>
    <pkgname>example-editor</pkgname>
    <name>Example Editor</name>
    <summary>Edit text</summary>
    <url type="homepage">https://example.org/editor</url>
  </component>
  <component type="desktop">
    <id>oldtool.desktop</id>
    <pkgname>oldtool</pkgname>
    <name>Old Tool</name>
  </component>
  <component type="desktop-application">
    <id>org.example.Gone</id>
    <pkgname>gone</pkgname>
    <name>Not in the repos</name>
  </component>
  <component type="addon">
    <id>org.example.Editor.Plugin</id>
    <pkgname>example-editor-plugin</pkgname>
    <name>Plugin</name>
  </component>
</components>`

func TestExtractFiles(t *testing.T) {
	rpm := buildRPM(t, map[string][]byte{
		"./usr/share/swcatalog/xml/fedora.xml.gz": []byte("catalog"),
		"./usr/share/doc/README":                  []byte("skip me"),
	})
	var got []string
	err := extractFiles(bytes.NewReader(rpm), isCatalogXML, func(name string, r io.Reader) error {
		b, _ := io.ReadAll(r)
		got = append(got, name+"="+string(b))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "./usr/share/swcatalog/xml/fedora.xml.gz=catalog" {
		t.Errorf("got %q", got)
	}

	if err := extractFiles(strings.NewReader(strings.Repeat("x", 200)), isCatalogXML, nil); err == nil {
		t.Error("accepted a non-RPM file")
	}
}

func primaryXML(pkgs ...string) []byte {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0"?><metadata xmlns="http://linux.duke.edu/metadata/common" xmlns:rpm="http://linux.duke.edu/metadata/rpm">`)
	for _, p := range pkgs {
		b.WriteString(p)
	}
	b.WriteString(`</metadata>`)
	return gz(b.String())
}

func pkgXML(name, arch, ver, rel, href string) string {
	return fmt.Sprintf(`<package type="rpm"><name>%s</name><arch>%s</arch>
<version epoch="0" ver="%s" rel="%s"/><time file="1" build="1760000000"/><location href="%s"/>
<format><rpm:license>MIT</rpm:license></format></package>`, name, arch, ver, rel, href)
}

func repomd(primary string) []byte {
	return []byte(`<?xml version="1.0"?><repomd xmlns="http://linux.duke.edu/metadata/repo">
<data type="filelists"><location href="repodata/x-filelists.xml.gz"/></data>
<data type="primary"><location href="` + primary + `"/></data></repomd>`)
}

func TestFetch(t *testing.T) {
	rpm := buildRPM(t, map[string][]byte{"./usr/share/swcatalog/xml/fedora.xml.gz": gz(appstreamXML)})
	files := map[string][]byte{
		"/releases/45/Everything/x86_64/os/repodata/repomd.xml": repomd("repodata/abc-primary.xml.gz"),
		"/releases/45/Everything/x86_64/os/repodata/abc-primary.xml.gz": primaryXML(
			pkgXML("example-editor", "x86_64", "1.0", "1.fc45", "Packages/e/example-editor-1.0-1.fc45.x86_64.rpm"),
			pkgXML("example-editor", "i686", "1.0", "1.fc45", "Packages/e/example-editor-1.0-1.fc45.i686.rpm"),
			pkgXML("oldtool", "noarch", "0.1", "1.fc45", "Packages/o/oldtool-0.1-1.fc45.noarch.rpm"),
			pkgXML("example-editor-plugin", "x86_64", "1.0", "1.fc45", "Packages/e/plugin.rpm"),
			pkgXML("appstream-data", "noarch", "45", "1.fc45", "Packages/a/appstream-data-45-1.fc45.noarch.rpm"),
		),
		"/releases/45/Everything/x86_64/os/Packages/a/appstream-data-45-1.fc45.noarch.rpm": rpm,
		"/updates/45/Everything/x86_64/repodata/repomd.xml":                                repomd("repodata/def-primary.xml.gz"),
		"/updates/45/Everything/x86_64/repodata/def-primary.xml.gz": primaryXML(
			pkgXML("example-editor", "x86_64", "1.1", "1.fc45", "Packages/e/example-editor-1.1-1.fc45.x86_64.rpm"),
		),
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, ok := files[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(b)
	}))
	defer srv.Close()

	f := &Fetcher{Client: srv.Client(), CacheDir: t.TempDir(), Arch: "x86_64", Mirror: srv.URL}
	ls, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(ls) != 2 {
		t.Fatalf("got %d listings, want 2: %+v", len(ls), ls)
	}
	ed := ls[0]
	if ed.Ref != "fedora-45/example-editor" || ed.Package != "example-editor" || ed.Distro != "fedora-45" ||
		ed.Version != "1.1-1.fc45" || ed.Source != catalog.SourceFedora || ed.Homepage != "https://example.org/editor" {
		t.Errorf("editor = %+v", ed)
	}
	if ls[1].AppStreamID != "oldtool.desktop" || ls[1].Kind != catalog.KindDesktopApp {
		t.Errorf("old tool = %+v", ls[1])
	}
}

func TestRepoRelative(t *testing.T) {
	for _, ok := range []string{"Packages/d/DNS-Compliance-Testing-0^20230708git4aea40b-10.fc44.x86_64.rpm",
		"repodata/abc-primary.xml.zst"} {
		if _, err := repoRelative(ok); err != nil {
			t.Errorf("repoRelative(%q) = %v", ok, err)
		}
	}
	for _, bad := range []string{"../../etc/passwd", "/abs/path", "https://evil.example/x.rpm", "a/../../b"} {
		if _, err := repoRelative(bad); err == nil {
			t.Errorf("repoRelative(%q) accepted", bad)
		}
	}
}
