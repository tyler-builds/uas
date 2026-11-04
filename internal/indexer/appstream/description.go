package appstream

import (
	"encoding/xml"
	"fmt"
	"io"
	"strings"
)

// descriptionMarkdown converts the inner XML of an AppStream <description> to Markdown.
//
// AppStream descriptions allow <p>, <ul>, <ol>, <li>, <em> and <code>. Elements with an xml:lang attribute are
// translations and are skipped. Unknown elements keep their text.
func descriptionMarkdown(inner string) (string, error) {
	dec := xml.NewDecoder(strings.NewReader("<d>" + inner + "</d>"))
	var (
		blocks  []string
		text    strings.Builder
		items   []string
		ordered bool
		skip    int // depth inside a translated element
	)
	flushParagraph := func() {
		if s := collapse(text.String()); s != "" {
			blocks = append(blocks, s)
		}
		text.Reset()
	}
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("parse description: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if skip > 0 || isTranslated(t) {
				skip++
				continue
			}
			switch t.Name.Local {
			case "p", "ul", "ol":
				flushParagraph()
				if t.Name.Local != "p" {
					items, ordered = nil, t.Name.Local == "ol"
				}
			case "li":
				text.Reset()
			case "code":
				text.WriteString("`")
			}
		case xml.EndElement:
			if skip > 0 {
				skip--
				continue
			}
			switch t.Name.Local {
			case "p", "d":
				flushParagraph()
			case "li":
				if s := collapse(text.String()); s != "" {
					items = append(items, s)
				}
				text.Reset()
			case "ul", "ol":
				var b strings.Builder
				for i, it := range items {
					if i > 0 {
						b.WriteString("\n")
					}
					if ordered {
						fmt.Fprintf(&b, "%d. %s", i+1, it)
					} else {
						b.WriteString("- " + it)
					}
				}
				if b.Len() > 0 {
					blocks = append(blocks, b.String())
				}
				items = nil
			case "code":
				text.WriteString("`")
			}
		case xml.CharData:
			if skip == 0 {
				text.Write(t)
			}
		}
	}
	return strings.Join(blocks, "\n\n"), nil
}

func isTranslated(se xml.StartElement) bool {
	for _, a := range se.Attr {
		if a.Name.Local == "lang" && (a.Name.Space == xmlNamespace || a.Name.Space == "xml") && a.Value != "" {
			return true
		}
	}
	return false
}
