package store

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Postgres used as an ordered KV (B-tree on k). Text keys use COLLATE "C" so ordering is bytewise,
// matching Pebble.
const pgSchema = `
CREATE TABLE IF NOT EXISTS kv (k text COLLATE "C" PRIMARY KEY, v bytea NOT NULL);
CREATE TABLE IF NOT EXISTS agg (k text COLLATE "C" PRIMARY KEY, a0 bigint NOT NULL DEFAULT 0,
  a1 bigint NOT NULL DEFAULT 0, a2 bigint NOT NULL DEFAULT 0, a3 bigint NOT NULL DEFAULT 0,
  a4 bigint NOT NULL DEFAULT 0, a5 bigint NOT NULL DEFAULT 0, a6 bigint NOT NULL DEFAULT 0);`

type pgKV struct{ pool *pgxpool.Pool }

func OpenPostgres(ctx context.Context, url string) (KV, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, err
	}
	if _, err := pool.Exec(ctx, pgSchema); err != nil {
		pool.Close()
		return nil, fmt.Errorf("postgres schema: %w", err)
	}
	return &pgKV{pool: pool}, nil
}

func (p *pgKV) Get(ctx context.Context, key string) ([]byte, error) {
	var v []byte
	err := p.pool.QueryRow(ctx, `SELECT v FROM kv WHERE k = $1`, key).Scan(&v)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return v, err
}

// rangeQuery builds the WHERE clause for a prefix scan with an exclusive cursor.
func rangeQuery(table, cols, prefix, after string, limit int) (string, []any) {
	var sb strings.Builder
	args := []any{prefix}
	fmt.Fprintf(&sb, "SELECT k, %s FROM %s WHERE k >= $1", cols, table)
	if end := prefixEnd(prefix); end != "" {
		args = append(args, end)
		fmt.Fprintf(&sb, " AND k < $%d", len(args))
	}
	if after != "" {
		args = append(args, after)
		fmt.Fprintf(&sb, " AND k > $%d", len(args))
	}
	sb.WriteString(" ORDER BY k")
	if limit > 0 {
		fmt.Fprintf(&sb, " LIMIT %d", limit)
	}
	return sb.String(), args
}

func (p *pgKV) Scan(ctx context.Context, prefix, after string, limit int) ([]Item, error) {
	q, args := rangeQuery("kv", "v", prefix, after, limit)
	rows, err := p.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Item
	for rows.Next() {
		var it Item
		if err := rows.Scan(&it.Key, &it.Value); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

type pgTx struct {
	ctx context.Context
	tx  pgx.Tx
}

func (t pgTx) Get(key string) ([]byte, error) {
	var v []byte
	err := t.tx.QueryRow(t.ctx, `SELECT v FROM kv WHERE k = $1`, key).Scan(&v)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return v, err
}

func (t pgTx) Put(key string, v []byte) error {
	_, err := t.tx.Exec(t.ctx, `INSERT INTO kv (k, v) VALUES ($1, $2) ON CONFLICT (k) DO UPDATE SET v = excluded.v`, key, v)
	return err
}

func (t pgTx) Delete(key string) error {
	_, err := t.tx.Exec(t.ctx, `DELETE FROM kv WHERE k = $1`, key)
	return err
}

// Update runs fn in a SERIALIZABLE transaction, retrying on serialization failures.
func (p *pgKV) Update(ctx context.Context, fn func(Tx) error) error {
	for attempt := 0; ; attempt++ {
		err := pgx.BeginTxFunc(ctx, p.pool, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(tx pgx.Tx) error {
			return fn(pgTx{ctx, tx})
		})
		var pgErr *pgconn.PgError
		if attempt < 5 && errors.As(err, &pgErr) && (pgErr.Code == "40001" || pgErr.Code == "40P01") {
			continue
		}
		return err
	}
}

func (p *pgKV) AddAgg(ctx context.Context, adds map[string]Agg) error {
	if len(adds) == 0 {
		return nil
	}
	keys := make([]string, 0, len(adds))
	for k := range adds {
		keys = append(keys, k)
	}
	sort.Strings(keys) // consistent row lock order across concurrent batches avoids deadlocks
	b := &pgx.Batch{}
	for _, k := range keys {
		a := adds[k]
		b.Queue(`INSERT INTO agg (k, a0, a1, a2, a3, a4, a5, a6) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
ON CONFLICT (k) DO UPDATE SET a0 = agg.a0 + excluded.a0, a1 = agg.a1 + excluded.a1, a2 = agg.a2 + excluded.a2,
a3 = agg.a3 + excluded.a3, a4 = agg.a4 + excluded.a4, a5 = agg.a5 + excluded.a5, a6 = agg.a6 + excluded.a6`,
			k, a[0], a[1], a[2], a[3], a[4], a[5], a[6])
	}
	return p.pool.SendBatch(ctx, b).Close()
}

const aggCols = "a0, a1, a2, a3, a4, a5, a6"

func scanAggRow(row pgx.Row, k *string, a *Agg) error {
	return row.Scan(k, &a[0], &a[1], &a[2], &a[3], &a[4], &a[5], &a[6])
}

func (p *pgKV) GetAgg(ctx context.Context, key string) (Agg, error) {
	var a Agg
	var k string
	err := scanAggRow(p.pool.QueryRow(ctx, `SELECT k, `+aggCols+` FROM agg WHERE k = $1`, key), &k, &a)
	if errors.Is(err, pgx.ErrNoRows) {
		return Agg{}, nil
	}
	return a, err
}

func (p *pgKV) ScanAgg(ctx context.Context, prefix string) (map[string]Agg, error) {
	q, args := rangeQuery("agg", aggCols, prefix, "", 0)
	rows, err := p.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]Agg{}
	for rows.Next() {
		var k string
		var a Agg
		if err := scanAggRow(rows, &k, &a); err != nil {
			return nil, err
		}
		out[k] = a
	}
	return out, rows.Err()
}

func (p *pgKV) DeleteRange(ctx context.Context, start, end string) error {
	for _, t := range []string{"kv", "agg"} {
		if _, err := p.pool.Exec(ctx, `DELETE FROM `+t+` WHERE k >= $1 AND k < $2`, start, end); err != nil {
			return err
		}
	}
	return nil
}

func (p *pgKV) Ping(ctx context.Context) error { return p.pool.Ping(ctx) }
func (p *pgKV) Close() error                   { p.pool.Close(); return nil }
