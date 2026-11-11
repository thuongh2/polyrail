package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestRotateKeyGraceAndOrgDelete(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	o := &Org{Name: "o"}
	if err := s.CreateOrg(ctx, o, ""); err != nil {
		t.Fatal(err)
	}
	k := &APIKey{OrgID: o.ID, Name: "k", RPM: 7, Models: []string{"a/b"}}
	if err := s.CreateKey(ctx, k, "h1"); err != nil {
		t.Fatal(err)
	}
	// rotate with grace: both hashes resolve, the old one expires at the grace end
	got, err := s.RotateKey(ctx, o.ID, k.ID, "h2", "sk-gw-new", time.Hour)
	if err != nil || got.RPM != 7 || len(got.Models) != 1 || got.Prefix != "sk-gw-new" || got.PrevValidUntil == nil {
		t.Fatalf("rotate: %+v %v", got, err)
	}
	if nk, err := s.KeyByHash(ctx, "h2"); err != nil || nk.ExpiresAt != nil {
		t.Fatalf("new secret: %+v %v", nk, err)
	}
	if ok, err := s.KeyByHash(ctx, "h1"); err != nil || ok.ExpiresAt == nil || !ok.ExpiresAt.Equal(*got.PrevValidUntil) {
		t.Fatalf("old secret during grace: %+v %v", ok, err)
	}
	// rotate again without grace: h1 (earlier rotation) and h2 stop at once
	if _, err := s.RotateKey(ctx, o.ID, k.ID, "h3", "p3", 0); err != nil {
		t.Fatal(err)
	}
	for _, h := range []string{"h1", "h2"} {
		if _, err := s.KeyByHash(ctx, h); !errors.Is(err, ErrNotFound) {
			t.Fatalf("%s still resolves: %v", h, err)
		}
	}
	// grace passed: rejected even though the index entry exists
	if _, err := s.RotateKey(ctx, o.ID, k.ID, "h4", "p4", time.Millisecond); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	if _, err := s.KeyByHash(ctx, "h3"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired grace secret resolves: %v", err)
	}
	if _, err := s.UpdateKey(ctx, o.ID, k.ID, func(k *APIKey) error { k.Revoked = true; return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RotateKey(ctx, o.ID, k.ID, "h5", "p5", 0); !errors.Is(err, ErrInvalid) {
		t.Fatalf("revoked key rotated: %v", err)
	}
	if err := s.DeleteOrg(ctx, o.ID); err != nil {
		t.Fatal(err)
	}
	for _, h := range []string{"h3", "h4"} {
		if _, err := s.KV.Get(ctx, "khash/"+h); !errors.Is(err, ErrNotFound) {
			t.Fatalf("khash/%s survived org delete", h)
		}
	}
}

func TestContentSealedPurgedAndOrgDelete(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	o := &Org{Name: "o"}
	if err := s.CreateOrg(ctx, o, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateOrg(ctx, o.ID, func(o *Org) error {
		o.ContentLogging = &ContentLogging{Enabled: true, RetentionDays: 2}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	big := strings.Repeat("é", ContentMaxBytes) // 2 bytes each: cut on a rune boundary
	for i, at := range []time.Time{now, now.AddDate(0, 0, -5)} {
		id := []string{"new", "old"}[i]
		if err := s.PutContent(ctx, o.ID, LogContent{LogID: id, At: at, Request: `{"secret prompt":1}`, Response: big}); err != nil {
			t.Fatal(err)
		}
	}
	items, _ := s.KV.Scan(ctx, "logc/", "", 0)
	for _, it := range items {
		if strings.Contains(string(it.Value), "secret prompt") {
			t.Fatal("content stored in the clear")
		}
	}
	c, err := s.GetContent(ctx, o.ID, "new")
	if err != nil || c.Request != `{"secret prompt":1}` || !c.ResTruncated || len(c.Response) > ContentMaxBytes || !strings.HasSuffix(c.Response, "é") {
		t.Fatalf("content: %v trunc=%v len=%d", err, c != nil && c.ResTruncated, len(c.Response))
	}
	if _, err := s.GetContent(ctx, "other", "new"); !errors.Is(err, ErrNotFound) {
		t.Fatal("content readable from another org")
	}
	if err := s.PurgeContent(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetContent(ctx, o.ID, "old"); !errors.Is(err, ErrNotFound) {
		t.Fatal("content past retention survived purge")
	}
	if _, err := s.GetContent(ctx, o.ID, "new"); err != nil {
		t.Fatal("purge removed fresh content")
	}
	if err := s.DeleteOrg(ctx, o.ID); err != nil {
		t.Fatal(err)
	}
	if items, _ := s.KV.Scan(ctx, "logc", "", 0); len(items) != 0 {
		t.Fatalf("content survived org delete: %d items", len(items))
	}
}

func TestEachLogRange(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	now := time.Now().UTC()
	var logs []RequestLog
	for i := range 5 {
		logs = append(logs, RequestLog{ID: NewID(), OrgID: "o", At: now.Add(-time.Duration(i) * 24 * time.Hour), Status: 200 + i})
	}
	logs = append(logs, RequestLog{ID: NewID(), OrgID: "other", At: now})
	if err := s.PutLogs(ctx, logs); err != nil {
		t.Fatal(err)
	}
	var got []int
	err := s.EachLog(ctx, "o", now.Add(-3*24*time.Hour-time.Minute), now.Add(-time.Minute), func(l RequestLog) (bool, error) {
		got = append(got, l.Status)
		return len(got) < 2, nil
	})
	if err != nil || len(got) != 2 || got[0] != 201 || got[1] != 202 {
		t.Fatalf("range/limit: %v %v", got, err)
	}
}

func TestMgmtTokens(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	o := &Org{Name: "o"}
	if err := s.CreateOrg(ctx, o, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateMgmtToken(ctx, &MgmtToken{OrgID: o.ID, Role: RoleOwner}, "x"); !errors.Is(err, ErrInvalid) {
		t.Fatal("owner token accepted")
	}
	tok := &MgmtToken{OrgID: o.ID, Name: "ci", Role: RoleAdmin}
	if err := s.CreateMgmtToken(ctx, tok, "mh"); err != nil {
		t.Fatal(err)
	}
	if got, err := s.MgmtTokenByHash(ctx, "mh"); err != nil || got.ID != tok.ID || got.Hash != "mh" {
		t.Fatalf("by hash: %+v %v", got, err)
	}
	if err := s.DeleteOrg(ctx, o.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.MgmtTokenByHash(ctx, "mh"); !errors.Is(err, ErrNotFound) {
		t.Fatal("token survived org delete")
	}
}
