package usage

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/polyrail/polyrail/internal/store"
)

func TestClickHouseSink(t *testing.T) {
	var queries []string
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		queries = append(queries, r.URL.Query().Get("query"))
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		if r.URL.Query().Get("database") != "gw" {
			http.Error(w, "wrong db", 400)
		}
	}))
	defer srv.Close()
	ctx := context.Background()
	ch, err := NewClickHouse(ctx, srv.URL+"/?database=gw")
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 1, 2, 3, 4, 5, 6e6, time.UTC)
	if err := ch.WriteLogs(ctx, []store.RequestLog{{ID: "a", OrgID: "o", Model: "m", Status: 200, At: at, EndUser: "u-1"}}); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(queries[0], "CREATE TABLE IF NOT EXISTS gateway_requests") ||
		!strings.HasPrefix(queries[1], "ALTER TABLE gateway_requests ADD COLUMN IF NOT EXISTS end_user") || queries[2] != "INSERT INTO gateway_requests FORMAT JSONEachRow" {
		t.Fatalf("queries: %q", queries)
	}
	var row map[string]any
	if err := json.Unmarshal([]byte(body), &row); err != nil || row["at"] != "2026-01-02 03:04:05.006" || row["org_id"] != "o" || row["end_user"] != "u-1" {
		t.Fatalf("row: %s %v", body, err)
	}
}
