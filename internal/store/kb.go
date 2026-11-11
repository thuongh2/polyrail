package store

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Knowledge bases (docs/KB.md §1). Records live under kb*/<org>/ and never bump meta/version:
// no catalog snapshot depends on them. Orchestration (chunking, embedding, search) is package kb.

// Limits shared by the store and package kb.
const (
	KBMaxPerOrg     = 20
	KBMaxDocs       = 5000   // per KB
	KBMaxChunks     = 100000 // per KB
	KBMaxQueuedOrg  = 200    // queued docs per org
	KBMaxAttributes = 16
)

// ErrLimit is returned when a knowledge-base limit would be exceeded (kb.ErrLimit is the same value).
var ErrLimit = errors.New("limit exceeded")

// ErrLeased is returned when a KB queue job is leased by another worker (or no longer by this one).
var ErrLeased = errors.New("job leased")

var errNoBox = errors.New("knowledge bases need MASTER_KEY")

// KB is a knowledge base: an org's searchable document collection. Key: kb/<org>/<id>.
type KB struct {
	ID               string    `json:"id"`
	OrgID            string    `json:"org_id"`
	Name             string    `json:"name"`
	Description      string    `json:"description,omitempty"`
	EmbedModel       string    `json:"embedding_model"`      // org-visible kind=embedding model id
	Dimensions       int       `json:"dimensions,omitempty"` // requested embedding size; 0 = native
	ChunkChars       int       `json:"chunk_chars"`
	OverlapChars     int       `json:"overlap_chars"`
	TopK             int       `json:"top_k"`
	MinScore         float64   `json:"min_score"` // cosine floor of the vector leg
	MaxContextTokens int       `json:"max_context_tokens"`
	Rev              int64     `json:"rev"` // bumped whenever the searchable set changes
	Docs             int       `json:"docs"`
	Chunks           int       `json:"chunks"`
	Bytes            int64     `json:"bytes"`
	CreatedByKey     string    `json:"created_by_key,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

// KBDoc is one document of a KB. Key: kbdoc/<org>/<kb>/<id>.
type KBDoc struct {
	ID          string            `json:"id"`
	KBID        string            `json:"kb_id"`
	OrgID       string            `json:"org_id"`
	Filename    string            `json:"filename"`
	Source      string            `json:"source"` // upload | text | url
	URL         string            `json:"url,omitempty"`
	ContentType string            `json:"content_type"`
	Bytes       int64             `json:"bytes"`
	Chars       int               `json:"chars"`
	Chunks      int               `json:"chunks"`
	InTokens    int               `json:"in_tokens"`                 // estimated embedded tokens (chars/4)
	EmbedModel  string            `json:"embedding_model,omitempty"` // model its chunks were embedded with
	Dimensions  int               `json:"dimensions,omitempty"`      // dimensions its chunks were embedded with
	Status      string            `json:"status"`                    // queued | processing | ready | failed
	Error       string            `json:"error,omitempty"`           // safe message, never an upstream body
	Attempts    int               `json:"attempts"`
	KeyID       string            `json:"key_id,omitempty"` // API key that added it; embeddings are billed to it ("" = console)
	Attributes  map[string]string `json:"attributes,omitempty"`
	CreatedAt   time.Time         `json:"created_at"`
	UpdatedAt   time.Time         `json:"updated_at"`
}

// Doc statuses.
const (
	KBQueued     = "queued"
	KBProcessing = "processing"
	KBReady      = "ready"
	KBFailed     = "failed"
)

// KBJob is an ingestion queue entry. Key: kbq/<doc id>: one entry per doc, so a doc is never
// processed by two workers at once; a requeue while leased sets Again instead.
type KBJob struct {
	OrgID      string    `json:"org"`
	KBID       string    `json:"kb"`
	DocID      string    `json:"doc"`
	Owner      string    `json:"owner,omitempty"`
	LeaseUntil time.Time `json:"lease_until"`
	Again      bool      `json:"again,omitempty"` // settings changed while leased: process once more
}

// KBDocResult is what a worker reports when a doc's chunks are written.
type KBDocResult struct {
	Chunks, Chars, InTokens int
	EmbedModel              string
	Dimensions              int
}

var (
	reKBID    = regexp.MustCompile(`^vs_[0-9a-f]{24}$`)
	reKBDocID = regexp.MustCompile(`^vsf_[0-9a-f]{24}$`)
)

func NewKBID() string             { return "vs_" + NewJobID() }
func NewKBDocID() string          { return "vsf_" + NewJobID() }
func ValidKBID(id string) bool    { return reKBID.MatchString(id) }
func ValidKBDocID(id string) bool { return reKBDocID.MatchString(id) }

func kbKey(org, id string) string         { return "kb/" + org + "/" + id }
func kbDocKey(org, kb, doc string) string { return "kbdoc/" + org + "/" + kb + "/" + doc }
func kbSrcKey(org, kb, doc string) string { return "kbsrc/" + org + "/" + kb + "/" + doc }
func kbJobKey(doc string) string          { return "kbq/" + doc }
func kbDocPrefix(org, kb string) string   { return "kbdoc/" + org + "/" + kb + "/" }
func kbOrgPrefixes(org string) []string {
	return []string{"kb/" + org + "/", "kbdoc/" + org + "/", "kbsrc/" + org + "/", "kbchunk/" + org + "/"}
}
func validKBRef(org, kb string) bool {
	return org != "" && !strings.Contains(org, "/") && ValidKBID(kb)
}
func validKBDocRef(org, kb, doc string) bool { return validKBRef(org, kb) && ValidKBDocID(doc) }

// KBChunkPrefix is the key prefix of a doc's chunks, or of all a KB's chunks when doc is "".
func KBChunkPrefix(org, kb, doc string) string {
	if doc == "" {
		return "kbchunk/" + org + "/" + kb + "/"
	}
	return "kbchunk/" + org + "/" + kb + "/" + doc + "/"
}

// KBChunkKey is the key of one chunk record.
func KBChunkKey(org, kb, doc string, seq int) string {
	return fmt.Sprintf("%s%06d", KBChunkPrefix(org, kb, doc), seq)
}

// DeletePrefix removes every key with the given prefix.
func (s *Store) DeletePrefix(ctx context.Context, prefix string) error {
	return s.KV.DeleteRange(ctx, prefix, prefixEnd(prefix))
}

// Box returns the at-rest cipher (nil when the store was opened without one).
func (s *Store) Box() *Box { return s.box }

// CreateKB stores a new KB; the caller validates settings. Fails with ErrLimit past KBMaxPerOrg.
func (s *Store) CreateKB(ctx context.Context, k *KB) error {
	if k.OrgID == "" || strings.Contains(k.OrgID, "/") {
		return fmt.Errorf("%w: org required", ErrInvalid)
	}
	existing, err := s.KV.Scan(ctx, "kb/"+k.OrgID+"/", "", KBMaxPerOrg)
	if err != nil {
		return err
	}
	if len(existing) >= KBMaxPerOrg {
		return fmt.Errorf("%w: at most %d knowledge bases per org", ErrLimit, KBMaxPerOrg)
	}
	k.ID = NewKBID()
	k.CreatedAt = time.Now().UTC()
	k.UpdatedAt, k.Rev, k.Docs, k.Chunks, k.Bytes = k.CreatedAt, 1, 0, 0, 0
	return s.KV.Update(ctx, func(tx Tx) error {
		if _, err := tx.Get("org/" + k.OrgID); err != nil {
			return err
		}
		return putJSON(tx, kbKey(k.OrgID, k.ID), k)
	})
}

// GetKB returns ErrNotFound for unknown ids and ids of other orgs.
func (s *Store) GetKB(ctx context.Context, org, id string) (*KB, error) {
	if !validKBRef(org, id) {
		return nil, ErrNotFound
	}
	return getJSON[KB](s.get(ctx), kbKey(org, id))
}

// ListKBs returns an org's KBs, newest first.
func (s *Store) ListKBs(ctx context.Context, org string) ([]KB, error) {
	return listJSON[KB](ctx, s.KV, "kb/"+org+"/", "", 0)
}

// UpdateKB applies fn atomically; identity and counters are kept and Rev can only grow.
func (s *Store) UpdateKB(ctx context.Context, org, id string, fn func(*KB) error) (*KB, error) {
	if !validKBRef(org, id) {
		return nil, ErrNotFound
	}
	var out *KB
	err := s.KV.Update(ctx, func(tx Tx) error {
		cur, err := getJSON[KB](tx.Get, kbKey(org, id))
		if err != nil {
			return err
		}
		k := *cur
		if err := fn(&k); err != nil {
			return err
		}
		k.ID, k.OrgID, k.CreatedAt, k.CreatedByKey = cur.ID, cur.OrgID, cur.CreatedAt, cur.CreatedByKey
		k.Rev, k.Docs, k.Chunks, k.Bytes = max(k.Rev, cur.Rev), cur.Docs, cur.Chunks, cur.Bytes
		k.UpdatedAt, out = time.Now().UTC(), &k
		return putJSON(tx, kbKey(org, id), &k)
	})
	return out, err
}

// DeleteKB removes a KB with its docs, sources, chunks and queue entries.
func (s *Store) DeleteKB(ctx context.Context, org, id string) error {
	if !validKBRef(org, id) {
		return ErrNotFound
	}
	err := s.KV.Update(ctx, func(tx Tx) error {
		if _, err := tx.Get(kbKey(org, id)); err != nil {
			return err
		}
		return tx.Delete(kbKey(org, id))
	})
	if err != nil {
		return err
	}
	// Workers drop queue entries whose KB is gone; delete them here too so the queue stays short.
	jobs, err := s.ListKBJobs(ctx, 0)
	if err != nil {
		return err
	}
	err = s.KV.Update(ctx, func(tx Tx) error {
		for _, j := range jobs {
			if j.OrgID == org && j.KBID == id {
				if err := tx.Delete(kbJobKey(j.DocID)); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, p := range []string{kbDocPrefix(org, id), "kbsrc/" + org + "/" + id + "/", KBChunkPrefix(org, id, "")} {
		if err := s.DeletePrefix(ctx, p); err != nil {
			return err
		}
	}
	return nil
}

// AddKBDoc stores a new doc (ID, KBID, OrgID set by the caller), its sealed source text and its
// queue entry in one transaction, and counts it on the KB.
func (s *Store) AddKBDoc(ctx context.Context, d *KBDoc, text string) error {
	if !validKBDocRef(d.OrgID, d.KBID, d.ID) {
		return fmt.Errorf("%w: doc ids", ErrInvalid)
	}
	if s.box == nil {
		return errNoBox
	}
	sealed, err := s.box.Seal(text)
	if err != nil {
		return err
	}
	queued, err := s.countQueued(ctx, d.OrgID)
	if err != nil {
		return err
	}
	if queued >= KBMaxQueuedOrg {
		return fmt.Errorf("%w: at most %d documents queued per org", ErrLimit, KBMaxQueuedOrg)
	}
	now := time.Now().UTC()
	d.Status, d.Attempts, d.Error, d.CreatedAt, d.UpdatedAt = KBQueued, 0, "", now, now
	return s.KV.Update(ctx, func(tx Tx) error {
		k, err := getJSON[KB](tx.Get, kbKey(d.OrgID, d.KBID))
		if err != nil {
			return err
		}
		if k.Docs >= KBMaxDocs {
			return fmt.Errorf("%w: at most %d documents per knowledge base", ErrLimit, KBMaxDocs)
		}
		k.Docs++
		k.Bytes += d.Bytes
		k.UpdatedAt = now
		if err := putJSON(tx, kbKey(d.OrgID, d.KBID), k); err != nil {
			return err
		}
		if err := tx.Put(kbSrcKey(d.OrgID, d.KBID, d.ID), []byte(sealed)); err != nil {
			return err
		}
		if err := putJSON(tx, kbDocKey(d.OrgID, d.KBID, d.ID), d); err != nil {
			return err
		}
		return putJSON(tx, kbJobKey(d.ID), KBJob{OrgID: d.OrgID, KBID: d.KBID, DocID: d.ID})
	})
}

// countQueued counts an org's queue entries.
// shortcut: scans the whole queue; fine while the queue is short (200/org cap), index by org if not.
func (s *Store) countQueued(ctx context.Context, org string) (int, error) {
	jobs, err := s.ListKBJobs(ctx, 0)
	n := 0
	for _, j := range jobs {
		if j.OrgID == org {
			n++
		}
	}
	return n, err
}

// GetKBDoc returns ErrNotFound for unknown ids and ids of other orgs or KBs.
func (s *Store) GetKBDoc(ctx context.Context, org, kb, doc string) (*KBDoc, error) {
	if !validKBDocRef(org, kb, doc) {
		return nil, ErrNotFound
	}
	return getJSON[KBDoc](s.get(ctx), kbDocKey(org, kb, doc))
}

// ListKBDocs returns a KB's docs, newest first, after the given doc id (limit 0 = all).
func (s *Store) ListKBDocs(ctx context.Context, org, kb, after string, limit int) ([]KBDoc, error) {
	if !validKBRef(org, kb) {
		return nil, ErrNotFound
	}
	if after != "" {
		after = kbDocKey(org, kb, after)
	}
	return listJSON[KBDoc](ctx, s.KV, kbDocPrefix(org, kb), after, limit)
}

// GetKBSource returns a doc's extracted text, decrypted.
func (s *Store) GetKBSource(ctx context.Context, org, kb, doc string) (string, error) {
	if !validKBDocRef(org, kb, doc) {
		return "", ErrNotFound
	}
	raw, err := s.KV.Get(ctx, kbSrcKey(org, kb, doc))
	if err != nil {
		return "", err
	}
	if s.box == nil {
		return "", errNoBox
	}
	return s.box.Open(string(raw))
}

// DeleteKBDoc removes a doc's record, source and queue entry, uncounts it and bumps the KB's Rev.
// Chunks are removed by the vector store (kb.VectorStore.DeleteDoc).
func (s *Store) DeleteKBDoc(ctx context.Context, org, kb, doc string) error {
	if !validKBDocRef(org, kb, doc) {
		return ErrNotFound
	}
	return s.KV.Update(ctx, func(tx Tx) error {
		d, err := getJSON[KBDoc](tx.Get, kbDocKey(org, kb, doc))
		if err != nil {
			return err
		}
		k, err := getJSON[KB](tx.Get, kbKey(org, kb))
		if err != nil {
			return err
		}
		k.Docs--
		k.Chunks -= d.Chunks
		k.Bytes -= d.Bytes
		k.Rev++
		k.UpdatedAt = time.Now().UTC()
		if err := putJSON(tx, kbKey(org, kb), k); err != nil {
			return err
		}
		for _, key := range []string{kbDocKey(org, kb, doc), kbSrcKey(org, kb, doc), kbJobKey(doc)} {
			if err := tx.Delete(key); err != nil {
				return err
			}
		}
		return nil
	})
}

// RequeueKBDoc queues a doc for processing again (settings change, reindex). If a worker holds
// it, the job is flagged to run once more after the current run.
func (s *Store) RequeueKBDoc(ctx context.Context, org, kb, doc string) error {
	if !validKBDocRef(org, kb, doc) {
		return ErrNotFound
	}
	now := time.Now().UTC()
	return s.KV.Update(ctx, func(tx Tx) error {
		d, err := getJSON[KBDoc](tx.Get, kbDocKey(org, kb, doc))
		if err != nil {
			return err
		}
		j, err := getJSON[KBJob](tx.Get, kbJobKey(doc))
		switch {
		case errors.Is(err, ErrNotFound):
			j = &KBJob{OrgID: org, KBID: kb, DocID: doc}
		case err != nil:
			return err
		case j.LeaseUntil.After(now):
			j.Again = true
		}
		if err := putJSON(tx, kbJobKey(doc), j); err != nil {
			return err
		}
		if d.Status != KBProcessing {
			d.Status = KBQueued
		}
		d.Attempts, d.Error, d.UpdatedAt = 0, "", now
		return putJSON(tx, kbDocKey(org, kb, doc), d)
	})
}

// ListKBJobs returns queue entries in doc-id order (oldest doc first), up to limit (0 = all).
func (s *Store) ListKBJobs(ctx context.Context, limit int) ([]KBJob, error) {
	return listJSON[KBJob](ctx, s.KV, "kbq/", "", limit)
}

// ClaimKBJob leases the doc's queue entry to owner if no lease is live, bumping the doc's
// Attempts and marking it processing. A doc that already used maxAttempts is marked failed and
// its entry dropped (ErrNotFound is returned, as for entries whose doc or KB is gone).
func (s *Store) ClaimKBJob(ctx context.Context, doc, owner string, lease time.Duration, maxAttempts int) (*KBJob, *KBDoc, error) {
	var job *KBJob
	var out *KBDoc
	now := time.Now().UTC()
	err := s.KV.Update(ctx, func(tx Tx) error {
		j, err := getJSON[KBJob](tx.Get, kbJobKey(doc))
		if err != nil {
			return err
		}
		if j.LeaseUntil.After(now) {
			return ErrLeased
		}
		d, err := getJSON[KBDoc](tx.Get, kbDocKey(j.OrgID, j.KBID, doc))
		if errors.Is(err, ErrNotFound) {
			return tx.Delete(kbJobKey(doc))
		}
		if err != nil {
			return err
		}
		if d.Attempts >= maxAttempts {
			d.Status, d.UpdatedAt = KBFailed, now
			if d.Error == "" {
				d.Error = "processing failed after " + strconv.Itoa(maxAttempts) + " attempts"
			}
			if err := putJSON(tx, kbDocKey(j.OrgID, j.KBID, doc), d); err != nil {
				return err
			}
			return tx.Delete(kbJobKey(doc))
		}
		d.Attempts++
		d.Status, d.UpdatedAt = KBProcessing, now
		j.Owner, j.LeaseUntil, j.Again = owner, now.Add(lease), false
		if err := putJSON(tx, kbDocKey(j.OrgID, j.KBID, doc), d); err != nil {
			return err
		}
		job, out = j, d
		return putJSON(tx, kbJobKey(doc), j)
	})
	if err == nil && job == nil {
		err = ErrNotFound
	}
	return job, out, err
}

// heldJob reads the doc's job inside tx and checks owner still holds its lease.
func heldJob(tx Tx, doc, owner string) (*KBJob, error) {
	j, err := getJSON[KBJob](tx.Get, kbJobKey(doc))
	if errors.Is(err, ErrNotFound) {
		return nil, ErrLeased
	}
	if err != nil {
		return nil, err
	}
	if j.Owner != owner || !j.LeaseUntil.After(time.Now()) {
		return nil, ErrLeased
	}
	return j, nil
}

// RenewKBJob extends owner's lease; ErrLeased means the lease was lost (expired or deleted).
func (s *Store) RenewKBJob(ctx context.Context, doc, owner string, lease time.Duration) error {
	return s.KV.Update(ctx, func(tx Tx) error {
		j, err := heldJob(tx, doc, owner)
		if err != nil {
			return err
		}
		j.LeaseUntil = time.Now().UTC().Add(lease)
		return putJSON(tx, kbJobKey(doc), j)
	})
}

// FinishKBJob records a successful run: the doc becomes ready (or queued again when a requeue
// arrived meanwhile), the KB's counts change and its Rev is bumped. ErrNotFound means the doc or
// KB was deleted meanwhile (the caller removes the chunks it wrote); ErrLeased means the lease was lost.
func (s *Store) FinishKBJob(ctx context.Context, doc, owner string, r KBDocResult) error {
	now := time.Now().UTC()
	return s.KV.Update(ctx, func(tx Tx) error {
		j, err := heldJob(tx, doc, owner)
		if err != nil {
			return err
		}
		d, err := getJSON[KBDoc](tx.Get, kbDocKey(j.OrgID, j.KBID, doc))
		if err != nil {
			return err
		}
		k, err := getJSON[KB](tx.Get, kbKey(j.OrgID, j.KBID))
		if err != nil {
			return err
		}
		k.Chunks += r.Chunks - d.Chunks
		k.Rev++
		k.UpdatedAt = now
		d.Chunks, d.Chars, d.InTokens, d.EmbedModel, d.Dimensions = r.Chunks, r.Chars, r.InTokens, r.EmbedModel, r.Dimensions
		d.Error, d.UpdatedAt = "", now
		if j.Again {
			d.Status, d.Attempts = KBQueued, 0
			j.Again, j.Owner, j.LeaseUntil = false, "", time.Time{}
			err = putJSON(tx, kbJobKey(doc), j)
		} else {
			d.Status = KBReady
			err = tx.Delete(kbJobKey(doc))
		}
		if err != nil {
			return err
		}
		if err := putJSON(tx, kbKey(j.OrgID, j.KBID), k); err != nil {
			return err
		}
		return putJSON(tx, kbDocKey(j.OrgID, j.KBID, doc), d)
	})
}

// FailKBJob records a failed run with a safe message. Permanent failures (and a failed last
// attempt) mark the doc failed and drop the entry; others release the lease for a retry after
// retryAfter. The doc's previous chunks are untouched.
func (s *Store) FailKBJob(ctx context.Context, doc, owner, msg string, permanent bool, maxAttempts int, retryAfter time.Duration) error {
	now := time.Now().UTC()
	return s.KV.Update(ctx, func(tx Tx) error {
		j, err := heldJob(tx, doc, owner)
		if err != nil {
			return err
		}
		d, err := getJSON[KBDoc](tx.Get, kbDocKey(j.OrgID, j.KBID, doc))
		if errors.Is(err, ErrNotFound) {
			return tx.Delete(kbJobKey(doc))
		}
		if err != nil {
			return err
		}
		d.Error, d.UpdatedAt = msg, now
		if (permanent || d.Attempts >= maxAttempts) && !j.Again {
			d.Status = KBFailed
			err = tx.Delete(kbJobKey(doc))
		} else {
			if j.Again {
				d.Attempts = 0
			}
			d.Status = KBQueued
			j.Owner, j.LeaseUntil, j.Again = "", now.Add(retryAfter), false
			err = putJSON(tx, kbJobKey(doc), j)
		}
		if err != nil {
			return err
		}
		return putJSON(tx, kbDocKey(j.OrgID, j.KBID, doc), d)
	})
}
