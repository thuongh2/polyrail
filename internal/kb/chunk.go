package kb

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

const minChunkChars = 20

// chunkText splits text into chunks of at most size characters (runes, overlap included) per
// docs/KB.md §2: sections at `#`–`###` headings keep their heading path ("A > B > C"), paragraphs
// are packed up to size, oversize paragraphs split at sentence ends and then hard-cut, and each
// chunk after the first in a section starts with up to overlap characters of the previous one.
func chunkText(text string, size, overlap int) []Chunk {
	var out []Chunk
	for _, sec := range sections(text) {
		for _, t := range pack(units(sec.body, size), size, overlap) {
			if utf8.RuneCountInString(strings.TrimSpace(t)) < minChunkChars {
				continue
			}
			out = append(out, Chunk{Seq: len(out), Section: sec.path, Text: t})
		}
	}
	return out
}

type section struct{ path, body string }

// sections splits markdown at level 1–3 headings outside fenced code blocks.
func sections(text string) []section {
	var out []section
	var path [3]string
	var body strings.Builder
	fenced := false
	flush := func() {
		if strings.TrimSpace(body.String()) != "" {
			var parts []string
			for _, p := range path {
				if p != "" {
					parts = append(parts, p)
				}
			}
			out = append(out, section{strings.Join(parts, " > "), body.String()})
		}
		body.Reset()
	}
	for line := range strings.Lines(text) {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			fenced = !fenced
		}
		if lvl, title := heading(trimmed); !fenced && lvl > 0 {
			flush()
			path[lvl-1] = title
			for i := lvl; i < len(path); i++ {
				path[i] = ""
			}
			continue
		}
		body.WriteString(line)
	}
	flush()
	return out
}

// heading returns the level (1–3) and title of a markdown heading line, or 0.
func heading(line string) (int, string) {
	n := 0
	for n < len(line) && line[n] == '#' {
		n++
	}
	if n == 0 || n > 3 || n == len(line) || (line[n] != ' ' && line[n] != '\t') {
		return 0, ""
	}
	title := strings.TrimSpace(strings.TrimRight(line[n:], "# \t"))
	if title == "" {
		return 0, ""
	}
	return n, title
}

// units splits a section body into paragraphs of at most size runes each.
func units(body string, size int) []string {
	var out []string
	for _, para := range splitParagraphs(body) {
		if utf8.RuneCountInString(para) <= size {
			out = append(out, para)
			continue
		}
		var cur string
		for _, s := range sentences(para) {
			for utf8.RuneCountInString(s) > size { // hard cut on a rune boundary
				head, rest := cutRunes(s, size)
				if cur != "" {
					out, cur = append(out, cur), ""
				}
				out, s = append(out, head), strings.TrimSpace(rest)
			}
			switch {
			case s == "":
			case cur == "":
				cur = s
			case utf8.RuneCountInString(cur)+1+utf8.RuneCountInString(s) <= size:
				cur += " " + s
			default:
				out, cur = append(out, cur), s
			}
		}
		if cur != "" {
			out = append(out, cur)
		}
	}
	return out
}

func splitParagraphs(body string) []string {
	var out []string
	var cur []string
	for line := range strings.Lines(body) {
		if strings.TrimSpace(line) == "" {
			if len(cur) > 0 {
				out = append(out, strings.TrimSpace(strings.Join(cur, "")))
				cur = nil
			}
			continue
		}
		cur = append(cur, line)
	}
	if len(cur) > 0 {
		out = append(out, strings.TrimSpace(strings.Join(cur, "")))
	}
	return out
}

// sentences splits after '.', '!' or '?' followed by whitespace, and at newlines.
func sentences(p string) []string {
	var out []string
	start := 0
	rs := []rune(p)
	for i, r := range rs {
		end := r == '\n' || ((r == '.' || r == '!' || r == '?') && i+1 < len(rs) && unicode.IsSpace(rs[i+1]))
		if end {
			if s := strings.TrimSpace(string(rs[start : i+1])); s != "" {
				out = append(out, s)
			}
			start = i + 1
		}
	}
	if s := strings.TrimSpace(string(rs[start:])); s != "" {
		out = append(out, s)
	}
	return out
}

// cutRunes splits s after n runes.
func cutRunes(s string, n int) (string, string) {
	i := 0
	for range n {
		_, w := utf8.DecodeRuneInString(s[i:])
		i += w
	}
	return s[:i], s[i:]
}

// tail returns the last n runes of s, starting at a word boundary when one exists.
func tail(s string, n int) string {
	if n <= 0 {
		return ""
	}
	rc := utf8.RuneCountInString(s)
	if rc <= n {
		return s
	}
	_, t := cutRunes(s, rc-n)
	if i := strings.IndexFunc(t, unicode.IsSpace); i >= 0 && i < len(t)-1 {
		t = t[i:]
	}
	return strings.TrimSpace(t)
}

// pack joins units into chunks of at most size runes, carrying overlap runes forward.
func pack(us []string, size, overlap int) []string {
	var out []string
	cur := ""
	for _, u := range us {
		if cur != "" && utf8.RuneCountInString(cur)+2+utf8.RuneCountInString(u) > size {
			out = append(out, cur)
			cur = tail(cur, overlap)
			if cur != "" && utf8.RuneCountInString(cur)+2+utf8.RuneCountInString(u) > size {
				cur = ""
			}
		}
		if cur == "" {
			cur = u
		} else {
			cur += "\n\n" + u
		}
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}
