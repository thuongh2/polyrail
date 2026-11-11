package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ContentLogging is an org's opt-in to store request and response bodies with its request logs.
// Off by default. Bodies are sealed with the store Box and kept RetentionDays days.
type ContentLogging struct {
	Enabled       bool `json:"enabled"`
	RetentionDays int  `json:"retention_days"` // 1..ContentMaxDays
}

const (
	ContentMaxDays  = 90
	ContentMaxBytes = 64 << 10 // per body; longer bodies are cut and marked truncated
	contentIndex    = "logcorg/"
)

// LogContent is the stored body pair of one request log.
type LogContent struct {
	LogID        string    `json:"log_id"`
	At           time.Time `json:"at"`
	Request      string    `json:"request"`
	Response     string    `json:"response"`
	ReqTruncated bool      `json:"request_truncated,omitempty"`
	ResTruncated bool      `json:"response_truncated,omitempty"`
}

// storedContent is the persisted form: both bodies sealed, nothing in the clear.
type storedContent struct {
	LogID        string    `json:"log_id"`
	At           time.Time `json:"at"`
	Request      string    `json:"req"`
	Response     string    `json:"res"`
	ReqTruncated bool      `json:"req_trunc,omitempty"`
	ResTruncated bool      `json:"res_trunc,omitempty"`
}

// contentPrefix holds an org's bodies, partitioned by day: logc/<org>/<yyyymmdd>/<log id>, so
// retention is one range delete per org.
func contentPrefix(org string) string { return "logc/" + org + "/" }

func contentKey(org string, at time.Time, logID string) string {
	return contentPrefix(org) + Day(at) + "/" + logID
}

// Truncate cuts s to ContentMaxBytes on a UTF-8 boundary and reports whether it cut.
func Truncate(s string) (string, bool) {
	if len(s) <= ContentMaxBytes {
		return s, false
	}
	n := ContentMaxBytes
	for n > 0 && n > ContentMaxBytes-4 && (s[n]&0xC0) == 0x80 {
		n--
	}
	return s[:n], true
}

// PutContent seals and stores the bodies of request log c.LogID of org.
func (s *Store) PutContent(ctx context.Context, org string, c LogContent) error {
	if s.box == nil {
		return errors.New("content logging needs MASTER_KEY")
	}
	if org == "" || strings.Contains(org, "/") || c.LogID == "" {
		return fmt.Errorf("%w: org and log id required", ErrInvalid)
	}
	sc := storedContent{LogID: c.LogID, At: c.At.UTC()}
	var err error
	req, trunc := Truncate(c.Request)
	sc.ReqTruncated = c.ReqTruncated || trunc
	res, trunc := Truncate(c.Response)
	sc.ResTruncated = c.ResTruncated || trunc
	if sc.Request, err = s.box.Seal(req); err != nil {
		return err
	}
	if sc.Response, err = s.box.Seal(res); err != nil {
		return err
	}
	return s.KV.Update(ctx, func(tx Tx) error { return putJSON(tx, contentKey(org, c.At, c.LogID), sc) })
}

// GetContent finds the bodies of log id of org, looking back ContentMaxDays days.
func (s *Store) GetContent(ctx context.Context, org, logID string) (*LogContent, error) {
	if s.box == nil || strings.Contains(logID, "/") || logID == "" {
		return nil, ErrNotFound
	}
	now := time.Now()
	for d := 0; d <= ContentMaxDays; d++ {
		raw, err := s.KV.Get(ctx, contentKey(org, now.AddDate(0, 0, -d), logID))
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		var sc storedContent
		if err := json.Unmarshal(raw, &sc); err != nil {
			return nil, err
		}
		out := &LogContent{LogID: sc.LogID, At: sc.At, ReqTruncated: sc.ReqTruncated, ResTruncated: sc.ResTruncated}
		if out.Request, err = s.box.Open(sc.Request); err != nil {
			return nil, err
		}
		if out.Response, err = s.box.Open(sc.Response); err != nil {
			return nil, err
		}
		return out, nil
	}
	return nil, ErrNotFound
}

// PurgeContent deletes stored bodies older than each org's retention (whole days, like
// PruneLogs), and all bodies of orgs that no longer exist.
func (s *Store) PurgeContent(ctx context.Context) error {
	items, err := s.KV.Scan(ctx, contentIndex, "", 0)
	if err != nil {
		return err
	}
	for _, it := range items {
		org := strings.TrimPrefix(it.Key, contentIndex)
		o, err := s.GetOrg(ctx, org)
		switch {
		case errors.Is(err, ErrNotFound):
			if err := s.DeletePrefix(ctx, contentPrefix(org)); err != nil {
				return err
			}
			if err := s.KV.Update(ctx, func(tx Tx) error { return tx.Delete(it.Key) }); err != nil {
				return err
			}
			continue
		case err != nil:
			return err
		}
		days := ContentMaxDays
		if c := o.ContentLogging; c != nil && c.RetentionDays > 0 && c.RetentionDays < days {
			days = c.RetentionDays
		}
		cutoff := Day(time.Now().AddDate(0, 0, -days))
		if err := s.KV.DeleteRange(ctx, contentPrefix(org), contentPrefix(org)+cutoff); err != nil {
			return err
		}
	}
	return nil
}

// EachLog calls fn for org's request logs with from <= At < to, newest first, until fn returns
// false or an error. It reads day partitions in pages, so callers can stream large ranges.
func (s *Store) EachLog(ctx context.Context, org string, from, to time.Time, fn func(RequestLog) (bool, error)) error {
	first := Day(from)
	for d := to.UTC(); Day(d) >= first; d = d.AddDate(0, 0, -1) {
		prefix := "log/" + Day(d) + "/" + org + "/"
		after := ""
		for {
			items, err := s.KV.Scan(ctx, prefix, after, 1000)
			if err != nil {
				return err
			}
			for _, it := range items {
				var l RequestLog
				if err := json.Unmarshal(it.Value, &l); err != nil {
					return fmt.Errorf("decode %s: %w", it.Key, err)
				}
				if l.At.Before(from) || !l.At.Before(to) {
					continue
				}
				if more, err := fn(l); err != nil || !more {
					return err
				}
			}
			if len(items) < 1000 {
				break
			}
			after = items[len(items)-1].Key
		}
	}
	return nil
}
