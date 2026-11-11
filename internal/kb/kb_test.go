package kb

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"math"
	"math/rand/v2"
	"net"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/polyrail/polyrail/internal/store"
)

// hashEmbed is the test embedder of docs/KB.md §9: hashed bag of words, L2-normalized.
func hashEmbed(text string, dims int) []float32 {
	if dims == 0 {
		dims = 64
	}
	v := make([]float32, dims)
	for _, w := range regexp.MustCompile(`[a-z0-9]+`).FindAllString(strings.ToLower(text), -1) {
		h := fnv.New32a()
		_, _ = h.Write([]byte(w))
		x := h.Sum32()
		if x&(1<<31) != 0 {
			v[x%uint32(dims)]--
		} else {
			v[x%uint32(dims)]++
		}
	}
	var n float64
	for _, x := range v {
		n += float64(x * x)
	}
	if n > 0 {
		for i := range v {
			v[i] /= float32(math.Sqrt(n))
		}
	}
	return v
}

type fakeEmb struct {
	calls  atomic.Int64
	inputs atomic.Int64
	mu     sync.Mutex
	err    error
	keys   []string
}

func (f *fakeEmb) Embed(_ context.Context, org, keyID, model string, dims int, texts []string) ([][]float32, error) {
	f.calls.Add(1)
	f.inputs.Add(int64(len(texts)))
	f.mu.Lock()
	err := f.err
	f.keys = append(f.keys, keyID)
	f.mu.Unlock()
	if err != nil {
		return nil, err
	}
	out := make([][]float32, len(texts))
	for i, t := range texts {
		out[i] = hashEmbed(model+" "+t, dims) // model in the input: different models give different spaces
	}
	return out, nil
}

func (f *fakeEmb) setErr(err error) { f.mu.Lock(); f.err = err; f.mu.Unlock() }

func newTestService(t *testing.T) (*Service, *fakeEmb, *store.Store, string) {
	t.Helper()
	kv, err := store.OpenPebble(t.TempDir(), 8)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { kv.Close() })
	box, _ := store.NewBox("0123456789abcdef-kb-test")
	st := store.New(kv, box)
	org := &store.Org{Name: "acme"}
	if err := st.CreateOrg(context.Background(), org, ""); err != nil {
		t.Fatal(err)
	}
	emb := &fakeEmb{}
	s := New(st, emb, Config{})
	s.poll, s.retry = 5*time.Millisecond, 0
	return s, emb, st, org.ID
}

func waitStatus(t *testing.T, s *Service, org, kb, doc, want string) *store.KBDoc {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		d, err := s.GetDoc(context.Background(), org, kb, doc)
		if err == nil && d.Status == want {
			return d
		}
		if time.Now().After(deadline) {
			t.Fatalf("doc %s: want %s, got %+v %v", doc, want, d, err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

const handbook = `# Handbook

Welcome to the company handbook. It covers how we work together every day.

## Billing

### Refunds

Customers can request a refund within 30 days of purchase. Refunds go back to the original card.

### Invoices

Invoices are emailed on the first business day of each month as PDF attachments.

## Travel

Book flights through the travel portal at least two weeks ahead. Economy class for trips under six hours.
`

func TestChunker(t *testing.T) {
	cs := chunkText(handbook, 200, 50)
	var sections []string
	for _, c := range cs {
		sections = append(sections, c.Section)
		if n := len([]rune(c.Text)); n > 200 || n < minChunkChars {
			t.Fatalf("chunk size %d: %q", n, c.Text)
		}
	}
	want := []string{"Handbook", "Handbook > Billing > Refunds", "Handbook > Billing > Invoices", "Handbook > Travel"}
	if fmt.Sprint(sections) != fmt.Sprint(want) {
		t.Fatalf("sections %q", sections)
	}
	// oversize paragraph: sentence split, overlap within the section, hard cut of a long word
	long := strings.Repeat("Alpha beta gamma delta. ", 40) + strings.Repeat("x", 450)
	cs = chunkText("# S\n\n"+long, 200, 40)
	if len(cs) < 6 {
		t.Fatalf("got %d chunks", len(cs))
	}
	for i, c := range cs {
		if len([]rune(c.Text)) > 200 || c.Section != "S" || c.Seq != i {
			t.Fatalf("chunk %d: %+v", i, c)
		}
	}
	if !strings.HasPrefix(cs[1].Text, "delta.") && !strings.Contains(cs[0].Text[len(cs[0].Text)-40:], cs[1].Text[:10]) {
		t.Fatalf("no overlap between %q and %q", cs[0].Text, cs[1].Text)
	}
	// headings inside code fences are text; tiny chunks dropped
	cs = chunkText("# A\n\n```\n# not a heading\nprint(1) and some more code here\n```\n\n# B\n\nok", 1600, 200)
	if len(cs) != 1 || cs[0].Section != "A" || !strings.Contains(cs[0].Text, "# not a heading") {
		t.Fatalf("fence: %+v", cs)
	}
}

func TestExtract(t *testing.T) {
	page := `<html><head><title>Docs  Home</title><style>x{}</style><script>alert(1)</script></head>
<body><nav>Menu Links</nav><h2>Install</h2><p>Run the   <b>installer</b>.</p><ul><li>one</li><li>two</li></ul>
<footer>Copyright</footer></body></html>`
	mt, err := mediaType("text/html; charset=utf-8", "x")
	if err != nil {
		t.Fatal(err)
	}
	got, err := extract(mt, []byte(page))
	if err != nil {
		t.Fatal(err)
	}
	if want := "# Docs Home\n\n## Install\n\nRun the installer .\n\none\n\ntwo"; got != want {
		t.Fatalf("html:\n%q\nwant\n%q", got, want)
	}
	for _, bad := range []string{"alert", "Menu", "Copyright", "x{}"} {
		if strings.Contains(got, bad) {
			t.Fatalf("html kept %q", bad)
		}
	}
	mt, _ = mediaType("application/octet-stream", "data.json")
	if got, err := extract(mt, []byte(`{"a":[1,2]}`)); err != nil || got != "{\n  \"a\": [\n    1,\n    2\n  ]\n}" {
		t.Fatalf("json %q %v", got, err)
	}
	if _, err := extract("application/json", []byte(`{bad`)); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("bad json: %v", err)
	}
	if mt, _ := mediaType("", "t.csv"); mt != "text/csv" {
		t.Fatalf("csv %q", mt)
	}
	if got, _ := extract("text/plain", []byte("a\x00b")); got != "ab" {
		t.Fatalf("nul %q", got)
	}
	if _, err := extract("text/plain", []byte{0xff, 0xfe}); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("utf8: %v", err)
	}
	if _, err := mediaType("application/pdf", "a.pdf"); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("pdf: %v", err)
	}
	if _, err := extract("text/plain", []byte(strings.Repeat("a", MaxTextBytes+1))); !errors.Is(err, ErrLimit) {
		t.Fatalf("size: %v", err)
	}
}

func TestQuantizationRoundTrip(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	vec := func() []float32 {
		v := make([]float32, 512)
		for i := range v {
			v[i] = float32(r.NormFloat64())
		}
		return v
	}
	cos := func(a, b []float32) float64 {
		var d, na, nb float64
		for i := range a {
			d += float64(a[i] * b[i])
			na += float64(a[i] * a[i])
			nb += float64(b[i] * b[i])
		}
		return d / math.Sqrt(na*nb)
	}
	worst := 0.0
	for range 200 {
		a := vec()
		b := vec()
		for i := range b { // correlated pairs too, not only near-orthogonal ones
			b[i] = a[i]*float32(r.Float64()) + b[i]*0.5
		}
		qa, sa := quantize(a)
		qb, sb := quantize(b)
		est := float64(sa) * float64(sb) * float64(dot(qa, qb))
		worst = max(worst, math.Abs(est-cos(a, b)))
	}
	if worst >= 0.02 {
		t.Fatalf("worst cosine error %.4f", worst)
	}
	if q, s := quantize(make([]float32, 8)); s != 0 || q[0] != 0 {
		t.Fatal("zero vector")
	}
}

func TestHybridOrdering(t *testing.T) {
	ix := newIndex(1)
	texts := []string{
		"refund policy: customers get money back within 30 days",
		"travel booking portal for flights",
		"error code XJ-42 means the license expired",
		"refund refund refund status page",
	}
	for i, txt := range texts {
		ix.docs = append(ix.docs, docInfo{id: fmt.Sprint(i), attrs: map[string]string{"team": []string{"a", "b"}[i%2]}})
		q, s := quantize(hashEmbed(txt, 64))
		ix.add(uint32(i), 0, q, s, txt)
	}
	ix.finish()
	// a pure keyword (BM25) query: the rare code wins although its vector is unrelated
	res := ix.search(hashEmbed("zzz", 64), "what is XJ-42", 3, 0.99, nil)
	if len(res) != 1 || res[0].chunk != 2 || res[0].cos != 0 {
		t.Fatalf("keyword leg: %+v", res)
	}
	// both legs agree on chunk 0 for a semantic query; fused first
	res = ix.search(hashEmbed("refund policy customers money back within 30 days", 64), "refund policy money back", 2, 0.1, nil)
	if len(res) == 0 || res[0].chunk != 0 || res[0].cos < 0.5 {
		t.Fatalf("fused: %+v", res)
	}
	// filters apply before ranking
	res = ix.search(hashEmbed("refund", 64), "refund", 4, -1, map[string][]string{"team": {"b"}})
	for _, r := range res {
		if r.chunk%2 != 1 {
			t.Fatalf("filter let chunk %d through", r.chunk)
		}
	}
}

func TestServiceIngestSearchDelete(t *testing.T) {
	s, emb, st, org := newTestService(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.Start(ctx)

	k := &store.KB{OrgID: org, Name: "Handbook", EmbedModel: "emb-a", Dimensions: 128, ChunkChars: 300, OverlapChars: 20}
	if err := s.CreateKB(ctx, k); err != nil {
		t.Fatal(err)
	}
	if k.TopK != DefaultTopK || k.MinScore != DefaultMinScore || k.MaxContextTokens != DefaultMaxContextTokens {
		t.Fatalf("defaults %+v", k)
	}
	if err := s.CreateKB(ctx, &store.KB{OrgID: org, Name: "x", EmbedModel: "m", ChunkChars: 100}); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("bad chunk_chars: %v", err)
	}
	sig1, _ := s.Sig(ctx, org, []string{k.ID})

	d, err := s.AddDoc(ctx, org, k.ID, DocInput{Filename: "handbook.md", Data: []byte(handbook), KeyID: "key1",
		Attributes: map[string]string{"lang": "en"}})
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.AddDoc(ctx, org, k.ID, DocInput{Text: "The cafeteria serves lunch from noon until two o'clock daily.",
		Attributes: map[string]string{"lang": "de"}})
	if err != nil {
		t.Fatal(err)
	}
	if d.Status != store.KBQueued || d.ContentType != "text/markdown" || other.Source != "text" {
		t.Fatalf("added %+v %+v", d, other)
	}
	got := waitStatus(t, s, org, k.ID, d.ID, store.KBReady)
	waitStatus(t, s, org, k.ID, other.ID, store.KBReady)
	if got.Chunks != 4 || got.EmbedModel != "emb-a" || got.Dimensions != 128 || got.InTokens == 0 {
		t.Fatalf("ready doc %+v", got)
	}
	if sig2, _ := s.Sig(ctx, org, []string{k.ID}); sig2 == sig1 {
		t.Fatalf("sig did not change: %s", sig2)
	}

	hits, err := s.Search(ctx, org, []string{k.ID}, "How do customers get a refund?", "key2", SearchOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 || hits[0].Section != "Handbook > Billing > Refunds" || hits[0].Filename != "handbook.md" ||
		!strings.Contains(hits[0].Text, "30 days") || hits[0].Score <= 0 {
		t.Fatalf("hits %+v", hits)
	}
	if emb.keys[len(emb.keys)-1] != "key2" || emb.keys[0] != "key1" && emb.keys[0] != "" {
		t.Fatalf("billing keys %v", emb.keys)
	}
	hits, _ = s.Search(ctx, org, []string{k.ID}, "lunch cafeteria", "", SearchOpts{Filter: map[string][]string{"lang": {"en"}}})
	for _, h := range hits {
		if h.Doc == other.ID {
			t.Fatal("filter ignored")
		}
	}
	hits, _ = s.Search(ctx, org, []string{k.ID}, "refund invoices travel", "", SearchOpts{TopK: 10, MaxContextTokens: 30})
	if len(hits) != 1 {
		t.Fatalf("context budget: %d hits", len(hits))
	}
	if hits, err := s.Search(ctx, org, []string{k.ID}, "hi", "", SearchOpts{}); err != nil || hits != nil {
		t.Fatalf("short query %v %v", hits, err)
	}
	if _, err := s.Search(ctx, "other-org", []string{k.ID}, "refund", "", SearchOpts{}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("cross-org: %v", err)
	}

	// re-embed with another model: rev bump hides old vectors, docs come back ready with the new model
	if _, err := s.UpdateKB(ctx, org, k.ID, func(k *store.KB) error { k.EmbedModel = "emb-b"; k.Rev = 99; return nil }); err != nil {
		t.Fatal(err)
	}
	if k2, _ := s.GetKB(ctx, org, k.ID); k2.Rev >= 99 {
		t.Fatalf("rev settable: %d", k2.Rev)
	}
	got = waitStatus(t, s, org, k.ID, d.ID, store.KBReady)
	for got.EmbedModel != "emb-b" {
		time.Sleep(5 * time.Millisecond)
		got = waitStatus(t, s, org, k.ID, d.ID, store.KBReady)
	}
	hits, _ = s.Search(ctx, org, []string{k.ID}, "refund within 30 days", "", SearchOpts{})
	if len(hits) == 0 || hits[0].Section != "Handbook > Billing > Refunds" {
		t.Fatalf("after re-embed %+v", hits)
	}

	// deletes leave no keys
	if err := s.DeleteDoc(ctx, org, k.ID, d.ID); err != nil {
		t.Fatal(err)
	}
	if items, _ := st.KV.Scan(ctx, store.KBChunkPrefix(org, k.ID, d.ID), "", 0); len(items) != 0 {
		t.Fatalf("doc chunks left: %d", len(items))
	}
	if err := s.DeleteKB(ctx, org, k.ID); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"kb/", "kbdoc/", "kbsrc/", "kbchunk/", "kbq/"} {
		if items, _ := st.KV.Scan(ctx, p, "", 0); len(items) != 0 {
			t.Fatalf("%s left %d keys", p, len(items))
		}
	}
}

func TestWorkerRetriesAndFails(t *testing.T) {
	s, emb, st, org := newTestService(t)
	ctx := context.Background()
	k := &store.KB{OrgID: org, Name: "kb", EmbedModel: "m"}
	if err := s.CreateKB(ctx, k); err != nil {
		t.Fatal(err)
	}
	// an expired lease of a dead worker is retried by a live one
	d, _ := s.AddDoc(ctx, org, k.ID, DocInput{Text: "Some text that is long enough to become a chunk."})
	if _, _, err := st.ClaimKBJob(ctx, d.ID, "dead", time.Millisecond, maxAttempts); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Millisecond)
	s.drain(ctx, "live")
	if got, _ := s.GetDoc(ctx, org, k.ID, d.ID); got.Status != store.KBReady || got.Attempts != 2 {
		t.Fatalf("lease retry: %+v", got)
	}

	// transient errors: 3 attempts, then failed with a safe message
	emb.setErr(errors.New("upstream said: secret body"))
	d2, _ := s.AddDoc(ctx, org, k.ID, DocInput{Text: "Another text that is long enough to become a chunk."})
	for range 4 {
		s.drain(ctx, "w")
	}
	got, _ := s.GetDoc(ctx, org, k.ID, d2.ID)
	if got.Status != store.KBFailed || got.Error != "embedding failed" || got.Attempts != maxAttempts {
		t.Fatalf("failed doc %+v", got)
	}

	// insufficient credits fail at once
	emb.setErr(fmt.Errorf("org: %w", ErrInsufficientCredits))
	before := emb.calls.Load()
	d3, _ := s.AddDoc(ctx, org, k.ID, DocInput{Text: "A third text that is long enough to become a chunk."})
	s.drain(ctx, "w")
	got, _ = s.GetDoc(ctx, org, k.ID, d3.ID)
	if got.Status != store.KBFailed || got.Error != "insufficient credits" || emb.calls.Load()-before != 1 {
		t.Fatalf("credits %+v", got)
	}
	if jobs, _ := st.ListKBJobs(ctx, 0); len(jobs) != 0 {
		t.Fatalf("queue %+v", jobs)
	}
	// reindex after fixing credits
	emb.setErr(nil)
	if err := s.Reindex(ctx, org, k.ID); err != nil {
		t.Fatal(err)
	}
	s.drain(ctx, "w")
	if got, _ := s.GetDoc(ctx, org, k.ID, d3.ID); got.Status != store.KBReady {
		t.Fatalf("reindex %+v", got)
	}
}

func TestAddDocValidation(t *testing.T) {
	s, _, _, org := newTestService(t)
	ctx := context.Background()
	k := &store.KB{OrgID: org, Name: "kb", EmbedModel: "m"}
	if err := s.CreateKB(ctx, k); err != nil {
		t.Fatal(err)
	}
	for name, in := range map[string]DocInput{
		"none":  {},
		"two":   {Text: "a", URL: "http://x"},
		"attrs": {Text: "hello", Attributes: map[string]string{"": "x"}},
		"empty": {Data: []byte("   "), Filename: "a.txt"},
	} {
		if _, err := s.AddDoc(ctx, org, k.ID, in); !errors.Is(err, store.ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := s.AddDoc(ctx, org, k.ID, DocInput{Data: []byte("%PDF"), Filename: "a.pdf"}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("pdf: %v", err)
	}
	if _, err := s.AddDoc(ctx, org, k.ID, DocInput{URL: "http://example.com"}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("url without config: %v", err)
	}
	d, err := s.AddDoc(ctx, org, k.ID, DocInput{Data: []byte("hi there"), Filename: "../../etc/pa\x01ss.txt"})
	if err != nil || d.Filename != "pass.txt" || d.Source != "upload" {
		t.Fatalf("filename %+v %v", d, err)
	}
	if _, err := s.AddDoc(ctx, org, "vs_000000000000000000000000", DocInput{Text: "x"}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing kb: %v", err)
	}
	if _, err := s.Search(ctx, org, []string{k.ID, "a", "b", "c"}, "query", "", SearchOpts{}); !errors.Is(err, ErrLimit) {
		t.Fatalf("too many kbs: %v", err)
	}
}

func TestFetchURL(t *testing.T) {
	s, _, _, org := newTestService(t)
	ctx := context.Background()
	k := &store.KB{OrgID: org, Name: "kb", EmbedModel: "m"}
	_ = s.CreateKB(ctx, k)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/page.html":
			if r.UserAgent() != "Polyrail" {
				t.Errorf("ua %q", r.UserAgent())
			}
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, "<h1>Title</h1><p>Body text here.</p>")
		case "/loop":
			http.Redirect(w, r, "/loop", http.StatusFound)
		case "/big":
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte(strings.Repeat("a", fetchMaxBytes+10)))
		case "/bin":
			w.Header().Set("Content-Type", "application/zip")
			fmt.Fprint(w, "PK")
		case "/private":
			http.Redirect(w, r, "http://blocked.example/x", http.StatusFound)
		default:
			http.Error(w, "internal secret", 500)
		}
	}))
	defer srv.Close()
	var checked []string
	s.cfg.CheckURL = func(raw string) error {
		checked = append(checked, raw)
		if strings.Contains(raw, "blocked") {
			return errors.New("address is not publicly routable")
		}
		return nil
	}
	s.cfg.DialContext = (&net.Dialer{}).DialContext

	d, err := s.AddDoc(ctx, org, k.ID, DocInput{URL: srv.URL + "/page.html"})
	if err != nil || d.Filename != "page.html" || d.Source != "url" || d.ContentType != "text/html" {
		t.Fatalf("page %+v %v", d, err)
	}
	for path, want := range map[string]error{"/loop": store.ErrInvalid, "/big": ErrLimit, "/bin": ErrUnsupported,
		"/private": store.ErrInvalid, "/500": store.ErrInvalid} {
		_, err := s.AddDoc(ctx, org, k.ID, DocInput{URL: srv.URL + path})
		if !errors.Is(err, want) || strings.Contains(fmt.Sprint(err), "secret") {
			t.Errorf("%s: %v", path, err)
		}
	}
	if !strings.Contains(strings.Join(checked, " "), "blocked.example") {
		t.Fatalf("redirect not re-checked: %v", checked)
	}
	if _, err := s.AddDoc(ctx, org, k.ID, DocInput{URL: "http://blocked.example/"}); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("blocked: %v", err)
	}
}

func TestIndexCacheEvictionAndRevReload(t *testing.T) {
	loads := map[string]int{}
	var mu sync.Mutex
	rev := map[string]int64{"o/a": 1, "o/b": 1, "o/c": 1}
	c := newIndexCache(1500, func(_ context.Context, org, kb string) (*index, error) {
		mu.Lock()
		defer mu.Unlock()
		loads[org+"/"+kb]++
		ix := newIndex(rev[org+"/"+kb])
		ix.size = 1000
		return ix, nil
	})
	ctx := context.Background()
	get := func(kb string, r int64) {
		if _, err := c.get(ctx, "o", kb, r); err != nil {
			t.Fatal(err)
		}
	}
	get("a", 1)
	get("a", 1)
	get("b", 1) // over budget: a evicted
	get("a", 1)
	if loads["o/a"] != 2 || loads["o/b"] != 1 || c.used != 1000 {
		t.Fatalf("loads %v used %d", loads, c.used)
	}
	rev["o/a"] = 2
	get("a", 2) // rev change reloads
	get("a", 1) // older rev is served by the newer index
	if loads["o/a"] != 3 {
		t.Fatalf("rev reload: %v", loads)
	}
	// concurrent cold loads share one
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() { defer wg.Done(); get("c", 1) }()
	}
	wg.Wait()
	if loads["o/c"] != 1 {
		t.Fatalf("singleflight: %d loads", loads["o/c"])
	}
}

// BenchmarkSearch100k measures one hybrid search over 100k chunks of 512 dims.
func BenchmarkSearch100k(b *testing.B) {
	const n, dims = 100_000, 512
	r := rand.New(rand.NewPCG(3, 4))
	words := strings.Fields("alpha beta gamma delta epsilon zeta eta theta iota kappa lambda mu nu xi omicron pi rho sigma tau upsilon phi chi psi omega refund invoice travel policy portal license error")
	ix := newIndex(1)
	ix.docs = []docInfo{{id: "d"}}
	vec := make([]float32, dims)
	for i := range n {
		for j := range vec {
			vec[j] = float32(r.NormFloat64())
		}
		q, s := quantize(vec)
		var sb strings.Builder
		for range 250 { // ~1600 chars of text
			sb.WriteString(words[r.IntN(len(words))] + fmt.Sprint(r.IntN(400)) + " ")
		}
		ix.add(0, i, q, s, sb.String())
	}
	ix.finish()
	for j := range vec {
		vec[j] = float32(r.NormFloat64())
	}
	for b.Loop() {
		ix.search(vec, "refund policy for travel invoices", 5, 0.0, nil)
	}
	b.ReportMetric(float64(ix.size)/n, "bytes/chunk")
}

// M14: a 2 MB run without sentence ends used to be hard-cut by re-counting the remaining text on
// every cut (quadratic: seconds of worker CPU per document).
func TestChunkerHardCutIsLinear(t *testing.T) {
	text := strings.Repeat("é", 1_000_000) // 2 MB, no break
	start := time.Now()
	cs := chunkText(text, 200, 0)
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("hard cut took %v", d)
	}
	if len(cs) != 1_000_000/200 {
		t.Fatalf("%d chunks", len(cs))
	}
	for _, c := range cs {
		if n := len([]rune(c.Text)); n != 200 {
			t.Fatalf("chunk of %d runes", n)
		}
	}
	if got := units("ab cdefgh  ijk", 3); strings.Join(got, "|") != "ab |cde|fgh|ijk" {
		t.Fatalf("units %q", got)
	}
}

// M14: the worker served the queue FIFO across orgs from a window of the 1,000 newest entries,
// so one org's backlog (or orgs past the window) starved everyone else.
func TestFairQueueOrder(t *testing.T) {
	// ListKBJobs order: newest doc first
	jobs := []store.KBJob{{OrgID: "b", DocID: "6"}, {OrgID: "a", DocID: "5"}, {OrgID: "a", DocID: "4"},
		{OrgID: "a", DocID: "3"}, {OrgID: "c", DocID: "2"}, {OrgID: "a", DocID: "1"}}
	var got []string
	for _, j := range fairOrder(jobs) {
		got = append(got, j.OrgID+j.DocID)
	}
	if strings.Join(got, ",") != "a1,c2,b6,a3,a4,a5" {
		t.Fatalf("order %v", got)
	}
}

type wideEmb struct{ fakeEmb }

func (w *wideEmb) Embed(_ context.Context, _, _, _ string, _ int, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range out {
		out[i] = make([]float32, MaxDimensions+1)
		out[i][0] = 1
	}
	return out, nil
}

// M13: the index byte budget (KB_INDEX_MB) was not enforced: an index larger than the budget was
// loaded and kept, and vectors of any length (a tenant's own embedding endpoint) were stored.
func TestIndexBudgetEnforced(t *testing.T) {
	c := newIndexCache(1500, func(_ context.Context, org, kb string) (*index, error) {
		ix := newIndex(1)
		ix.size = 2000
		return ix, nil
	})
	if _, err := c.get(context.Background(), "o", "big", 1); !errors.Is(err, ErrLimit) {
		t.Fatalf("over-budget index: %v", err)
	}
	if c.used != 0 || c.ll.Len() != 0 {
		t.Fatalf("over-budget index cached: %d", c.used)
	}

	// the loader stops early instead of materializing an index past the budget
	s, _, st, org := newTestService(t)
	ctx := context.Background()
	s.vs = NewKVStore(st, 4096)
	k := &store.KB{OrgID: org, Name: "kb", EmbedModel: "m", Dimensions: 512}
	if err := s.CreateKB(ctx, k); err != nil {
		t.Fatal(err)
	}
	d, _ := s.AddDoc(ctx, org, k.ID, DocInput{Text: strings.Repeat("Refund policy text for the budget test. ", 400)})
	s.drain(ctx, "w")
	waitStatus(t, s, org, k.ID, d.ID, store.KBReady)
	if _, err := s.Search(ctx, org, []string{k.ID}, "refund policy", "", SearchOpts{}); !errors.Is(err, ErrLimit) {
		t.Fatalf("search over budget: %v", err)
	}

	// vectors wider than MaxDimensions fail the document
	w := &wideEmb{}
	s.emb = w
	s.vs = NewKVStore(st, 1<<20)
	d2, _ := s.AddDoc(ctx, org, k.ID, DocInput{Text: "Some text that is long enough to become a chunk."})
	s.drain(ctx, "w")
	if got := waitStatus(t, s, org, k.ID, d2.ID, store.KBFailed); got.Error != "embedding has too many dimensions" {
		t.Fatalf("wide vectors: %+v", got)
	}
}
