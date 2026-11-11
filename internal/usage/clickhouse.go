package usage

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/polyrail/polyrail/internal/store"
)

// ClickHouse ships raw request logs over the ClickHouse HTTP interface (no driver needed).
// URL form: http://user:pass@host:8123/?database=default
type ClickHouse struct {
	url    string
	client *http.Client
}

const chSchema = `CREATE TABLE IF NOT EXISTS gateway_requests (
  id String, org_id String, key_id String, model LowCardinality(String), provider LowCardinality(String),
  upstream_model String, status UInt16, error String, in_tokens UInt32, out_tokens UInt32,
  cost_micros Int64, latency_ms UInt32, cached Bool, stream Bool, at DateTime64(3, 'UTC')
) ENGINE = MergeTree PARTITION BY toYYYYMM(at) ORDER BY (org_id, at)`

func NewClickHouse(ctx context.Context, rawURL string) (*ClickHouse, error) {
	c := &ClickHouse{url: rawURL, client: &http.Client{Timeout: 30 * time.Second}}
	return c, c.exec(ctx, chSchema, nil)
}

func (c *ClickHouse) exec(ctx context.Context, query string, body []byte) error {
	u, err := url.Parse(c.url)
	if err != nil {
		return err
	}
	q := u.Query()
	q.Set("query", query)
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(body))
	if err != nil {
		return err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("clickhouse %d: %s", resp.StatusCode, msg)
	}
	return nil
}

type chRow struct {
	ID        string `json:"id"`
	OrgID     string `json:"org_id"`
	KeyID     string `json:"key_id"`
	Model     string `json:"model"`
	Provider  string `json:"provider"`
	Upstream  string `json:"upstream_model"`
	Status    int    `json:"status"`
	Error     string `json:"error"`
	InTokens  int    `json:"in_tokens"`
	OutTokens int    `json:"out_tokens"`
	Cost      int64  `json:"cost_micros"`
	LatencyMs int64  `json:"latency_ms"`
	Cached    bool   `json:"cached"`
	Stream    bool   `json:"stream"`
	At        string `json:"at"`
}

func (c *ClickHouse) WriteLogs(ctx context.Context, logs []store.RequestLog) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, l := range logs {
		if err := enc.Encode(chRow{l.ID, l.OrgID, l.KeyID, l.Model, l.Provider, l.Upstream, l.Status, l.Error,
			l.InTokens, l.OutTokens, l.Cost, l.LatencyMs, l.Cached, l.Stream, l.At.UTC().Format("2006-01-02 15:04:05.000")}); err != nil {
			return err
		}
	}
	return c.exec(ctx, "INSERT INTO gateway_requests FORMAT JSONEachRow", buf.Bytes())
}
