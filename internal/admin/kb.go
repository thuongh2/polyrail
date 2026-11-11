package admin

// Knowledge-base console API (docs/KB.md §5): viewers read, members search (billed to the org),
// admins manage KBs and documents. Every mutation is audited.

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"

	"github.com/polyrail/polyrail/internal/kb"
	"github.com/polyrail/polyrail/internal/store"
)

func (a *API) registerKB(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/orgs/{org}/kbs", a.org(store.RoleViewer, a.kb(a.listKBs)))
	mux.HandleFunc("POST /api/orgs/{org}/kbs", a.org(store.RoleAdmin, a.kb(a.createKB)))
	mux.HandleFunc("GET /api/orgs/{org}/kbs/{kb}", a.org(store.RoleViewer, a.kb(a.getKB)))
	mux.HandleFunc("PATCH /api/orgs/{org}/kbs/{kb}", a.org(store.RoleAdmin, a.kb(a.patchKB)))
	mux.HandleFunc("DELETE /api/orgs/{org}/kbs/{kb}", a.org(store.RoleAdmin, a.kb(a.deleteKB)))
	mux.HandleFunc("POST /api/orgs/{org}/kbs/{kb}/reindex", a.org(store.RoleAdmin, a.kb(a.reindexKB)))
	mux.HandleFunc("POST /api/orgs/{org}/kbs/{kb}/search", a.org(store.RoleMember, a.kb(a.searchKB)))
	mux.HandleFunc("GET /api/orgs/{org}/kbs/{kb}/docs", a.org(store.RoleViewer, a.kb(a.listKBDocs)))
	mux.HandleFunc("POST /api/orgs/{org}/kbs/{kb}/docs", a.org(store.RoleAdmin, a.kb(a.addKBDoc)))
	mux.HandleFunc("GET /api/orgs/{org}/kbs/{kb}/docs/{doc}", a.org(store.RoleViewer, a.kb(a.getKBDoc)))
	mux.HandleFunc("DELETE /api/orgs/{org}/kbs/{kb}/docs/{doc}", a.org(store.RoleAdmin, a.kb(a.deleteKBDoc)))
}

// kb answers 404 when the server runs without the knowledge-base engine.
func (a *API) kb(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if a.G.KB == nil {
			writeErr(w, http.StatusNotFound, "knowledge bases are not enabled")
			return
		}
		h(w, r)
	}
}

// kbFail maps knowledge-base errors on top of fail.
func kbFail(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, kb.ErrLimit):
		writeErr(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, kb.ErrUnsupported):
		writeErr(w, http.StatusUnsupportedMediaType, err.Error())
	case errors.Is(err, kb.ErrInsufficientCredits):
		writeErr(w, http.StatusPaymentRequired, err.Error())
	default:
		fail(w, err)
	}
}

// kbInput is the body of POST/PATCH …/kbs; absent fields keep their value (PATCH) or take the
// default (POST).
type kbInput struct {
	Name             *string  `json:"name"`
	Description      *string  `json:"description"`
	EmbedModel       *string  `json:"embedding_model"`
	Dimensions       *int     `json:"dimensions"`
	ChunkChars       *int     `json:"chunk_chars"`
	OverlapChars     *int     `json:"overlap_chars"`
	TopK             *int     `json:"top_k"`
	MinScore         *float64 `json:"min_score"`
	MaxContextTokens *int     `json:"max_context_tokens"`
}

func (in kbInput) apply(k *store.KB) {
	set := func(dst *int, v *int) {
		if v != nil {
			*dst = *v
		}
	}
	if in.Name != nil {
		k.Name = *in.Name
	}
	if in.Description != nil {
		k.Description = *in.Description
	}
	if in.EmbedModel != nil {
		k.EmbedModel = *in.EmbedModel
	}
	set(&k.Dimensions, in.Dimensions)
	set(&k.ChunkChars, in.ChunkChars)
	set(&k.OverlapChars, in.OverlapChars)
	set(&k.TopK, in.TopK)
	set(&k.MaxContextTokens, in.MaxContextTokens)
	if in.MinScore != nil {
		k.MinScore = *in.MinScore
	}
}

// checkEmbedModel requires an enabled embedding model visible to the org.
func (a *API) checkEmbedModel(org, id string) error {
	m, ok := a.G.Snapshot().Model(org, id)
	if !ok {
		return fmt.Errorf("%w: embedding model %q not found", store.ErrInvalid, id)
	}
	if m.Kind != "embedding" {
		return fmt.Errorf("%w: model %q is a %s model, not an embedding model", store.ErrInvalid, id, m.Kind)
	}
	return nil
}

func (a *API) listKBs(w http.ResponseWriter, r *http.Request) {
	ks, err := a.G.KB.ListKBs(r.Context(), r.PathValue("org"))
	if err != nil {
		kbFail(w, err)
		return
	}
	writeJSON(w, 200, ks)
}

func (a *API) createKB(w http.ResponseWriter, r *http.Request) {
	var in kbInput
	if !decode(w, r, &in) {
		return
	}
	org := r.PathValue("org")
	k := &store.KB{OrgID: org}
	in.apply(k)
	if err := a.checkEmbedModel(org, k.EmbedModel); err != nil {
		kbFail(w, err)
		return
	}
	if err := a.G.KB.CreateKB(r.Context(), k); err != nil {
		kbFail(w, err)
		return
	}
	a.audit(r, org, "kb.create", k.ID, k.Name+" ("+k.EmbedModel+")")
	writeJSON(w, http.StatusCreated, k)
}

func (a *API) getKB(w http.ResponseWriter, r *http.Request) {
	k, err := a.G.KB.GetKB(r.Context(), r.PathValue("org"), r.PathValue("kb"))
	if err != nil {
		kbFail(w, err)
		return
	}
	writeJSON(w, 200, k)
}

func (a *API) patchKB(w http.ResponseWriter, r *http.Request) {
	var in kbInput
	if !decode(w, r, &in) {
		return
	}
	org, id := r.PathValue("org"), r.PathValue("kb")
	if in.EmbedModel != nil {
		if err := a.checkEmbedModel(org, *in.EmbedModel); err != nil {
			kbFail(w, err)
			return
		}
	}
	k, err := a.G.KB.UpdateKB(r.Context(), org, id, func(k *store.KB) error { in.apply(k); return nil })
	if err != nil {
		kbFail(w, err)
		return
	}
	a.audit(r, org, "kb.update", id, k.Name)
	writeJSON(w, 200, k)
}

func (a *API) deleteKB(w http.ResponseWriter, r *http.Request) {
	org, id := r.PathValue("org"), r.PathValue("kb")
	if err := a.G.KB.DeleteKB(r.Context(), org, id); err != nil {
		kbFail(w, err)
		return
	}
	a.audit(r, org, "kb.delete", id, "")
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) reindexKB(w http.ResponseWriter, r *http.Request) {
	org, id := r.PathValue("org"), r.PathValue("kb")
	if err := a.G.KB.Reindex(r.Context(), org, id); err != nil {
		kbFail(w, err)
		return
	}
	a.audit(r, org, "kb.reindex", id, "")
	w.WriteHeader(http.StatusNoContent)
}

// searchKB: POST …/kbs/{kb}/search {query, top_k?, filters?: {attr: value | [values]}}. The query
// embedding is billed to the org (console, no key). Returns hits as a bare array.
func (a *API) searchKB(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Query   string                     `json:"query"`
		TopK    int                        `json:"top_k"`
		Filters map[string]json.RawMessage `json:"filters"`
	}
	if !decode(w, r, &in) {
		return
	}
	filter := map[string][]string{}
	for k, raw := range in.Filters {
		var one any
		if err := json.Unmarshal(raw, &one); err != nil {
			writeErr(w, http.StatusBadRequest, "filters must map attribute names to a value or a list of values")
			return
		}
		vals, ok := one.([]any)
		if !ok {
			vals = []any{one}
		}
		for _, v := range vals {
			s, ok := kb.FilterValue(v)
			if !ok {
				writeErr(w, http.StatusBadRequest, "filter values must be strings, numbers or booleans")
				return
			}
			filter[k] = append(filter[k], s)
		}
	}
	hits, err := a.G.KB.Search(r.Context(), r.PathValue("org"), []string{r.PathValue("kb")}, in.Query, "",
		kb.SearchOpts{TopK: in.TopK, Filter: filter, MaxContextTokens: 1 << 20})
	if err != nil {
		kbFail(w, err)
		return
	}
	if hits == nil {
		hits = []kb.Hit{}
	}
	writeJSON(w, 200, hits)
}

func (a *API) listKBDocs(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	docs, err := a.G.KB.ListDocs(r.Context(), r.PathValue("org"), r.PathValue("kb"), r.URL.Query().Get("after"), limit)
	if err != nil {
		kbFail(w, err)
		return
	}
	if docs == nil {
		docs = []store.KBDoc{}
	}
	writeJSON(w, 200, docs)
}

// addKBDoc: multipart "file" (+ "attributes" JSON) or JSON {filename, text} / {url} → 202 with
// the queued doc. Embeddings are billed to the org (console).
func (a *API) addKBDoc(w http.ResponseWriter, r *http.Request) {
	in, fileID, err := a.G.KB.ReadDocInput(w, r)
	if err != nil {
		kbFail(w, err)
		return
	}
	if fileID != "" {
		writeErr(w, http.StatusBadRequest, "file_id is not supported: upload the file directly")
		return
	}
	org, id := r.PathValue("org"), r.PathValue("kb")
	d, err := a.G.KB.AddDoc(r.Context(), org, id, in)
	if err != nil {
		kbFail(w, err)
		return
	}
	a.audit(r, org, "kb.doc.add", d.ID, id+" "+d.Filename)
	writeJSON(w, http.StatusAccepted, d)
}

func (a *API) getKBDoc(w http.ResponseWriter, r *http.Request) {
	d, err := a.G.KB.GetDoc(r.Context(), r.PathValue("org"), r.PathValue("kb"), r.PathValue("doc"))
	if err != nil {
		kbFail(w, err)
		return
	}
	writeJSON(w, 200, d)
}

func (a *API) deleteKBDoc(w http.ResponseWriter, r *http.Request) {
	org, id, doc := r.PathValue("org"), r.PathValue("kb"), r.PathValue("doc")
	if err := a.G.KB.DeleteDoc(r.Context(), org, id, doc); err != nil {
		kbFail(w, err)
		return
	}
	a.audit(r, org, "kb.doc.delete", doc, id)
	w.WriteHeader(http.StatusNoContent)
}

// checkKBIDs validates a key's kb_ids: distinct KBs of the org, at most kb.MaxKBsPerRequest.
func (a *API) checkKBIDs(r *http.Request, org string, ids []string) ([]string, error) {
	var out []string
	for _, id := range ids {
		if !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	if len(out) > kb.MaxKBsPerRequest {
		return nil, fmt.Errorf("%w: a key uses at most %d knowledge bases", store.ErrInvalid, kb.MaxKBsPerRequest)
	}
	if len(out) > 0 && a.G.KB == nil {
		return nil, fmt.Errorf("%w: knowledge bases are not enabled", store.ErrInvalid)
	}
	for _, id := range out {
		if _, err := a.G.KB.GetKB(r.Context(), org, id); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return nil, fmt.Errorf("%w: knowledge base %q not found", store.ErrInvalid, id)
			}
			return nil, err
		}
	}
	return out, nil
}

// keyKBs validates kb_ids of a key create/update (admins only); false means the error was written.
func (a *API) keyKBs(w http.ResponseWriter, r *http.Request, in *keyInput) bool {
	if in.KBIDs == nil {
		return true
	}
	if role(r).Rank() < store.RoleAdmin.Rank() {
		fail(w, errForbidden)
		return false
	}
	ids, err := a.checkKBIDs(r, r.PathValue("org"), *in.KBIDs)
	if err != nil {
		fail(w, err)
		return false
	}
	in.KBIDs = &ids
	return true
}
