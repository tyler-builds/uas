package fedora

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strconv"

	"github.com/klauspost/compress/zstd"
)

// The parts of the RPM file format needed to read files out of a package: the lead, the signature header,
// the main header (only for the payload compressor) and the cpio archive that follows.
// See https://rpm-software-management.github.io/rpm/manual/format_v4.html.

var (
	leadMagic   = []byte{0xed, 0xab, 0xee, 0xdb}
	headerMagic = []byte{0x8e, 0xad, 0xe8, 0x01}
)

const (
	leadSize                   = 96
	tagPayloadCompressor       = 1125
	typeString                 = 6
	maxHeaderBytes             = 64 << 20
	maxExtractedFile     int64 = 512 << 20
)

// extractFiles reads an RPM from r and calls fn for each regular file in its payload whose path matches want.
// Paths are as stored in the archive, such as "./usr/share/swcatalog/xml/fedora.xml.gz".
func extractFiles(r io.Reader, want func(name string) bool, fn func(name string, data io.Reader) error) error {
	br := bufio.NewReader(r)
	lead := make([]byte, leadSize)
	if _, err := io.ReadFull(br, lead); err != nil {
		return fmt.Errorf("rpm: read lead: %w", err)
	}
	if !bytes.Equal(lead[:4], leadMagic) {
		return errors.New("rpm: not an RPM file")
	}
	// The signature header is padded to a multiple of 8 bytes; the main header is not.
	if _, err := readHeader(br, true); err != nil {
		return fmt.Errorf("rpm: signature header: %w", err)
	}
	tags, err := readHeader(br, false)
	if err != nil {
		return fmt.Errorf("rpm: header: %w", err)
	}

	var payload io.Reader
	switch c := tags[tagPayloadCompressor]; c {
	case "zstd":
		zr, err := zstd.NewReader(br, zstd.WithDecoderMaxMemory(256<<20))
		if err != nil {
			return err
		}
		defer zr.Close()
		payload = zr
	case "gzip", "":
		zr, err := gzip.NewReader(br)
		if err != nil {
			return err
		}
		defer zr.Close()
		payload = zr
	default:
		return fmt.Errorf("rpm: unsupported payload compression %q", c)
	}
	return readCPIO(payload, want, fn)
}

// readHeader reads one header structure and returns its string tags.
func readHeader(r io.Reader, pad bool) (map[int32]string, error) {
	var intro [16]byte
	if _, err := io.ReadFull(r, intro[:]); err != nil {
		return nil, err
	}
	if !bytes.Equal(intro[:4], headerMagic) {
		return nil, errors.New("bad header magic")
	}
	nindex := binary.BigEndian.Uint32(intro[8:12])
	hsize := binary.BigEndian.Uint32(intro[12:16])
	total := int64(nindex)*16 + int64(hsize)
	if total > maxHeaderBytes {
		return nil, errors.New("header too large")
	}
	buf := make([]byte, total)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, err
	}
	if pad {
		if rem := (16 + total) % 8; rem != 0 {
			if _, err := io.CopyN(io.Discard, r, 8-rem); err != nil {
				return nil, err
			}
		}
	}

	index, data := buf[:nindex*16], buf[nindex*16:]
	tags := map[int32]string{}
	for i := uint32(0); i < nindex; i++ {
		e := index[i*16 : i*16+16]
		tag := int32(binary.BigEndian.Uint32(e[0:4]))
		typ := binary.BigEndian.Uint32(e[4:8])
		off := binary.BigEndian.Uint32(e[8:12])
		if typ != typeString || off >= uint32(len(data)) {
			continue
		}
		s := data[off:]
		if end := bytes.IndexByte(s, 0); end >= 0 {
			s = s[:end]
		}
		tags[tag] = string(s)
	}
	return tags, nil
}

// readCPIO walks a cpio archive in the "newc" format that RPM uses.
func readCPIO(r io.Reader, want func(string) bool, fn func(string, io.Reader) error) error {
	var hdr [110]byte
	for {
		if _, err := io.ReadFull(r, hdr[:]); err != nil {
			return fmt.Errorf("cpio: %w", err)
		}
		magic := string(hdr[:6])
		if magic != "070701" && magic != "070702" {
			return fmt.Errorf("cpio: bad magic %q", magic)
		}
		field := func(i int) (int64, error) {
			return strconv.ParseInt(string(hdr[6+8*i:14+8*i]), 16, 64)
		}
		mode, err1 := field(1)
		size, err2 := field(6)
		nameSize, err3 := field(11)
		if err := errors.Join(err1, err2, err3); err != nil {
			return fmt.Errorf("cpio: %w", err)
		}
		if nameSize <= 0 || nameSize > 4096 || size < 0 {
			return errors.New("cpio: bad entry header")
		}
		// The name, including its NUL, is padded so header plus name is a multiple of 4.
		name := make([]byte, nameSize+pad4(110+nameSize))
		if _, err := io.ReadFull(r, name); err != nil {
			return fmt.Errorf("cpio: %w", err)
		}
		path := string(bytes.TrimRight(name[:nameSize], "\x00"))
		if path == "TRAILER!!!" {
			return nil
		}

		const typeMask, regular = 0o170000, 0o100000
		body := io.LimitReader(r, size)
		if mode&typeMask == regular && want(path) {
			if size > maxExtractedFile {
				return fmt.Errorf("cpio: %s is too large", path)
			}
			if err := fn(path, body); err != nil {
				return err
			}
		}
		// Skip whatever fn did not read, plus the padding after the data.
		if _, err := io.Copy(io.Discard, body); err != nil {
			return fmt.Errorf("cpio: %w", err)
		}
		if _, err := io.CopyN(io.Discard, r, pad4(size)); err != nil {
			return fmt.Errorf("cpio: %w", err)
		}
	}
}

func pad4(n int64) int64 { return (4 - n%4) % 4 }
