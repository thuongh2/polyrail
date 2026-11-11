// Package kb is the knowledge-base engine (docs/KB.md, decision D21): document ingestion,
// chunking, embedding through an injected Embedder, and hybrid (vector + BM25, RRF-fused)
// retrieval. The gateway imports kb, never the reverse; gateway-specific pieces (embedding
// billing, the public-URL check and dialer) are injected through Embedder and Config.
//
// Service API (errors: store.ErrNotFound, store.ErrInvalid, ErrLimit, ErrUnsupported):
//
//	New(s *store.Store, emb Embedder, cfg Config) *Service
//	(*Service).Start(ctx)                                  // ingestion workers until ctx ends
//	CreateKB(ctx, k *store.KB) error                       // k.OrgID, Name, EmbedModel set; zero settings get defaults
//	GetKB(ctx, org, id) (*store.KB, error)
//	ListKBs(ctx, org) ([]store.KB, error)
//	UpdateKB(ctx, org, id, fn func(*store.KB) error) (*store.KB, error) // re-embeds every doc when model, dimensions or chunking change
//	DeleteKB(ctx, org, id) error
//	Reindex(ctx, org, id) error                            // re-queues every doc
//	AddDoc(ctx, org, kb, DocInput) (*store.KBDoc, error)   // extracts now, embeds asynchronously (status queued)
//	ListDocs(ctx, org, kb, after, limit) ([]store.KBDoc, error)
//	GetDoc(ctx, org, kb, doc) (*store.KBDoc, error)
//	DeleteDoc(ctx, org, kb, doc) error
//	Search(ctx, org, kbIDs, query, keyID, SearchOpts) ([]Hit, error) // embeds the query (billed to keyID)
//	Sig(ctx, org, kbIDs) (string, error)                   // "id@rev,…" for cache keys; ErrNotFound for unknown ids
//
// The caller validates that EmbedModel is an embedding model visible to the org.
package kb

import (
	"context"
	"errors"
	"fmt"
	"net"
	"path"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/agent-gateway/agent-gateway/internal/store"
)

var (
	// ErrLimit: a knowledge-base limit would be exceeded (same value as store.ErrLimit).
	ErrLimit = store.ErrLimit
	// ErrUnsupported: the document's media type cannot be ingested (HTTP 415).
	ErrUnsupported = errors.New("unsupported media type")
	// ErrInsufficientCredits is returned (wrapped) by an Embedder when the org cannot pay; the
	// document fails without retries.
	ErrInsufficientCredits = errors.New("insufficient credits")
)

// Defaults and bounds of KB settings (docs/KB.md §1).
const (
	DefaultChunkChars       = 1600
	DefaultOverlapChars     = 200
	DefaultTopK             = 5
	DefaultMinScore         = 0.25
	DefaultMaxContextTokens = 2000
	MaxKBsPerRequest        = 3
	MaxSearchResults        = 50
	MaxQueryChars           = 2000
	MaxDimensions           = 8192 // longest embedding vector stored
	maxAttempts             = 3
)

// Chunk is one retrievable piece of a document. Vec is set before VectorStore.Upsert.
type Chunk struct {
	Seq           int
	Text, Section string
	Vec           []float32
}

// Query is one KB search. Filter keys are ANDed; a key matches when the doc attribute equals any
// listed value.
type Query struct {
	Org, KB  string
	Rev      int64
	Vec      []float32
	Text     string
	TopK     int
	MinScore float64
	Filter   map[string][]string
}

// Hit is one retrieved chunk. Score is the cosine similarity (0 when only BM25 matched).
type Hit struct {
	KB         string            `json:"kb_id"`
	Doc        string            `json:"doc_id"`
	Seq        int               `json:"seq"`
	Score      float64           `json:"score"`
	Section    string            `json:"section,omitempty"`
	Text       string            `json:"text"`
	Filename   string            `json:"filename"`
	Attributes map[string]string `json:"attributes,omitempty"`
}

// MaxDocBytes is the upload limit (KB_MAX_DOC_MB).
func (s *Service) MaxDocBytes() int64 { return int64(s.cfg.MaxDocMB) << 20 }

// VectorStore stores chunks and answers hybrid searches. The default keeps everything on store.KV.
type VectorStore interface {
	Upsert(ctx context.Context, org, kb, doc string, chunks []Chunk) error // replaces the doc's chunks
	DeleteDoc(ctx context.Context, org, kb, doc string) error
	DeleteKB(ctx context.Context, org, kb string) error
	Search(ctx context.Context, q Query) ([]Hit, error) // hybrid, fused
}

// Embedder embeds texts with an org's model, billing the org (keyID "" = console). Implemented by
// the gateway. It returns one vector per text, in order.
type Embedder interface {
	Embed(ctx context.Context, org, keyID, model string, dims int, texts []string) ([][]float32, error)
}

// Config configures a Service. Zero values take the defaults noted.
type Config struct {
	Workers      int         // ingestion goroutines per node (KB_WORKERS, default 1)
	IndexMB      int         // in-memory index budget per node (KB_INDEX_MB, default 256)
	MaxOrgChunks int         // chunks per org (KB_MAX_ORG_CHUNKS, default 300000)
	MaxDocMB     int         // upload size (KB_MAX_DOC_MB, default 10)
	VectorStore  VectorStore // default: NewKVStore
	// URL ingestion: CheckURL validates the URL and every redirect; DialContext opens connections
	// (the gateway passes CheckPublicURL and PublicDialer). Both nil = URL ingestion disabled.
	CheckURL    func(raw string) error
	DialContext func(ctx context.Context, network, addr string) (net.Conn, error)
}

// DocInput is one document to add: exactly one of Data (an upload), Text (pasted text) or URL.
type DocInput struct {
	Filename    string
	ContentType string // of Data; "" = by filename extension
	Data        []byte
	Text        string
	URL         string
	Attributes  map[string]string
	KeyID       string // API key adding the doc; its embedding is billed to it ("" = console)
}

// SearchOpts overrides KB settings for one search; zero values use the KBs' settings.
type SearchOpts struct {
	TopK             int
	MinScore         float64
	Filter           map[string][]string
	MaxContextTokens int
}

// Service is the KB engine of one node.
type Service struct {
	st  *store.Store
	emb Embedder
	vs  VectorStore
	cfg Config

	// worker timing (tests shorten them)
	poll, lease, retry time.Duration

	metaMu sync.Mutex
	meta   map[string]metaEntry
}

type metaEntry struct {
	kb *store.KB
	at time.Time
}

const metaTTL = 2 * time.Second

var (
	mSearch     = prometheus.NewHistogram(prometheus.HistogramOpts{Name: "gw_kb_search_seconds", Help: "Knowledge-base search latency (query embedding included).", Buckets: []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5}})
	mIndexBytes = prometheus.NewGauge(prometheus.GaugeOpts{Name: "gw_kb_index_bytes", Help: "Estimated bytes of knowledge-base indexes held by this node."})
	mIndexLoads = prometheus.NewCounter(prometheus.CounterOpts{Name: "gw_kb_index_loads_total", Help: "Knowledge-base index loads (cold, evicted or changed)."})
	mIngest     = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "gw_kb_ingest_total", Help: "Document ingestion runs by outcome (ready, retry, failed)."}, []string{"status"})
	mQueueDepth = prometheus.NewGauge(prometheus.GaugeOpts{Name: "gw_kb_queue_depth", Help: "Documents waiting in the ingestion queue."})
)

func init() {
	prometheus.MustRegister(mSearch, mIndexBytes, mIndexLoads, mIngest, mQueueDepth)
}

// New returns a Service; call Start to run ingestion workers.
func New(s *store.Store, emb Embedder, cfg Config) *Service {
	if cfg.Workers <= 0 {
		cfg.Workers = 1
	}
	if cfg.IndexMB <= 0 {
		cfg.IndexMB = 256
	}
	if cfg.MaxOrgChunks <= 0 {
		cfg.MaxOrgChunks = 300000
	}
	if cfg.MaxDocMB <= 0 {
		cfg.MaxDocMB = 10
	}
	if cfg.VectorStore == nil {
		cfg.VectorStore = NewKVStore(s, int64(cfg.IndexMB)<<20)
	}
	return &Service{st: s, emb: emb, vs: cfg.VectorStore, cfg: cfg, poll: time.Second, lease: 2 * time.Minute,
		retry: 10 * time.Second, meta: map[string]metaEntry{}}
}

func invalid(msg string) error { return fmt.Errorf("%w: %s", store.ErrInvalid, msg) }

// applyDefaults fills zero settings of a new KB.
func applyDefaults(k *store.KB) {
	if k.ChunkChars == 0 {
		k.ChunkChars = DefaultChunkChars
		if k.OverlapChars == 0 {
			k.OverlapChars = DefaultOverlapChars
		}
	}
	if k.TopK == 0 {
		k.TopK = DefaultTopK
	}
	if k.MinScore == 0 {
		k.MinScore = DefaultMinScore
	}
	if k.MaxContextTokens == 0 {
		k.MaxContextTokens = DefaultMaxContextTokens
	}
}

func validateKB(k *store.KB) error {
	k.Name, k.Description, k.EmbedModel = strings.TrimSpace(k.Name), strings.TrimSpace(k.Description), strings.TrimSpace(k.EmbedModel)
	switch {
	case k.Name == "" || utf8.RuneCountInString(k.Name) > 128:
		return invalid("name must be 1-128 characters")
	case utf8.RuneCountInString(k.Description) > 1024:
		return invalid("description must be at most 1024 characters")
	case k.EmbedModel == "" || len(k.EmbedModel) > 256:
		return invalid("embedding_model required")
	case k.Dimensions < 0 || k.Dimensions > MaxDimensions:
		return invalid("dimensions must be 0 (native) to 8192")
	case k.ChunkChars < 200 || k.ChunkChars > 8000:
		return invalid("chunk_chars must be 200-8000")
	case k.OverlapChars < 0 || k.OverlapChars > k.ChunkChars/2:
		return invalid("overlap_chars must be 0 to chunk_chars/2")
	case k.TopK < 1 || k.TopK > 20:
		return invalid("top_k must be 1-20")
	case k.MinScore < -1 || k.MinScore > 1:
		return invalid("min_score must be -1 to 1")
	case k.MaxContextTokens < 100 || k.MaxContextTokens > 16000:
		return invalid("max_context_tokens must be 100-16000")
	}
	return nil
}

// CreateKB validates k (zero settings take defaults) and stores it; k gets its ID and Rev.
func (s *Service) CreateKB(ctx context.Context, k *store.KB) error {
	applyDefaults(k)
	if err := validateKB(k); err != nil {
		return err
	}
	return s.st.CreateKB(ctx, k)
}

func (s *Service) GetKB(ctx context.Context, org, id string) (*store.KB, error) {
	return s.st.GetKB(ctx, org, id)
}

func (s *Service) ListKBs(ctx context.Context, org string) ([]store.KB, error) {
	return s.st.ListKBs(ctx, org)
}

// UpdateKB applies fn (identity, counters and Rev cannot change) and validates the result.
// Changing embedding_model or dimensions bumps Rev (search ignores docs not yet re-embedded);
// changing those or chunk settings re-queues every doc.
func (s *Service) UpdateKB(ctx context.Context, org, id string, fn func(*store.KB) error) (*store.KB, error) {
	var reembed bool
	k, err := s.st.UpdateKB(ctx, org, id, func(k *store.KB) error {
		before := *k
		if err := fn(k); err != nil {
			return err
		}
		if err := validateKB(k); err != nil {
			return err
		}
		k.Rev = before.Rev
		if k.EmbedModel != before.EmbedModel || k.Dimensions != before.Dimensions {
			k.Rev++
		}
		reembed = k.Rev != before.Rev || k.ChunkChars != before.ChunkChars || k.OverlapChars != before.OverlapChars
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.forget(org, id)
	if reembed {
		return k, s.Reindex(ctx, org, id)
	}
	return k, nil
}

// DeleteKB removes the KB, its documents and chunks.
func (s *Service) DeleteKB(ctx context.Context, org, id string) error {
	if err := s.st.DeleteKB(ctx, org, id); err != nil {
		return err
	}
	s.forget(org, id)
	return s.vs.DeleteKB(ctx, org, id)
}

// Reindex re-queues every document of the KB (chunked and embedded again with current settings).
func (s *Service) Reindex(ctx context.Context, org, id string) error {
	if _, err := s.st.GetKB(ctx, org, id); err != nil {
		return err
	}
	docs, err := s.st.ListKBDocs(ctx, org, id, "", 0)
	if err != nil {
		return err
	}
	for _, d := range docs {
		if err := s.st.RequeueKBDoc(ctx, org, id, d.ID); err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
	}
	return nil
}

// cleanFilename keeps a display-safe base name: no directories, no control characters, ≤255 bytes.
func cleanFilename(name, fallback string) string {
	name = path.Base(strings.ReplaceAll(name, "\\", "/"))
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, strings.TrimSpace(name))
	if name == "" || name == "." || name == "/" {
		name = fallback
	}
	for len(name) > 255 {
		_, w := utf8.DecodeLastRuneInString(name)
		name = name[:len(name)-w]
	}
	return name
}

func validateAttributes(a map[string]string) error {
	if len(a) > store.KBMaxAttributes {
		return invalid(fmt.Sprintf("at most %d attributes", store.KBMaxAttributes))
	}
	for k, v := range a {
		if k == "" || len(k) > 64 || len(v) > 256 {
			return invalid("attribute keys must be 1-64 bytes and values at most 256 bytes")
		}
	}
	return nil
}

// AddDoc extracts the document's text now (fetching a URL first) and queues it for chunking and
// embedding. The returned doc has status queued.
func (s *Service) AddDoc(ctx context.Context, org, kbID string, in DocInput) (*store.KBDoc, error) {
	if _, err := s.st.GetKB(ctx, org, kbID); err != nil {
		return nil, err
	}
	if err := validateAttributes(in.Attributes); err != nil {
		return nil, err
	}
	given := 0
	for _, set := range []bool{in.Data != nil, in.Text != "", in.URL != ""} {
		if set {
			given++
		}
	}
	if given != 1 {
		return nil, invalid("give exactly one of a file, text or url")
	}
	d := &store.KBDoc{ID: store.NewKBDocID(), KBID: kbID, OrgID: org, Attributes: in.Attributes}
	data, ct := in.Data, in.ContentType
	switch {
	case in.URL != "":
		f, err := s.fetchURL(ctx, in.URL)
		if err != nil {
			return nil, err
		}
		d.Source, d.URL, data, ct = "url", in.URL, f.data, f.contentType
		d.Filename = cleanFilename(in.Filename, f.filename)
	case in.Text != "":
		d.Source, data = "text", []byte(in.Text)
		d.Filename = cleanFilename(in.Filename, "text.txt")
		if ct == "" && path.Ext(d.Filename) == "" {
			ct = "text/plain"
		}
	default:
		d.Source = "upload"
		d.Filename = cleanFilename(in.Filename, "document.txt")
	}
	if len(data) > s.cfg.MaxDocMB<<20 {
		return nil, fmt.Errorf("%w: documents are limited to %d MB", ErrLimit, s.cfg.MaxDocMB)
	}
	mt, err := mediaType(ct, d.Filename)
	if err != nil {
		return nil, err
	}
	text, err := extract(mt, data)
	if err != nil {
		return nil, err
	}
	d.ContentType, d.Bytes, d.Chars, d.KeyID = mt, int64(len(data)), utf8.RuneCountInString(text), in.KeyID
	if err := s.st.AddKBDoc(ctx, d, text); err != nil {
		return nil, err
	}
	return d, nil
}

func (s *Service) ListDocs(ctx context.Context, org, kbID, after string, limit int) ([]store.KBDoc, error) {
	if _, err := s.st.GetKB(ctx, org, kbID); err != nil {
		return nil, err
	}
	return s.st.ListKBDocs(ctx, org, kbID, after, limit)
}

func (s *Service) GetDoc(ctx context.Context, org, kbID, doc string) (*store.KBDoc, error) {
	return s.st.GetKBDoc(ctx, org, kbID, doc)
}

// DeleteDoc removes a document and its chunks.
func (s *Service) DeleteDoc(ctx context.Context, org, kbID, doc string) error {
	if err := s.st.DeleteKBDoc(ctx, org, kbID, doc); err != nil {
		return err
	}
	s.forget(org, kbID)
	return s.vs.DeleteDoc(ctx, org, kbID, doc)
}

// kbMeta returns a KB record cached for up to 2 s (search path).
func (s *Service) kbMeta(ctx context.Context, org, id string) (*store.KB, error) {
	key := org + "/" + id
	s.metaMu.Lock()
	e, ok := s.meta[key]
	s.metaMu.Unlock()
	if ok && time.Since(e.at) < metaTTL {
		return e.kb, nil
	}
	k, err := s.st.GetKB(ctx, org, id)
	if err != nil {
		return nil, err
	}
	s.metaMu.Lock()
	if len(s.meta) >= 10000 {
		clear(s.meta)
	}
	s.meta[key] = metaEntry{k, time.Now()}
	s.metaMu.Unlock()
	return k, nil
}

func (s *Service) forget(org, id string) {
	s.metaMu.Lock()
	delete(s.meta, org+"/"+id)
	s.metaMu.Unlock()
}

// kbs resolves up to MaxKBsPerRequest distinct KB ids of org.
func (s *Service) kbs(ctx context.Context, org string, ids []string) ([]*store.KB, error) {
	ids = slices.Compact(slices.Sorted(slices.Values(ids)))
	if len(ids) > MaxKBsPerRequest {
		return nil, fmt.Errorf("%w: at most %d knowledge bases per request", ErrLimit, MaxKBsPerRequest)
	}
	var out []*store.KB
	for _, id := range ids {
		k, err := s.kbMeta(ctx, org, id)
		if err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, nil
}

// Sig returns the sorted "id@rev" list of the KBs, for cache keys that must change with content.
func (s *Service) Sig(ctx context.Context, org string, kbIDs []string) (string, error) {
	ks, err := s.kbs(ctx, org, kbIDs)
	if err != nil {
		return "", err
	}
	parts := make([]string, len(ks))
	for i, k := range ks {
		parts[i] = k.ID + "@" + strconv.FormatInt(k.Rev, 10)
	}
	slices.Sort(parts)
	return strings.Join(parts, ","), nil
}

// Search embeds query once per distinct embedding model (billed to keyID), searches each KB,
// fuses the per-KB rankings with RRF and returns at most TopK hits whose text fits
// MaxContextTokens (chars/4). A query shorter than 3 characters returns no hits.
func (s *Service) Search(ctx context.Context, org string, kbIDs []string, query, keyID string, o SearchOpts) ([]Hit, error) {
	start := time.Now()
	if o.TopK < 0 || o.TopK > MaxSearchResults {
		return nil, invalid(fmt.Sprintf("top_k must be 1-%d", MaxSearchResults))
	}
	if len(o.Filter) > store.KBMaxAttributes {
		return nil, invalid("too many filters")
	}
	ks, err := s.kbs(ctx, org, kbIDs)
	if err != nil {
		return nil, err
	}
	query = strings.TrimSpace(query)
	if r := []rune(query); len(r) > MaxQueryChars {
		query = string(r[:MaxQueryChars])
	}
	if utf8.RuneCountInString(query) < 3 || len(ks) == 0 {
		return nil, nil
	}
	topK, maxTok := o.TopK, o.MaxContextTokens
	for _, k := range ks {
		if o.TopK == 0 {
			topK = max(topK, k.TopK)
		}
		if o.MaxContextTokens == 0 {
			maxTok = max(maxTok, k.MaxContextTokens)
		}
	}
	vecs := map[string][]float32{}
	var lists [][]Hit
	for _, k := range ks {
		ek := k.EmbedModel + "|" + strconv.Itoa(k.Dimensions)
		vec, ok := vecs[ek]
		if !ok {
			out, err := s.emb.Embed(ctx, org, keyID, k.EmbedModel, k.Dimensions, []string{query})
			if err != nil {
				return nil, err
			}
			if len(out) != 1 {
				return nil, errors.New("embedding returned no vector")
			}
			vec, vecs[ek] = out[0], out[0]
		}
		minScore := o.MinScore
		if minScore == 0 {
			minScore = k.MinScore
		}
		hits, err := s.vs.Search(ctx, Query{Org: org, KB: k.ID, Rev: k.Rev, Vec: vec, Text: query, TopK: topK, MinScore: minScore, Filter: o.Filter})
		if err != nil {
			return nil, err
		}
		lists = append(lists, hits)
	}
	hits := fuse(lists, topK)
	mSearch.Observe(time.Since(start).Seconds())
	return packHits(hits, maxTok*4), nil
}

// fuse merges ranked lists with RRF (k=60), ties broken by cosine score.
func fuse(lists [][]Hit, k int) []Hit {
	if len(lists) == 1 {
		return lists[0][:min(len(lists[0]), k)]
	}
	type fh struct {
		h   Hit
		rrf float64
	}
	var all []fh
	for _, l := range lists {
		for r, h := range l {
			all = append(all, fh{h, 1 / float64(rrfK+r+1)})
		}
	}
	slices.SortStableFunc(all, func(a, b fh) int {
		if c := cmpF(b.rrf, a.rrf); c != 0 {
			return c
		}
		return cmpF(b.h.Score, a.h.Score)
	})
	out := make([]Hit, 0, min(len(all), k))
	for _, f := range all[:min(len(all), k)] {
		out = append(out, f.h)
	}
	return out
}

// packHits keeps hits while their text fits budget characters; a first hit longer than the
// budget is truncated.
func packHits(hits []Hit, budget int) []Hit {
	var out []Hit
	used := 0
	for _, h := range hits {
		n := utf8.RuneCountInString(h.Text)
		if used+n > budget {
			if len(out) == 0 {
				h.Text, _ = cutRunes(h.Text, budget)
				out = append(out, h)
			}
			break
		}
		used += n
		out = append(out, h)
	}
	return out
}
