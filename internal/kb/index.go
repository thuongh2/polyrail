package kb

import (
	"container/list"
	"context"
	"slices"
	"sync"

	"golang.org/x/sync/singleflight"
)

const (
	legSize = 50 // candidates per retrieval leg
	rrfK    = 60
)

type docInfo struct {
	id, filename string
	attrs        map[string]string
}

// index is one KB's searchable state on this node: int8 vectors in one flat slice, BM25
// postings and chunk → (doc, seq) refs. Chunk text is not kept; winners are read from the store.
type index struct {
	rev    int64
	dims   int
	vecs   []int8
	scales []float32
	doc    []uint32 // chunk → docs index
	seq    []uint32 // chunk → seq within its doc
	docs   []docInfo
	bm     *bm25
	size   int64 // estimated bytes
}

func newIndex(rev int64) *index { return &index{rev: rev, bm: newBM25()} }

// add appends a chunk; vectors of a different length than the first are skipped.
func (ix *index) add(doc uint32, seq int, vec []int8, scale float32, text string) {
	if ix.dims == 0 {
		ix.dims = len(vec)
	}
	if len(vec) != ix.dims {
		return
	}
	ix.vecs = append(ix.vecs, vec...)
	ix.scales = append(ix.scales, scale)
	ix.doc = append(ix.doc, doc)
	ix.seq = append(ix.seq, uint32(seq))
	ix.bm.add(text)
}

// finish computes BM25 statistics and the memory estimate; call once after the last add.
func (ix *index) finish() {
	ix.bm.finish()
	ix.size = int64(cap(ix.vecs)) + int64(cap(ix.scales))*4 + int64(cap(ix.doc)+cap(ix.seq))*4 + ix.bm.bytes()
	for _, d := range ix.docs {
		ix.size += 64 + int64(len(d.id)+len(d.filename))
		for k, v := range d.attrs {
			ix.size += int64(len(k)+len(v)) + 32
		}
	}
}

func (ix *index) chunks() int { return len(ix.scales) }

type scored struct {
	chunk uint32
	cos   float64
	rrf   float64
}

// search runs the vector leg (top 50 with cosine ≥ minScore) and the BM25 leg (top 50) over the
// chunks whose doc passes filter, fuses them with RRF and returns the best k. cos is 0 for
// BM25-only results.
func (ix *index) search(vec []float32, text string, k int, minScore float64, filter map[string][]string) []scored {
	n := ix.chunks()
	if n == 0 || k <= 0 {
		return nil
	}
	var allowedDoc []bool
	if len(filter) > 0 {
		allowedDoc = make([]bool, len(ix.docs))
		for i, d := range ix.docs {
			allowedDoc[i] = matches(d.attrs, filter)
		}
	}
	ok := func(c uint32) bool { return allowedDoc == nil || allowedDoc[ix.doc[c]] }

	var vleg []scored
	if len(vec) == ix.dims {
		q, qs := quantize(vec)
		minCos := float64(-2)
		for c := range n {
			if !ok(uint32(c)) {
				continue
			}
			cos := float64(qs) * float64(ix.scales[c]) * float64(dot(q, ix.vecs[c*ix.dims:(c+1)*ix.dims]))
			if cos < minScore || (len(vleg) == legSize && cos <= minCos) {
				continue
			}
			if len(vleg) < legSize {
				vleg = append(vleg, scored{chunk: uint32(c), cos: cos})
			} else {
				vleg[slices.IndexFunc(vleg, func(s scored) bool { return s.cos == minCos })] = scored{chunk: uint32(c), cos: cos}
			}
			if len(vleg) == legSize {
				minCos = slices.MinFunc(vleg, func(a, b scored) int { return cmpF(a.cos, b.cos) }).cos
			}
		}
		slices.SortFunc(vleg, func(a, b scored) int { return cmpF(b.cos, a.cos) })
	}

	var kleg []scored
	for c, sc := range ix.bm.scores(text, ok) {
		kleg = append(kleg, scored{chunk: c, rrf: sc})
	}
	slices.SortFunc(kleg, func(a, b scored) int {
		if r := cmpF(b.rrf, a.rrf); r != 0 {
			return r
		}
		return int(a.chunk) - int(b.chunk)
	})
	kleg = kleg[:min(len(kleg), legSize)]

	fused := map[uint32]*scored{}
	for r, s := range vleg {
		fused[s.chunk] = &scored{chunk: s.chunk, cos: s.cos, rrf: 1 / float64(rrfK+r+1)}
	}
	for r, s := range kleg {
		if f, hit := fused[s.chunk]; hit {
			f.rrf += 1 / float64(rrfK+r+1)
		} else {
			fused[s.chunk] = &scored{chunk: s.chunk, rrf: 1 / float64(rrfK+r+1)}
		}
	}
	out := make([]scored, 0, len(fused))
	for _, f := range fused {
		out = append(out, *f)
	}
	slices.SortFunc(out, func(a, b scored) int {
		if r := cmpF(b.rrf, a.rrf); r != 0 {
			return r
		}
		if r := cmpF(b.cos, a.cos); r != 0 {
			return r
		}
		return int(a.chunk) - int(b.chunk)
	})
	return out[:min(len(out), k)]
}

func cmpF(a, b float64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// matches ANDs the filter keys; each key matches when the attribute equals one of its values.
func matches(attrs map[string]string, filter map[string][]string) bool {
	for k, vals := range filter {
		v, ok := attrs[k]
		if !ok || !slices.Contains(vals, v) {
			return false
		}
	}
	return true
}

// indexCache keeps loaded indexes under a global byte budget, least recently used evicted first.
// Concurrent loads of one KB share a single load.
type indexCache struct {
	budget int64
	load   func(ctx context.Context, org, kb string) (*index, error)

	mu   sync.Mutex
	used int64
	ll   *list.List // front = most recent; values *cacheEntry
	m    map[string]*list.Element
	sf   singleflight.Group
}

type cacheEntry struct {
	key string
	ix  *index
}

func newIndexCache(budget int64, load func(ctx context.Context, org, kb string) (*index, error)) *indexCache {
	return &indexCache{budget: budget, load: load, ll: list.New(), m: map[string]*list.Element{}}
}

// get returns the KB's index, loading it when absent or older than rev.
func (c *indexCache) get(ctx context.Context, org, kb string, rev int64) (*index, error) {
	key := org + "/" + kb
	if ix := c.cached(key, rev); ix != nil {
		return ix, nil
	}
	v, err, _ := c.sf.Do(key, func() (any, error) {
		if ix := c.cached(key, rev); ix != nil { // a load finished between the check and Do
			return ix, nil
		}
		ix, err := c.load(context.WithoutCancel(ctx), org, kb)
		if err != nil {
			return nil, err
		}
		c.put(key, ix)
		return ix, nil
	})
	if err != nil {
		return nil, err
	}
	return v.(*index), nil
}

func (c *indexCache) cached(key string, rev int64) *index {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.m[key]; ok && e.Value.(*cacheEntry).ix.rev >= rev {
		c.ll.MoveToFront(e)
		return e.Value.(*cacheEntry).ix
	}
	return nil
}

func (c *indexCache) put(key string, ix *index) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.removeLocked(key)
	c.m[key] = c.ll.PushFront(&cacheEntry{key, ix})
	c.used += ix.size
	for c.used > c.budget && c.ll.Len() > 1 { // the newest index stays even when over budget alone
		c.removeLocked(c.ll.Back().Value.(*cacheEntry).key)
	}
	mIndexBytes.Set(float64(c.used))
}

func (c *indexCache) drop(org, kb string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.removeLocked(org + "/" + kb)
	mIndexBytes.Set(float64(c.used))
}

func (c *indexCache) removeLocked(key string) {
	if e, ok := c.m[key]; ok {
		c.used -= e.Value.(*cacheEntry).ix.size
		c.ll.Remove(e)
		delete(c.m, key)
	}
}
