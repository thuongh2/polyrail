package kb

import (
	"bytes"
	"encoding/json"
	"fmt"
	"mime"
	"path"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"

	"github.com/polyrail/polyrail/internal/store"
)

// MaxTextBytes caps the extracted text of one document.
const MaxTextBytes = 2 << 20

// Supported media types; anything else is ErrUnsupported.
var byExt = map[string]string{
	".txt": "text/plain", ".text": "text/plain", ".md": "text/markdown", ".markdown": "text/markdown",
	".csv": "text/csv", ".json": "application/json", ".html": "text/html", ".htm": "text/html",
}

// mediaType resolves a doc's media type from its declared content type, else its extension.
// Generic declared types (application/octet-stream) fall back to the extension.
func mediaType(contentType, filename string) (string, error) {
	mt, _, _ := mime.ParseMediaType(contentType)
	if mt == "" || mt == "application/octet-stream" {
		mt = byExt[strings.ToLower(path.Ext(filename))]
	}
	switch mt {
	case "text/plain", "text/markdown", "text/x-markdown", "text/csv", "application/json", "text/html":
		return mt, nil
	}
	return "", fmt.Errorf("%w: supported types are text, markdown, csv, json and html", ErrUnsupported)
}

// extract turns a document into plain text (markdown headings kept). UTF-8 only; NULs stripped.
func extract(mt string, data []byte) (string, error) {
	if !utf8.Valid(data) {
		return "", fmt.Errorf("%w: document is not UTF-8 text", store.ErrInvalid)
	}
	data = bytes.ReplaceAll(data, []byte{0}, nil)
	var text string
	switch mt {
	case "application/json":
		var buf bytes.Buffer
		if err := json.Indent(&buf, data, "", "  "); err != nil {
			return "", fmt.Errorf("%w: invalid JSON", store.ErrInvalid)
		}
		text = buf.String()
	case "text/html":
		t, err := htmlText(data)
		if err != nil {
			return "", err
		}
		text = t
	default:
		text = string(data)
	}
	text = strings.TrimSpace(strings.ReplaceAll(text, "\r\n", "\n"))
	if len(text) > MaxTextBytes {
		return "", fmt.Errorf("%w: extracted text over %d MB", ErrLimit, MaxTextBytes>>20)
	}
	if text == "" {
		return "", fmt.Errorf("%w: document has no text", store.ErrInvalid)
	}
	return text, nil
}

var skipHTML = map[atom.Atom]bool{atom.Script: true, atom.Style: true, atom.Nav: true, atom.Footer: true,
	atom.Noscript: true, atom.Template: true, atom.Svg: true, atom.Iframe: true}

var blockHTML = map[atom.Atom]bool{atom.P: true, atom.Div: true, atom.Li: true, atom.Br: true, atom.Tr: true,
	atom.Section: true, atom.Article: true, atom.Main: true, atom.Header: true, atom.Blockquote: true,
	atom.Pre: true, atom.Table: true, atom.Ul: true, atom.Ol: true, atom.Dd: true, atom.Dt: true, atom.Hr: true}

var headingHTML = map[atom.Atom]int{atom.H1: 1, atom.H2: 2, atom.H3: 3, atom.H4: 4, atom.H5: 5, atom.H6: 6}

// htmlText keeps visible text: head (except title), script, style, nav and footer are dropped;
// title and h1–h6 become markdown headings; block elements become paragraph breaks.
func htmlText(data []byte) (string, error) {
	doc, err := html.Parse(bytes.NewReader(data))
	if err != nil {
		return "", fmt.Errorf("%w: invalid HTML", store.ErrInvalid)
	}
	var b strings.Builder
	var walk func(n *html.Node)
	inline := func(n *html.Node) string {
		var t strings.Builder
		var rec func(*html.Node)
		rec = func(n *html.Node) {
			if n.Type == html.TextNode {
				t.WriteString(n.Data)
			}
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				if c.Type != html.ElementNode || !skipHTML[c.DataAtom] {
					rec(c)
				}
			}
		}
		rec(n)
		return strings.Join(strings.Fields(t.String()), " ")
	}
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch {
			case skipHTML[n.DataAtom]:
				return
			case n.DataAtom == atom.Title:
				if t := inline(n); t != "" {
					b.WriteString("\n\n# " + t + "\n\n")
				}
				return
			case n.DataAtom == atom.Head:
				for c := n.FirstChild; c != nil; c = c.NextSibling {
					if c.Type == html.ElementNode && c.DataAtom == atom.Title {
						walk(c)
					}
				}
				return
			case headingHTML[n.DataAtom] > 0:
				if t := inline(n); t != "" {
					b.WriteString("\n\n" + strings.Repeat("#", headingHTML[n.DataAtom]) + " " + t + "\n\n")
				}
				return
			case blockHTML[n.DataAtom]:
				b.WriteString("\n\n")
				defer b.WriteString("\n\n")
			}
		}
		if n.Type == html.TextNode {
			if t := strings.Join(strings.Fields(n.Data), " "); t != "" {
				b.WriteString(t + " ")
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	// normalize: trim lines, collapse runs of blank lines
	var out []string
	blank := true
	for _, l := range strings.Split(b.String(), "\n") {
		l = strings.TrimSpace(l)
		if l == "" {
			if !blank {
				out = append(out, "")
			}
			blank = true
			continue
		}
		out, blank = append(out, l), false
	}
	return strings.Join(out, "\n"), nil
}
