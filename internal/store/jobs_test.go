package store

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

func TestJobsIsolationAndExactlyOnceBilling(t *testing.T) {
	kv, err := OpenPebble(t.TempDir(), 8)
	if err != nil {
		t.Fatal(err)
	}
	defer kv.Close()
	s := New(kv, nil)
	ctx := context.Background()
	j := &Job{ID: NewJobID(), OrgID: "orga", UpstreamID: "file-up"}
	if err := s.PutJob(ctx, JobFile, j); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetJob(ctx, JobFile, "orgb", j.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other org: %v", err)
	}
	if _, err := s.GetJob(ctx, JobFile, "orga", "../x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("bad id: %v", err)
	}
	if err := s.PutJob(ctx, JobFile, &Job{ID: "x/y", OrgID: "orga"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad id put: %v", err)
	}
	newer := &Job{ID: NewJobID(), OrgID: "orga"}
	if err := s.PutJob(ctx, JobFile, newer); err != nil {
		t.Fatal(err)
	}
	ls, err := s.ListJobs(ctx, JobFile, "orga", "", 10)
	if err != nil || len(ls) != 2 || ls[0].ID != newer.ID {
		t.Fatalf("list newest first: %v %+v", err, ls)
	}
	if ls, _ := s.ListJobs(ctx, JobFile, "orgb", "", 10); len(ls) != 0 {
		t.Fatalf("org b sees %d", len(ls))
	}

	var wins atomic.Int64
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			switch err := s.ClaimBilling(ctx, JobFile, "orga", j.ID, 42); {
			case err == nil:
				wins.Add(1)
			case !errors.Is(err, ErrBilled):
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatalf("billed %d times", wins.Load())
	}
	got, _ := s.GetJob(ctx, JobFile, "orga", j.ID)
	if !got.Billed || got.CostMicros != 42 {
		t.Fatalf("job %+v", got)
	}
}
