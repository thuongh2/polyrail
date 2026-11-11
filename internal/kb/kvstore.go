package kb

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/agent-gateway/agent-gateway/internal/store"
)

// kvVectorStore is the default VectorStore: chunks on store.KV (sealed text, int8 vectors) and a
// per-node in-memory index per KB (index.go).
type kvVectorStore struct {
	s     *store.Store
	cache *indexCache
}

// chunkRec is the stored form of one chunk: x = sealed(section + "\n" + text), v = int8 vector, k = scale.
type chunkRec struct {
	X string  `json:"x"`
	V []byte  `json:"v"`
	K float32 `json:"k"`
}

// NewKVStore returns the default vector store with an index budget of indexBytes per node.
func NewKVStore(s *store.Store, indexBytes int64) VectorStore {
	v := &kvVectorStore{s: s}
	v.cache = newIndexCache(indexBytes, v.load)
	return v
}

func (v *kvVectorStore) box() (*store.Box, error) {
	if b := v.s.Box(); b != nil {
		return b, nil
	}
	return nil, errors.New("knowledge bases need MASTER_KEY")
}

func (v *kvVectorStore) Upsert(ctx context.Context, org, kb, doc string, chunks []Chunk) error {
	box, err := v.box()
	if err != nil {
		return err
	}
	recs := make(map[string][]byte, len(chunks))
	for _, c := range chunks {
		x, err := box.Seal(c.Section + "\n" + c.Text)
		if err != nil {
			return err
		}
		q, scale := quantize(c.Vec)
		raw, err := json.Marshal(chunkRec{X: x, V: int8Bytes(q), K: scale})
		if err != nil {
			return err
		}
		recs[store.KBChunkKey(org, kb, doc, c.Seq)] = raw
	}
	if err := v.DeleteDoc(ctx, org, kb, doc); err != nil {
		return err
	}
	return v.s.KV.Update(ctx, func(tx store.Tx) error {
		for k, raw := range recs {
			if err := tx.Put(k, raw); err != nil {
				return err
			}
		}
		return nil
	})
}

func (v *kvVectorStore) DeleteDoc(ctx context.Context, org, kb, doc string) error {
	return v.s.DeletePrefix(ctx, store.KBChunkPrefix(org, kb, doc))
}

func (v *kvVectorStore) DeleteKB(ctx context.Context, org, kb string) error {
	v.cache.drop(org, kb)
	return v.s.DeletePrefix(ctx, store.KBChunkPrefix(org, kb, ""))
}

func (v *kvVectorStore) Search(ctx context.Context, q Query) ([]Hit, error) {
	ix, err := v.cache.get(ctx, q.Org, q.KB, q.Rev)
	if err != nil {
		return nil, err
	}
	box, err := v.box()
	if err != nil {
		return nil, err
	}
	var hits []Hit
	for _, s := range ix.search(q.Vec, q.Text, q.TopK, q.MinScore, q.Filter) {
		d := ix.docs[ix.doc[s.chunk]]
		seq := int(ix.seq[s.chunk])
		raw, err := v.s.KV.Get(ctx, store.KBChunkKey(q.Org, q.KB, d.id, seq))
		if errors.Is(err, store.ErrNotFound) {
			continue // re-embedded or deleted since the index was loaded
		}
		if err != nil {
			return nil, err
		}
		var rec chunkRec
		if err := json.Unmarshal(raw, &rec); err != nil {
			return nil, err
		}
		plain, err := box.Open(rec.X)
		if err != nil {
			return nil, err
		}
		section, text, _ := strings.Cut(plain, "\n")
		hits = append(hits, Hit{KB: q.KB, Doc: d.id, Seq: seq, Score: s.cos, Text: text, Section: section,
			Filename: d.filename, Attributes: d.attrs})
	}
	return hits, nil
}

// load builds a KB's index from its chunks: only docs embedded with the KB's current model and
// dimensions count, so vector spaces never mix during a re-embed.
func (v *kvVectorStore) load(ctx context.Context, org, kbID string) (*index, error) {
	k, err := v.s.GetKB(ctx, org, kbID)
	if err != nil {
		return nil, err
	}
	box, err := v.box()
	if err != nil {
		return nil, err
	}
	docs, err := v.s.ListKBDocs(ctx, org, kbID, "", 0)
	if err != nil {
		return nil, err
	}
	ix := newIndex(k.Rev)
	docIdx := map[string]uint32{}
	for _, d := range docs {
		if d.EmbedModel == k.EmbedModel && d.Dimensions == k.Dimensions {
			docIdx[d.ID] = uint32(len(ix.docs))
			ix.docs = append(ix.docs, docInfo{id: d.ID, filename: d.Filename, attrs: d.Attributes})
		}
	}
	prefix := store.KBChunkPrefix(org, kbID, "")
	for after := ""; ; {
		items, err := v.s.KV.Scan(ctx, prefix, after, 1000)
		if err != nil {
			return nil, err
		}
		for _, it := range items {
			docID, seqStr, ok := strings.Cut(strings.TrimPrefix(it.Key, prefix), "/")
			di, eligible := docIdx[docID]
			seq, err := strconv.Atoi(seqStr)
			if !ok || !eligible || err != nil {
				continue
			}
			var rec chunkRec
			if err := json.Unmarshal(it.Value, &rec); err != nil {
				return nil, err
			}
			plain, err := box.Open(rec.X)
			if err != nil {
				return nil, err
			}
			ix.add(di, seq, bytesInt8(rec.V), rec.K, plain)
		}
		if len(items) < 1000 {
			break
		}
		after = items[len(items)-1].Key
	}
	ix.finish()
	mIndexLoads.Inc()
	return ix, nil
}

func int8Bytes(q []int8) []byte {
	b := make([]byte, len(q))
	for i, x := range q {
		b[i] = byte(x)
	}
	return b
}

func bytesInt8(b []byte) []int8 {
	q := make([]int8, len(b))
	for i, x := range b {
		q[i] = int8(x)
	}
	return q
}
