package kb

import (
	"math"
	"strings"
	"unicode"
)

// BM25 parameters (docs/KB.md §3).
const (
	bm25K1 = 1.2
	bm25B  = 0.75
)

var stopwords = func() map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(`a an and are as at be but by for from has have how i if in into is it its
		of on or that the their then there these this to was were what when where which who why will with you your`) {
		m[w] = true
	}
	return m
}()

// tokens returns the fnv32a hashes of text's lowercase `[\p{L}\p{N}_-]+` words, stopwords dropped.
func tokens(text string) []uint32 {
	var out []uint32
	isWord := func(r rune) bool { return unicode.IsLetter(r) || unicode.IsNumber(r) || r == '_' || r == '-' }
	for _, w := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool { return !isWord(r) }) {
		if stopwords[w] {
			continue
		}
		h := uint32(2166136261)
		for i := 0; i < len(w); i++ {
			h = (h ^ uint32(w[i])) * 16777619
		}
		out = append(out, h)
	}
	return out
}

// A posting packs chunk id (high 24 bits) and term frequency (low 8 bits, saturated) in 4 bytes;
// store.KBMaxChunks (100k) is far below the 16M chunk ids that fit.
type posting uint32

func (p posting) chunk() uint32 { return uint32(p) >> 8 }
func (p posting) tf() float64   { return float64(p & 0xff) }

// bm25 is an inverted index over chunk texts. Texts are tokenized and dropped; only hashes stay.
type bm25 struct {
	post   map[uint32][]posting
	lens   []uint16 // tokens per chunk (saturated)
	avgLen float64
}

func newBM25() *bm25 { return &bm25{post: map[uint32][]posting{}} }

// add indexes the next chunk (chunk ids are assigned in call order).
func (b *bm25) add(text string) {
	id := uint32(len(b.lens))
	toks := tokens(text)
	tf := map[uint32]uint32{}
	for _, t := range toks {
		tf[t]++
	}
	for t, n := range tf {
		b.post[t] = append(b.post[t], posting(id<<8|min(n, 255)))
	}
	b.lens = append(b.lens, uint16(min(len(toks), math.MaxUint16)))
}

// finish trims postings and computes the average length; call once after the last add.
func (b *bm25) finish() {
	for t, p := range b.post {
		b.post[t] = p[:len(p):len(p)]
	}
	var sum float64
	for _, l := range b.lens {
		sum += float64(l)
	}
	if len(b.lens) > 0 {
		b.avgLen = sum / float64(len(b.lens))
	}
}

// bytes estimates the index's memory use.
func (b *bm25) bytes() int64 {
	n := int64(len(b.lens)) * 2
	for _, p := range b.post {
		n += 48 + int64(cap(p))*4 // map entry + slice header + postings
	}
	return n
}

// scores returns the BM25 score of every chunk matching any query token (allowed may be nil).
func (b *bm25) scores(query string, allowed func(uint32) bool) map[uint32]float64 {
	out := map[uint32]float64{}
	n := float64(len(b.lens))
	seen := map[uint32]bool{}
	for _, t := range tokens(query) {
		if seen[t] {
			continue
		}
		seen[t] = true
		ps := b.post[t]
		if len(ps) == 0 {
			continue
		}
		df := float64(len(ps))
		idf := math.Log(1 + (n-df+0.5)/(df+0.5))
		for _, p := range ps {
			c := p.chunk()
			if allowed != nil && !allowed(c) {
				continue
			}
			tf := p.tf()
			norm := bm25K1 * (1 - bm25B + bm25B*float64(b.lens[c])/max(b.avgLen, 1))
			out[c] += idf * tf * (bm25K1 + 1) / (tf + norm)
		}
	}
	return out
}
