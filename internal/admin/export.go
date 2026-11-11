package admin

import (
	"encoding/csv"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/polyrail/polyrail/internal/store"
)

// Activity export (docs/API.md "Exports"): usage rollups and request logs as streamed CSV
// (RFC 4180 via encoding/csv). Cells that a spreadsheet would run as a formula get a leading '.

const maxExportLogs = 100_000

// safeCell defuses spreadsheet formula injection (cells starting with = + - @, tab or CR).
func safeCell(s string) string {
	if s != "" && strings.ContainsRune("=+-@\t\r", rune(s[0])) {
		return "'" + s
	}
	return s
}

func writeRow(cw *csv.Writer, cells ...string) error {
	for i, c := range cells {
		cells[i] = safeCell(c)
	}
	return cw.Write(cells)
}

// parseTime reads a date (2006-01-02, UTC) or an RFC 3339 time. A date used as `to` means the end
// of that day.
func parseTime(v string, end bool) (time.Time, error) {
	if t, err := time.Parse("2006-01-02", v); err == nil {
		if end {
			t = t.AddDate(0, 0, 1)
		}
		return t, nil
	}
	return time.Parse(time.RFC3339, v)
}

// exportRange parses from/to (from inclusive, to exclusive) with a default span of days ending now.
func exportRange(r *http.Request, days int) (time.Time, time.Time, error) {
	q := r.URL.Query()
	to := time.Now().UTC()
	var err error
	if v := q.Get("to"); v != "" {
		if to, err = parseTime(v, true); err != nil {
			return to, to, fmt.Errorf("%w: to must be YYYY-MM-DD or RFC 3339", store.ErrInvalid)
		}
	}
	from := to.AddDate(0, 0, -days)
	if v := q.Get("from"); v != "" {
		if from, err = parseTime(v, false); err != nil {
			return from, to, fmt.Errorf("%w: from must be YYYY-MM-DD or RFC 3339", store.ErrInvalid)
		}
	}
	if !from.Before(to) || to.Sub(from) > 366*24*time.Hour {
		return from, to, fmt.Errorf("%w: from must be before to, at most 366 days apart", store.ErrInvalid)
	}
	return from.UTC(), to.UTC(), nil
}

func csvHeaders(w http.ResponseWriter, name string) *csv.Writer {
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Cache-Control", "no-store")
	return csv.NewWriter(w)
}

func aggCells(a store.Agg) []string {
	avg := int64(0)
	if a[store.AggRequests] > 0 {
		avg = a[store.AggLatencyMs] / a[store.AggRequests]
	}
	return []string{strconv.FormatInt(a[store.AggRequests], 10), strconv.FormatInt(a[store.AggErrors], 10),
		strconv.FormatInt(a[store.AggInTokens], 10), strconv.FormatInt(a[store.AggOutTokens], 10),
		strconv.FormatFloat(float64(a[store.AggCostMicros])/1e6, 'f', 6, 64), strconv.FormatInt(avg, 10),
		strconv.FormatInt(a[store.AggCacheHits], 10), strconv.FormatInt(a[store.AggCacheReadTokens], 10),
		strconv.FormatInt(a[store.AggCacheWriteTokens], 10)}
}

// exportUsage streams daily usage rollups grouped by model, key, creator (the user who created
// the key) or day. Rollups are daily, so from/to cover whole UTC days. The platform scope has
// per-model rollups only (model, day).
func (a *API) exportUsage(w http.ResponseWriter, r *http.Request) {
	org := r.PathValue("org")
	group := r.URL.Query().Get("group")
	if group == "" {
		group = "model"
	}
	if group != "model" && group != "key" && group != "creator" && group != "day" && group != "workspace" ||
		(org == "_" && (group == "key" || group == "creator" || group == "workspace")) {
		fail(w, fmt.Errorf("%w: group must be model, key, creator, workspace or day (platform: model or day)", store.ErrInvalid))
		return
	}
	keyWS, only, err := a.usageWorkspaces(r, org, group == "workspace")
	if err != nil {
		fail(w, err)
		return
	}
	if only != nil && group == "model" {
		fail(w, fmt.Errorf("%w: usage by model cannot be filtered by workspace (rollups are per key or per model)", store.ErrInvalid))
		return
	}
	from, to, err := exportRange(r, 30)
	if err != nil {
		fail(w, err)
		return
	}
	ctx := r.Context()
	creator := map[string]string{} // key id -> creator email
	if group == "creator" {
		keys, err := a.S.ListKeys(ctx, org)
		if err != nil {
			fail(w, err)
			return
		}
		emails := map[string]string{}
		for _, k := range keys {
			if _, ok := emails[k.CreatedBy]; !ok {
				emails[k.CreatedBy] = k.CreatedBy // management tokens and removed users keep their id
				if u, err := a.S.GetUser(ctx, k.CreatedBy); err == nil {
					emails[k.CreatedBy] = u.Email
				}
			}
			creator[k.ID] = emails[k.CreatedBy]
		}
	}
	cw := csvHeaders(w, fmt.Sprintf("usage-%s-%s.csv", group, store.Day(from)))
	head := []string{"date", group, "requests", "errors", "in_tokens", "out_tokens", "cost_usd", "avg_latency_ms", "cache_hits", "cache_read_tokens", "cache_write_tokens"}
	if group == "day" {
		head = append(head[:1], head[2:]...)
	}
	if err := writeRow(cw, head...); err != nil {
		return
	}
	last := store.Day(to.Add(-time.Nanosecond))
	for d := from; store.Day(d) <= last; d = d.AddDate(0, 0, 1) {
		day := store.Day(d)
		prefix := "agg/" + org + "/" + day + "/"
		m, err := a.S.KV.ScanAgg(ctx, prefix)
		if err != nil {
			cw.Flush()
			slog.Error("usage export", "err", err, "org", org) // headers are sent: the client sees a cut-off file
			return
		}
		rows := map[string]store.Agg{}
		var total store.Agg
		for k, v := range m {
			rest := strings.TrimPrefix(k, prefix)
			if only != nil && (!strings.HasPrefix(rest, "k/") || !only(keyWS[rest[2:]])) {
				continue
			}
			switch {
			case only != nil && group == "day": // one workspace: the day total is its keys' sum
				total.Add(v)
			case rest == "t":
				total = v
			case strings.HasPrefix(rest, "m/"):
				if org == "_" {
					total.Add(v)
				}
				if group == "model" {
					rows[rest[2:]] = v
				}
			case strings.HasPrefix(rest, "k/") && (group == "key" || group == "creator" || group == "workspace"):
				name := rest[2:]
				if group == "workspace" {
					name = store.WSRef(keyWS[name])
				}
				if group == "creator" {
					if name = creator[name]; name == "" {
						name = "unknown"
					}
				}
				cur := rows[name]
				cur.Add(v)
				rows[name] = cur
			}
		}
		date := d.Format("2006-01-02")
		if group == "day" {
			if err := writeRow(cw, append([]string{date}, aggCells(total)...)...); err != nil {
				return
			}
			continue
		}
		names := make([]string, 0, len(rows))
		for n := range rows {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			if err := writeRow(cw, append([]string{date, n}, aggCells(rows[n])...)...); err != nil {
				return
			}
		}
		cw.Flush()
	}
	cw.Flush()
}

// exportLogs streams the org's request logs in [from, to) (default: the last 7 days), newest
// first, at most maxExportLogs rows (X-GW-Row-Cap). Narrow the range to get older rows.
func (a *API) exportLogs(w http.ResponseWriter, r *http.Request) {
	from, to, err := exportRange(r, 7)
	if err != nil {
		fail(w, err)
		return
	}
	org := r.PathValue("org")
	w.Header().Set("X-GW-Row-Cap", strconv.Itoa(maxExportLogs))
	cw := csvHeaders(w, fmt.Sprintf("logs-%s-%s.csv", store.Day(from), store.Day(to)))
	rc := http.NewResponseController(w)
	ws, filter := "", r.URL.Query().Get("workspace") != ""
	if filter {
		if ws, err = a.resolveWS(r, r.URL.Query().Get("workspace")); err != nil {
			fail(w, err)
			return
		}
	}
	if err := writeRow(cw, "id", "at", "key_id", "model", "provider", "upstream_model", "status", "error", "in_tokens",
		"out_tokens", "cost_usd", "latency_ms", "cached", "stream", "kb_chunks", "end_user",
		"cache_read_tokens", "cache_write_tokens", "reasoning_tokens"); err != nil {
		return
	}
	n := 0
	err = a.S.EachLog(r.Context(), org, from, to, func(l store.RequestLog) (bool, error) {
		if filter && l.Workspace != ws {
			return true, nil
		}
		n++
		if err := writeRow(cw, l.ID, l.At.UTC().Format(time.RFC3339Nano), l.KeyID, l.Model, l.Provider, l.Upstream,
			strconv.Itoa(l.Status), l.Error, strconv.Itoa(l.InTokens), strconv.Itoa(l.OutTokens),
			strconv.FormatFloat(float64(l.Cost)/1e6, 'f', 6, 64), strconv.FormatInt(l.LatencyMs, 10),
			strconv.FormatBool(l.Cached), strconv.FormatBool(l.Stream), strconv.Itoa(l.KBChunks), l.EndUser,
			strconv.Itoa(l.CacheReadTokens), strconv.Itoa(l.CacheWriteTokens), strconv.Itoa(l.ReasoningTokens)); err != nil {
			return false, err
		}
		if n%1000 == 0 {
			cw.Flush()
			_ = rc.Flush()
		}
		return n < maxExportLogs, nil
	})
	cw.Flush()
	if err != nil && r.Context().Err() == nil {
		slog.Error("log export", "err", err, "org", org) // headers are sent: the client sees a cut-off file
	}
}

// logContent returns the stored request and response bodies of one request log (admins only,
// when the org has content logging on). Each read is audited.
func (a *API) logContent(w http.ResponseWriter, r *http.Request) {
	org, id := r.PathValue("org"), r.PathValue("id")
	c, err := a.S.GetContent(r.Context(), org, id)
	if err != nil {
		fail(w, err)
		return
	}
	a.audit(r, org, "log.content.read", id, "")
	writeJSON(w, 200, c)
}
