package kb

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/agent-gateway/agent-gateway/internal/store"
)

// Embedding batch bounds (docs/KB.md §2).
const (
	batchInputs = 64
	batchChars  = 200000
)

// Start runs cfg.Workers ingestion workers until ctx ends. Workers on every node share the KV
// queue (kbq/); a claimed doc is leased for 2 min, renewed per embedding batch, and an expired
// lease makes the doc claimable again (at most 3 attempts).
func (s *Service) Start(ctx context.Context) {
	for range s.cfg.Workers {
		go s.work(ctx, store.NewID())
	}
}

func (s *Service) work(ctx context.Context, owner string) {
	t := time.NewTicker(s.poll)
	defer t.Stop()
	for {
		s.drain(ctx, owner)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// drain claims and processes every claimable queue entry, round-robin across orgs.
// shortcut: reads the whole queue per poll (≤ 200 entries per org); shard it by org if that grows.
func (s *Service) drain(ctx context.Context, owner string) {
	jobs, err := s.st.ListKBJobs(ctx, 0)
	if err != nil {
		if ctx.Err() == nil {
			slog.Warn("kb queue scan failed", "err", err)
		}
		return
	}
	mQueueDepth.Set(float64(len(jobs)))
	now := time.Now()
	for _, j := range fairOrder(jobs) {
		if ctx.Err() != nil {
			return
		}
		if j.LeaseUntil.After(now) {
			continue
		}
		job, doc, err := s.st.ClaimKBJob(ctx, j.DocID, owner, s.lease, maxAttempts)
		switch {
		case errors.Is(err, store.ErrLeased) || errors.Is(err, store.ErrNotFound):
			continue
		case err != nil:
			slog.Warn("kb claim failed", "err", err)
			continue
		}
		s.process(ctx, owner, job, doc)
	}
}

// process chunks, embeds and stores one claimed doc, then records the outcome.
func (s *Service) process(ctx context.Context, owner string, job *store.KBJob, doc *store.KBDoc) {
	fail := func(msg string, permanent bool) {
		status := "retry"
		if permanent || doc.Attempts >= maxAttempts {
			status = "failed"
		}
		mIngest.WithLabelValues(status).Inc()
		if err := s.st.FailKBJob(ctx, doc.ID, owner, msg, permanent, maxAttempts, s.retry); err != nil && !errors.Is(err, store.ErrLeased) {
			slog.Warn("kb fail record failed", "err", err)
		}
	}
	k, err := s.st.GetKB(ctx, job.OrgID, job.KBID)
	if err != nil {
		fail("knowledge base unavailable", errors.Is(err, store.ErrNotFound))
		return
	}
	text, err := s.st.GetKBSource(ctx, job.OrgID, job.KBID, doc.ID)
	if err != nil {
		fail("document source unavailable", errors.Is(err, store.ErrNotFound))
		return
	}
	chunks := chunkText(text, k.ChunkChars, k.OverlapChars)
	if k.Chunks-doc.Chunks+len(chunks) > store.KBMaxChunks {
		fail("knowledge base chunk limit reached", true)
		return
	}
	if total, err := s.orgChunks(ctx, job.OrgID); err != nil {
		fail("knowledge base unavailable", false)
		return
	} else if total-doc.Chunks+len(chunks) > s.cfg.MaxOrgChunks {
		fail("organization chunk limit reached", true)
		return
	}

	inputs := make([]string, len(chunks))
	chars := 0
	for i, c := range chunks {
		head := doc.Filename
		if c.Section != "" {
			head += " § " + c.Section
		}
		inputs[i] = head + "\n\n" + c.Text
		chars += len([]rune(inputs[i]))
	}
	for start := 0; start < len(inputs); {
		end, n := start, 0
		for end < len(inputs) && end-start < batchInputs && (end == start || n+len(inputs[end]) <= batchChars) {
			n += len(inputs[end])
			end++
		}
		vecs, err := s.emb.Embed(ctx, job.OrgID, doc.KeyID, k.EmbedModel, k.Dimensions, inputs[start:end])
		if errors.Is(err, ErrInsufficientCredits) {
			fail("insufficient credits", true)
			return
		}
		if err != nil || len(vecs) != end-start {
			slog.Warn("kb embedding failed", "org", job.OrgID, "doc", doc.ID, "err", err)
			fail("embedding failed", false)
			return
		}
		for i, v := range vecs {
			if len(v) > MaxDimensions { // a tenant's own endpoint could return any size: bound the index
				fail("embedding has too many dimensions", true)
				return
			}
			chunks[start+i].Vec = v
		}
		if err := s.st.RenewKBJob(ctx, doc.ID, owner, s.lease); err != nil {
			return // lease lost: another worker owns the doc now
		}
		start = end
	}
	if err := s.vs.Upsert(ctx, job.OrgID, job.KBID, doc.ID, chunks); err != nil {
		slog.Warn("kb chunk write failed", "err", err)
		fail("storing chunks failed", false)
		return
	}
	err = s.st.FinishKBJob(ctx, doc.ID, owner, store.KBDocResult{Chunks: len(chunks), Chars: len([]rune(text)),
		InTokens: chars / 4, EmbedModel: k.EmbedModel, Dimensions: k.Dimensions})
	switch {
	case errors.Is(err, store.ErrNotFound): // doc or KB deleted meanwhile
		_ = s.vs.DeleteDoc(ctx, job.OrgID, job.KBID, doc.ID)
	case err != nil && !errors.Is(err, store.ErrLeased):
		slog.Warn("kb finish failed", "err", err)
	case err == nil:
		mIngest.WithLabelValues("ready").Inc()
		s.forget(job.OrgID, job.KBID)
	}
}

// orgChunks totals the chunks of an org's KBs.
func (s *Service) orgChunks(ctx context.Context, org string) (int, error) {
	ks, err := s.st.ListKBs(ctx, org)
	n := 0
	for _, k := range ks {
		n += k.Chunks
	}
	return n, err
}

// fairOrder turns the queue (newest doc first) into a processing order that takes each org's
// oldest doc in turn, so one org's backlog cannot hold up the others.
func fairOrder(jobs []store.KBJob) []store.KBJob {
	var orgs []string
	byOrg := map[string][]store.KBJob{}
	for i := len(jobs) - 1; i >= 0; i-- {
		j := jobs[i]
		if _, ok := byOrg[j.OrgID]; !ok {
			orgs = append(orgs, j.OrgID)
		}
		byOrg[j.OrgID] = append(byOrg[j.OrgID], j)
	}
	out := make([]store.KBJob, 0, len(jobs))
	for round := 0; len(out) < len(jobs); round++ {
		for _, o := range orgs {
			if q := byOrg[o]; round < len(q) {
				out = append(out, q[round])
			}
		}
	}
	return out
}
