package gateway

// OpenAI-compatible /v1/vector_stores subset over the knowledge-base engine (docs/KB.md §5).
// Any key of the org may list, read and search; a key mutates only the stores it created
// (console-created stores are managed in the console). Files are uploaded here directly.

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strconv"

	"github.com/polyrail/polyrail/internal/kb"
	"github.com/polyrail/polyrail/internal/store"
)

// errEmbed marks embedding failures; their message is safe to show (no upstream body).
var errEmbed = errors.New("embedding failed")

// kbError maps a kb.Service error to an API error.
func kbError(err error) *apiError {
	switch {
	case errors.Is(err, store.ErrNotFound):
		return errf(404, "invalid_request_error", "not_found", "not found")
	case errors.Is(err, store.ErrInvalid):
		return errf(400, "invalid_request_error", "invalid_request", "%v", err)
	case errors.Is(err, kb.ErrLimit):
		return errf(400, "invalid_request_error", "limit_exceeded", "%v", err)
	case errors.Is(err, kb.ErrUnsupported):
		return errf(415, "invalid_request_error", "unsupported_media_type", "%v", err)
	case errors.Is(err, kb.ErrInsufficientCredits):
		return errf(402, "insufficient_quota", "insufficient_credits", "%v", err)
	case errors.Is(err, errEmbed):
		return errf(502, "upstream_error", "provider_error", "%v", err)
	}
	slog.Error("vector store", "err", err)
	return errf(500, "api_error", "internal", "internal error")
}

func writeV1(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// vsAuth authenticates and admits a vector-store call.
func (g *Gateway) vsAuth(w http.ResponseWriter, r *http.Request) *principal {
	p, e := g.authenticate(r.Context(), r)
	if e == nil && g.KB == nil {
		e = errf(404, "invalid_request_error", "not_found", "vector stores are not enabled")
	}
	if e == nil {
		e = g.admit(r.Context(), p)
	}
	if e == nil && r.PathValue("id") != "" { // a vector store of another workspace does not exist for this key (D31)
		if k, err := g.KB.GetKB(r.Context(), p.Org.ID, r.PathValue("id")); err == nil && store.WSID(k.WorkspaceID) != p.ws() {
			e = kbError(store.ErrNotFound)
		}
	}
	if e != nil {
		writeError(w, e)
		return nil
	}
	return p
}

// vsOwned loads {id} and checks the key created it (for mutations).
func (g *Gateway) vsOwned(w http.ResponseWriter, r *http.Request, p *principal) *store.KB {
	k, err := g.KB.GetKB(r.Context(), p.Org.ID, r.PathValue("id"))
	if err != nil {
		writeError(w, kbError(err))
		return nil
	}
	if k.CreatedByKey != p.Key.ID {
		writeError(w, errf(403, "permission_error", "not_owner", "this API key did not create this vector store"))
		return nil
	}
	return k
}

type vsCounts struct {
	InProgress int `json:"in_progress"`
	Completed  int `json:"completed"`
	Failed     int `json:"failed"`
	Cancelled  int `json:"cancelled"`
	Total      int `json:"total"`
}

type vsObject struct {
	ID             string            `json:"id"`
	Object         string            `json:"object"`
	CreatedAt      int64             `json:"created_at"`
	Name           string            `json:"name"`
	UsageBytes     int64             `json:"usage_bytes"`
	FileCounts     vsCounts          `json:"file_counts"`
	Status         string            `json:"status"`
	ExpiresAfter   any               `json:"expires_after"`
	ExpiresAt      any               `json:"expires_at"`
	LastActiveAt   int64             `json:"last_active_at"`
	Metadata       map[string]string `json:"metadata"`
	EmbeddingModel string            `json:"embedding_model"`
	Dimensions     int               `json:"dimensions,omitempty"`
}

// vsOf renders a KB; its file counts come from the doc list.
// shortcut: counts scan the KB's docs (≤5,000); keep per-status counters on the KB if this shows up.
func (g *Gateway) vsOf(r *http.Request, k *store.KB) (vsObject, error) {
	docs, err := g.KB.ListDocs(r.Context(), k.OrgID, k.ID, "", 0)
	if err != nil {
		return vsObject{}, err
	}
	o := vsObject{ID: k.ID, Object: "vector_store", CreatedAt: k.CreatedAt.Unix(), Name: k.Name, UsageBytes: k.Bytes,
		Status: "completed", LastActiveAt: k.UpdatedAt.Unix(), Metadata: k.Metadata, EmbeddingModel: k.EmbedModel, Dimensions: k.Dimensions}
	if o.Metadata == nil {
		o.Metadata = map[string]string{}
	}
	for _, d := range docs {
		switch fileStatus(d.Status) {
		case "completed":
			o.FileCounts.Completed++
		case "failed":
			o.FileCounts.Failed++
		default:
			o.FileCounts.InProgress++
			o.Status = "in_progress"
		}
	}
	o.FileCounts.Total = len(docs)
	return o, nil
}

func fileStatus(s string) string {
	switch s {
	case store.KBReady:
		return "completed"
	case store.KBFailed:
		return "failed"
	}
	return "in_progress"
}

type vsFile struct {
	ID            string            `json:"id"`
	Object        string            `json:"object"`
	CreatedAt     int64             `json:"created_at"`
	VectorStoreID string            `json:"vector_store_id"`
	Status        string            `json:"status"`
	LastError     any               `json:"last_error"`
	UsageBytes    int64             `json:"usage_bytes"`
	Attributes    map[string]string `json:"attributes"`
	Filename      string            `json:"filename"`
}

func fileOf(d *store.KBDoc) vsFile {
	f := vsFile{ID: d.ID, Object: "vector_store.file", CreatedAt: d.CreatedAt.Unix(), VectorStoreID: d.KBID,
		Status: fileStatus(d.Status), UsageBytes: d.Bytes, Attributes: d.Attributes, Filename: d.Filename}
	if f.Attributes == nil {
		f.Attributes = map[string]string{}
	}
	if d.Status == store.KBFailed {
		f.LastError = map[string]string{"code": "server_error", "message": d.Error}
	}
	return f
}

// CreateVectorStore: POST /v1/vector_stores {name, embedding_model, dimensions?, chunking_strategy?, metadata?}.
func (g *Gateway) CreateVectorStore(w http.ResponseWriter, r *http.Request) {
	p := g.vsAuth(w, r)
	if p == nil {
		return
	}
	var req struct {
		Name             string            `json:"name"`
		EmbeddingModel   string            `json:"embedding_model"`
		Dimensions       int               `json:"dimensions"`
		Metadata         map[string]string `json:"metadata"`
		ChunkingStrategy *struct {
			Type   string `json:"type"`
			Static *struct {
				MaxChunkSizeTokens int `json:"max_chunk_size_tokens"`
				ChunkOverlapTokens int `json:"chunk_overlap_tokens"`
			} `json:"static"`
		} `json:"chunking_strategy"`
		FileIDs []string `json:"file_ids"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		writeError(w, errf(400, "invalid_request_error", "bad_request", "body must be a JSON vector store request"))
		return
	}
	if len(req.FileIDs) > 0 {
		writeError(w, errf(400, "invalid_request_error", "unsupported", "file_ids are not supported: upload the file here directly with POST /v1/vector_stores/{id}/files"))
		return
	}
	if req.EmbeddingModel == "" {
		writeError(w, errf(400, "invalid_request_error", "bad_request", "embedding_model is required (gateway extension: any embedding model of the catalog)"))
		return
	}
	if _, _, e := g.resolve(p, req.EmbeddingModel, "embedding", nil); e != nil {
		writeError(w, e)
		return
	}
	if len(req.Metadata) > 16 {
		writeError(w, errf(400, "invalid_request_error", "bad_request", "metadata holds at most 16 pairs"))
		return
	}
	for k, v := range req.Metadata {
		if len(k) > 64 || len(v) > 512 {
			writeError(w, errf(400, "invalid_request_error", "bad_request", "metadata keys are at most 64 and values 512 characters"))
			return
		}
	}
	if req.Name == "" {
		req.Name = "vector store"
	}
	k := &store.KB{OrgID: p.Org.ID, WorkspaceID: p.ws(), Name: req.Name, EmbedModel: req.EmbeddingModel, Dimensions: req.Dimensions,
		CreatedByKey: p.Key.ID, Metadata: req.Metadata}
	if cs := req.ChunkingStrategy; cs != nil && cs.Type == "static" && cs.Static != nil {
		k.ChunkChars, k.OverlapChars = cs.Static.MaxChunkSizeTokens*4, cs.Static.ChunkOverlapTokens*4
	} else if cs != nil && cs.Type != "auto" {
		writeError(w, errf(400, "invalid_request_error", "bad_request", "chunking_strategy type must be auto or static"))
		return
	}
	if err := g.KB.CreateKB(r.Context(), k); err != nil {
		writeError(w, kbError(err))
		return
	}
	o, err := g.vsOf(r, k)
	if err != nil {
		writeError(w, kbError(err))
		return
	}
	writeV1(w, 200, o)
}

// ListVectorStores: GET /v1/vector_stores (an org has at most 20, returned in one page).
func (g *Gateway) ListVectorStores(w http.ResponseWriter, r *http.Request) {
	p := g.vsAuth(w, r)
	if p == nil {
		return
	}
	ks, err := g.KB.ListKBs(r.Context(), p.Org.ID)
	if err != nil {
		writeError(w, kbError(err))
		return
	}
	ks = slices.DeleteFunc(ks, func(k store.KB) bool { return store.WSID(k.WorkspaceID) != p.ws() })
	data := make([]vsObject, 0, len(ks))
	for i := range ks {
		o, err := g.vsOf(r, &ks[i])
		if err != nil {
			writeError(w, kbError(err))
			return
		}
		data = append(data, o)
	}
	writeV1(w, 200, listPage(data, func(o vsObject) string { return o.ID }, false))
}

func listPage[T any](data []T, id func(T) string, more bool) map[string]any {
	out := map[string]any{"object": "list", "data": data, "first_id": nil, "last_id": nil, "has_more": more}
	if len(data) > 0 {
		out["first_id"], out["last_id"] = id(data[0]), id(data[len(data)-1])
	}
	return out
}

// GetVectorStore: GET /v1/vector_stores/{id}.
func (g *Gateway) GetVectorStore(w http.ResponseWriter, r *http.Request) {
	p := g.vsAuth(w, r)
	if p == nil {
		return
	}
	k, err := g.KB.GetKB(r.Context(), p.Org.ID, r.PathValue("id"))
	if err != nil {
		writeError(w, kbError(err))
		return
	}
	o, err := g.vsOf(r, k)
	if err != nil {
		writeError(w, kbError(err))
		return
	}
	writeV1(w, 200, o)
}

// DeleteVectorStore: DELETE /v1/vector_stores/{id} (creating key only).
func (g *Gateway) DeleteVectorStore(w http.ResponseWriter, r *http.Request) {
	p := g.vsAuth(w, r)
	if p == nil {
		return
	}
	k := g.vsOwned(w, r, p)
	if k == nil {
		return
	}
	if err := g.KB.DeleteKB(r.Context(), p.Org.ID, k.ID); err != nil {
		writeError(w, kbError(err))
		return
	}
	writeV1(w, 200, map[string]any{"id": k.ID, "object": "vector_store.deleted", "deleted": true})
}

// CreateVectorStoreFile: POST /v1/vector_stores/{id}/files, multipart "file" or JSON {text, filename}
// or {url}, with optional attributes. Embedding runs asynchronously (status in_progress).
func (g *Gateway) CreateVectorStoreFile(w http.ResponseWriter, r *http.Request) {
	p := g.vsAuth(w, r)
	if p == nil {
		return
	}
	k := g.vsOwned(w, r, p)
	if k == nil {
		return
	}
	in, fileID, err := g.KB.ReadDocInput(w, r)
	if err != nil {
		writeError(w, kbError(err))
		return
	}
	if fileID != "" {
		writeError(w, errf(400, "invalid_request_error", "unsupported", "file_id is not supported: upload the file here directly (multipart file, or JSON text or url)"))
		return
	}
	in.KeyID = p.Key.ID
	d, err := g.KB.AddDoc(r.Context(), p.Org.ID, k.ID, in)
	if err != nil {
		writeError(w, kbError(err))
		return
	}
	writeV1(w, 200, fileOf(d))
}

// ListVectorStoreFiles: GET /v1/vector_stores/{id}/files?after=&limit= (limit ≤100, default 20).
func (g *Gateway) ListVectorStoreFiles(w http.ResponseWriter, r *http.Request) {
	p := g.vsAuth(w, r)
	if p == nil {
		return
	}
	limit, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil || limit <= 0 {
		limit = 20
	}
	limit = min(limit, 100)
	docs, err := g.KB.ListDocs(r.Context(), p.Org.ID, r.PathValue("id"), r.URL.Query().Get("after"), limit+1)
	if err != nil {
		writeError(w, kbError(err))
		return
	}
	more := len(docs) > limit
	data := make([]vsFile, 0, min(len(docs), limit))
	for i := range docs[:min(len(docs), limit)] {
		data = append(data, fileOf(&docs[i]))
	}
	writeV1(w, 200, listPage(data, func(f vsFile) string { return f.ID }, more))
}

// GetVectorStoreFile: GET /v1/vector_stores/{id}/files/{file_id}.
func (g *Gateway) GetVectorStoreFile(w http.ResponseWriter, r *http.Request) {
	p := g.vsAuth(w, r)
	if p == nil {
		return
	}
	d, err := g.KB.GetDoc(r.Context(), p.Org.ID, r.PathValue("id"), r.PathValue("file_id"))
	if err != nil {
		writeError(w, kbError(err))
		return
	}
	writeV1(w, 200, fileOf(d))
}

// DeleteVectorStoreFile: DELETE /v1/vector_stores/{id}/files/{file_id} (creating key only).
func (g *Gateway) DeleteVectorStoreFile(w http.ResponseWriter, r *http.Request) {
	p := g.vsAuth(w, r)
	if p == nil {
		return
	}
	k := g.vsOwned(w, r, p)
	if k == nil {
		return
	}
	id := r.PathValue("file_id")
	if err := g.KB.DeleteDoc(r.Context(), p.Org.ID, k.ID, id); err != nil {
		writeError(w, kbError(err))
		return
	}
	writeV1(w, 200, map[string]any{"id": id, "object": "vector_store.file.deleted", "deleted": true})
}

// SearchVectorStore: POST /v1/vector_stores/{id}/search {query, max_num_results≤50, filters,
// ranking_options.score_threshold}. The query embedding is billed to the key.
func (g *Gateway) SearchVectorStore(w http.ResponseWriter, r *http.Request) {
	p := g.vsAuth(w, r)
	if p == nil {
		return
	}
	var req struct {
		Query          string          `json:"query"`
		MaxNumResults  int             `json:"max_num_results"`
		Filters        json.RawMessage `json:"filters"`
		RankingOptions *struct {
			ScoreThreshold float64 `json:"score_threshold"`
		} `json:"ranking_options"`
		RewriteQuery bool `json:"rewrite_query"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil || req.Query == "" {
		writeError(w, errf(400, "invalid_request_error", "bad_request", "body must be JSON with a query string"))
		return
	}
	if req.MaxNumResults == 0 {
		req.MaxNumResults = 10
	}
	if req.MaxNumResults < 1 || req.MaxNumResults > kb.MaxSearchResults {
		writeError(w, errf(400, "invalid_request_error", "bad_request", "max_num_results must be 1-%d", kb.MaxSearchResults))
		return
	}
	filter, err := openAIFilter(req.Filters)
	if err != nil {
		writeError(w, errf(400, "invalid_request_error", "bad_request", "%v", err))
		return
	}
	o := kb.SearchOpts{TopK: req.MaxNumResults, Filter: filter, MaxContextTokens: 1 << 20}
	if req.RankingOptions != nil {
		o.MinScore = req.RankingOptions.ScoreThreshold
	}
	ctx := r.Context()
	hits, err := g.KB.Search(ctx, p.Org.ID, []string{r.PathValue("id")}, req.Query, p.Key.ID, o)
	if err != nil {
		writeError(w, kbError(err))
		return
	}
	type content struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	type result struct {
		FileID     string            `json:"file_id"`
		Filename   string            `json:"filename"`
		Score      float64           `json:"score"`
		Attributes map[string]string `json:"attributes"`
		Content    []content         `json:"content"`
	}
	data := make([]result, 0, len(hits))
	for _, h := range hits {
		a := h.Attributes
		if a == nil {
			a = map[string]string{}
		}
		data = append(data, result{FileID: h.Doc, Filename: h.Filename, Score: h.Score, Attributes: a, Content: []content{{"text", h.Text}}})
	}
	writeV1(w, 200, map[string]any{"object": "vector_store.search_results.page", "search_query": req.Query, "data": data, "has_more": false, "next_page": nil})
}

// openAIFilter converts OpenAI attribute filters ({type:"eq"|"in", key, value} or
// {type:"and", filters:[…]}) to kb filters; other operators are rejected.
func openAIFilter(raw json.RawMessage) (map[string][]string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	out := map[string][]string{}
	var walk func(raw json.RawMessage, depth int) error
	walk = func(raw json.RawMessage, depth int) error {
		var f struct {
			Type    string            `json:"type"`
			Key     string            `json:"key"`
			Value   any               `json:"value"`
			Filters []json.RawMessage `json:"filters"`
		}
		if err := json.Unmarshal(raw, &f); err != nil || depth > 4 {
			return errors.New("filters must be an eq/in comparison or an and of them")
		}
		switch f.Type {
		case "and":
			for _, c := range f.Filters {
				if err := walk(c, depth+1); err != nil {
					return err
				}
			}
			return nil
		case "eq", "in":
			if f.Key == "" {
				return errors.New("filter key is required")
			}
			if _, dup := out[f.Key]; dup {
				return errors.New("each filter key may appear once")
			}
			vals, ok := f.Value.([]any)
			if f.Type == "eq" || !ok {
				vals = []any{f.Value}
			}
			for _, v := range vals {
				s, ok := kb.FilterValue(v)
				if !ok {
					return errors.New("filter values must be strings, numbers or booleans")
				}
				out[f.Key] = append(out[f.Key], s)
			}
			return nil
		}
		return errors.New("filter type must be eq, in or and (other operators are not supported)")
	}
	return out, walk(raw, 0)
}
