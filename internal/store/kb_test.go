package store

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

func testKBStore(t *testing.T, kv KV) {
	ctx := context.Background()
	box, _ := NewBox("0123456789abcdef-test")
	s := New(kv, box)
	org := &Org{Name: "kb org"}
	if err := s.CreateOrg(ctx, org, ""); err != nil {
		t.Fatal(err)
	}
	k := &KB{OrgID: org.ID, Name: "handbook", EmbedModel: "m"}
	if err := s.CreateKB(ctx, k); err != nil {
		t.Fatal(err)
	}
	if !ValidKBID(k.ID) || k.Rev != 1 {
		t.Fatalf("kb %+v", k)
	}
	if _, err := s.GetKB(ctx, "other", k.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other org: %v", err)
	}
	if _, err := s.GetKB(ctx, org.ID, "../x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("bad id: %v", err)
	}

	d := &KBDoc{ID: NewKBDocID(), KBID: k.ID, OrgID: org.ID, Filename: "a.md", Bytes: 10}
	if err := s.AddKBDoc(ctx, d, "secret text"); err != nil {
		t.Fatal(err)
	}
	if raw, _ := kv.Get(ctx, kbSrcKey(org.ID, k.ID, d.ID)); string(raw) == "secret text" || len(raw) == 0 {
		t.Fatal("source not sealed")
	}
	if txt, err := s.GetKBSource(ctx, org.ID, k.ID, d.ID); err != nil || txt != "secret text" {
		t.Fatalf("source %q %v", txt, err)
	}
	if got, _ := s.GetKB(ctx, org.ID, k.ID); got.Docs != 1 || got.Bytes != 10 {
		t.Fatalf("counts %+v", got)
	}

	// claim, competing claim, renew, requeue while leased, finish -> queued again
	if _, _, err := s.ClaimKBJob(ctx, d.ID, "w1", time.Minute, 3); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.ClaimKBJob(ctx, d.ID, "w2", time.Minute, 3); !errors.Is(err, ErrLeased) {
		t.Fatalf("second claim: %v", err)
	}
	if err := s.RenewKBJob(ctx, d.ID, "w2", time.Minute); !errors.Is(err, ErrLeased) {
		t.Fatalf("renew by non-owner: %v", err)
	}
	if err := s.RequeueKBDoc(ctx, org.ID, k.ID, d.ID); err != nil {
		t.Fatal(err)
	}
	res := KBDocResult{Chunks: 3, Chars: 30, InTokens: 8, EmbedModel: "m"}
	if err := s.FinishKBJob(ctx, d.ID, "w1", res); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetKBDoc(ctx, org.ID, k.ID, d.ID); got.Status != KBQueued {
		t.Fatalf("again: %+v", got)
	}
	if _, _, err := s.ClaimKBJob(ctx, d.ID, "w1", time.Minute, 3); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishKBJob(ctx, d.ID, "w1", res); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetKBDoc(ctx, org.ID, k.ID, d.ID)
	kb, _ := s.GetKB(ctx, org.ID, k.ID)
	if got.Status != KBReady || got.Chunks != 3 || kb.Chunks != 3 || kb.Rev != 3 {
		t.Fatalf("ready doc %+v kb %+v", got, kb)
	}
	if jobs, _ := s.ListKBJobs(ctx, 0); len(jobs) != 0 {
		t.Fatalf("queue left %+v", jobs)
	}

	// transient failures until attempts run out
	if err := s.RequeueKBDoc(ctx, org.ID, k.ID, d.ID); err != nil {
		t.Fatal(err)
	}
	for i := range 2 {
		if _, _, err := s.ClaimKBJob(ctx, d.ID, "w1", time.Minute, 2); err != nil {
			t.Fatalf("attempt %d: %v", i, err)
		}
		if err := s.FailKBJob(ctx, d.ID, "w1", "embedding failed", false, 3, 0); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := s.ClaimKBJob(ctx, d.ID, "w1", time.Minute, 2); !errors.Is(err, ErrNotFound) {
		t.Fatalf("exhausted claim: %v", err)
	}
	if got, _ := s.GetKBDoc(ctx, org.ID, k.ID, d.ID); got.Status != KBFailed || got.Error != "embedding failed" || got.Chunks != 3 {
		t.Fatalf("failed doc %+v", got)
	}

	// deletes leave no keys
	if err := kv.Update(ctx, func(tx Tx) error { return tx.Put(KBChunkKey(org.ID, k.ID, d.ID, 0), []byte("x")) }); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteKBDoc(ctx, org.ID, k.ID, d.ID); err != nil {
		t.Fatal(err)
	}
	if kb, _ := s.GetKB(ctx, org.ID, k.ID); kb.Docs != 0 || kb.Chunks != 0 || kb.Bytes != 0 {
		t.Fatalf("counts after delete %+v", kb)
	}
	d2 := &KBDoc{ID: NewKBDocID(), KBID: k.ID, OrgID: org.ID}
	if err := s.AddKBDoc(ctx, d2, "t"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteKB(ctx, org.ID, k.ID); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"kb/", "kbdoc/", "kbsrc/", "kbq/", "kbchunk/"} {
		if items, _ := kv.Scan(ctx, p, "", 0); len(items) != 0 {
			t.Fatalf("%s left %v", p, items)
		}
	}

	// DeleteOrg removes every kb* key of the org
	k2 := &KB{OrgID: org.ID, Name: "x", EmbedModel: "m"}
	if err := s.CreateKB(ctx, k2); err != nil {
		t.Fatal(err)
	}
	if err := s.AddKBDoc(ctx, &KBDoc{ID: NewKBDocID(), KBID: k2.ID, OrgID: org.ID}, "t"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteOrg(ctx, org.ID); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"kb/", "kbdoc/", "kbsrc/", "kbchunk/"} {
		if items, _ := kv.Scan(ctx, p, "", 0); len(items) != 0 {
			t.Fatalf("%s left after DeleteOrg %v", p, items)
		}
	}
	o2 := &Org{Name: "o2"}
	if err := s.CreateOrg(ctx, o2, ""); err != nil {
		t.Fatal(err)
	}
	for range KBMaxPerOrg {
		if err := s.CreateKB(ctx, &KB{OrgID: o2.ID, Name: "x"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.CreateKB(ctx, &KB{OrgID: o2.ID, Name: "x"}); !errors.Is(err, ErrLimit) {
		t.Fatalf("limit: %v", err)
	}
}

func TestKBStorePebble(t *testing.T) {
	kv, err := OpenPebble(t.TempDir(), 8)
	if err != nil {
		t.Fatal(err)
	}
	defer kv.Close()
	testKBStore(t, kv)
}

func TestKBStorePostgres(t *testing.T) {
	url := os.Getenv("TEST_POSTGRES_URL")
	if url == "" {
		t.Skip("TEST_POSTGRES_URL not set")
	}
	kv, err := OpenPostgres(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	defer kv.Close()
	_ = kv.DeleteRange(context.Background(), "", "\x7f")
	testKBStore(t, kv)
}
