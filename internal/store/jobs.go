package store

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"
)

// Job kinds: async objects that live at a provider (files, batches, videos). Upstream ids are
// global per provider account, so the gateway owns the mapping: a job is stored under its org and
// only reachable through the gateway id, never through the upstream id.
const (
	JobFile  = "files"
	JobBatch = "batches"
	JobVideo = "videos"
)

// ErrBilled is returned by ClaimBilling when the job was billed already.
var ErrBilled = errors.New("already billed")

// Job is one tenant-owned async object at a provider. Key: <kind>/<org>/<id>.
type Job struct {
	ID         string `json:"id"`
	OrgID      string `json:"org_id"`
	KeyID      string `json:"key_id"` // API key that created it (billing attribution)
	ProviderID string `json:"provider_id"`
	ProvKeyID  string `json:"provider_key_id"` // provider key the upstream object belongs to
	UpstreamID string `json:"upstream_id"`
	Model      string `json:"model"`          // catalog model id
	ModelOrg   string `json:"model_org"`      // owner of the model ("" = public)
	Upstream   string `json:"upstream_model"` // upstream model name at the provider
	// Prices copied from the model at creation, so a later price or model change cannot alter the bill.
	InputPrice       int64 `json:"input_price_micros,omitempty"`
	OutputPrice      int64 `json:"output_price_micros,omitempty"`
	VideoSecondPrice int64 `json:"video_second_price_micros,omitempty"`

	// files
	Purpose  string `json:"purpose,omitempty"`
	Filename string `json:"filename,omitempty"`
	Bytes    int64  `json:"bytes,omitempty"`
	// batches
	InputFileID  string `json:"input_file_id,omitempty"` // gateway file ids
	OutputFileID string `json:"output_file_id,omitempty"`
	ErrorFileID  string `json:"error_file_id,omitempty"`

	Status     string    `json:"status,omitempty"` // last status seen upstream
	Billed     bool      `json:"billed,omitempty"`
	CostMicros int64     `json:"cost_micros,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
}

var reJobID = regexp.MustCompile(`^[0-9a-f]{24}$`)

// ValidJobID reports whether id has the shape NewJobID makes (keeps ids out of key paths otherwise).
func ValidJobID(id string) bool { return reJobID.MatchString(id) }

// NewJobID returns an id that sorts newest first: inverted nanoseconds then random bits.
func NewJobID() string {
	return fmt.Sprintf("%016x", uint64(1<<63-1-time.Now().UnixNano())) + NewID()[:8]
}

func jobKey(kind, org, id string) string { return kind + "/" + org + "/" + id }

func validJob(kind, org, id string) error {
	if (kind != JobFile && kind != JobBatch && kind != JobVideo) || org == "" || !ValidJobID(id) {
		return fmt.Errorf("%w: job kind, org and id", ErrInvalid)
	}
	return nil
}

// PutJob creates a job; ID, OrgID and Kind must be set (ID from NewJobID).
func (s *Store) PutJob(ctx context.Context, kind string, j *Job) error {
	if err := validJob(kind, j.OrgID, j.ID); err != nil {
		return err
	}
	if j.CreatedAt.IsZero() {
		j.CreatedAt = time.Now().UTC()
	}
	return s.KV.Update(ctx, func(tx Tx) error { return putJSON(tx, jobKey(kind, j.OrgID, j.ID), j) })
}

// GetJob returns ErrNotFound for unknown ids and for ids of other orgs.
func (s *Store) GetJob(ctx context.Context, kind, org, id string) (*Job, error) {
	if validJob(kind, org, id) != nil {
		return nil, ErrNotFound
	}
	return getJSON[Job](s.get(ctx), jobKey(kind, org, id))
}

// ListJobs returns an org's jobs of one kind, newest first, after the given id.
func (s *Store) ListJobs(ctx context.Context, kind, org, after string, limit int) ([]Job, error) {
	if after != "" {
		after = jobKey(kind, org, after)
	}
	return listJSON[Job](ctx, s.KV, kind+"/"+org+"/", after, limit)
}

// UpdateJob changes a job atomically.
func (s *Store) UpdateJob(ctx context.Context, kind, org, id string, fn func(*Job) error) (*Job, error) {
	if validJob(kind, org, id) != nil {
		return nil, ErrNotFound
	}
	var out *Job
	err := s.KV.Update(ctx, func(tx Tx) error {
		j, err := getJSON[Job](tx.Get, jobKey(kind, org, id))
		if err != nil {
			return err
		}
		if err := fn(j); err != nil {
			return err
		}
		j.ID, j.OrgID, out = id, org, j
		return putJSON(tx, jobKey(kind, org, id), j)
	})
	return out, err
}

// ClaimBilling marks a job billed with the given cost. Exactly one caller gets nil; every other
// (concurrent or later) caller gets ErrBilled.
func (s *Store) ClaimBilling(ctx context.Context, kind, org, id string, costMicros int64) error {
	_, err := s.UpdateJob(ctx, kind, org, id, func(j *Job) error {
		if j.Billed {
			return ErrBilled
		}
		j.Billed, j.CostMicros = true, costMicros
		return nil
	})
	return err
}

func (s *Store) DeleteJob(ctx context.Context, kind, org, id string) error {
	if validJob(kind, org, id) != nil {
		return ErrNotFound
	}
	return s.KV.Update(ctx, func(tx Tx) error { return tx.Delete(jobKey(kind, org, id)) })
}
