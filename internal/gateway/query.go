package gateway

import (
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"slices"
	"sort"
	"strings"

	"github.com/polyrail/polyrail/internal/store"
)

// modelQuery is the body of POST /v1/models/query; every filter is optional.
type modelQuery struct {
	Q              string   `json:"q"`
	Kind           string   `json:"kind"`
	Modalities     []string `json:"modalities"`
	MinContext     int      `json:"min_context"`
	MinOutput      int      `json:"min_output"`
	MaxInputPrice  *float64 `json:"max_input_price"`  // USD per 1M tokens
	MaxOutputPrice *float64 `json:"max_output_price"` // USD per 1M tokens
	OwnedBy        string   `json:"owned_by"`
	Exclude        []string `json:"exclude"`
	Sort           string   `json:"sort"` // id (default) | price | context
	Limit          *int     `json:"limit"`
}

func microsPerM(usd float64) int64 { return int64(math.Round(usd * 1e6)) }

func (q *modelQuery) match(m store.Model) bool {
	owner, _, _ := strings.Cut(m.ID, "/")
	if s := strings.ToLower(q.Q); s != "" && !strings.Contains(strings.ToLower(m.ID), s) &&
		!strings.Contains(strings.ToLower(m.Name), s) && !strings.Contains(strings.ToLower(m.Description), s) {
		return false
	}
	for _, mod := range q.Modalities {
		if !slices.Contains(m.Modalities, mod) {
			return false
		}
	}
	return (q.Kind == "" || m.Kind == q.Kind) && m.ContextLength >= q.MinContext && m.MaxOutput >= q.MinOutput &&
		(q.MaxInputPrice == nil || m.InputPrice <= microsPerM(*q.MaxInputPrice)) &&
		(q.MaxOutputPrice == nil || m.OutputPrice <= microsPerM(*q.MaxOutputPrice)) &&
		(q.OwnedBy == "" || owner == q.OwnedBy) && !slices.Contains(q.Exclude, m.ID)
}

// ModelsQuery serves POST /v1/models/query: the models this key can see (as GET /v1/models),
// filtered, sorted and limited by a JSON body.
func (g *Gateway) ModelsQuery(w http.ResponseWriter, r *http.Request) {
	p, e := g.authenticate(r.Context(), r)
	if e != nil {
		writeError(w, e)
		return
	}
	var q modelQuery
	dec := json.NewDecoder(io.LimitReader(r.Body, 64<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&q); err != nil && !errors.Is(err, io.EOF) { // an empty body means no filters
		badRequest(w, "invalid query: "+err.Error())
		return
	}
	limit := 50
	if q.Limit != nil {
		limit = min(*q.Limit, 100)
	}
	if limit < 1 || q.MinContext < 0 || q.MinOutput < 0 || !slices.Contains([]string{"", "id", "price", "context"}, q.Sort) {
		badRequest(w, "limit must be 1-100, minimums non-negative, sort one of id, price, context")
		return
	}
	snap := g.snap.Load()
	var ms []store.Model // Visible is sorted by id
	for _, m := range snap.Visible(p.Org.ID) {
		if (len(p.Key.Models) == 0 || slices.Contains(p.Key.Models, m.ID)) && q.match(m) {
			ms = append(ms, m)
		}
	}
	switch q.Sort {
	case "price": // cheapest first by input + output price per 1M
		sort.SliceStable(ms, func(i, j int) bool { return ms[i].InputPrice+ms[i].OutputPrice < ms[j].InputPrice+ms[j].OutputPrice })
	case "context": // largest first
		sort.SliceStable(ms, func(i, j int) bool { return ms[i].ContextLength > ms[j].ContextLength })
	}
	data := make([]ModelInfo, 0, min(len(ms), limit))
	for _, m := range ms[:min(len(ms), limit)] {
		data = append(data, snap.Info(m))
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data})
}
