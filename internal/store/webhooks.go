package store

import (
	"context"
	"fmt"
	"time"
)

// Webhook is an org's HTTP endpoint for gateway events (see internal/webhook).
type Webhook struct {
	ID        string    `json:"id"`
	OrgID     string    `json:"org_id"`
	URL       string    `json:"url"`
	Events    []string  `json:"events"` // event types, or "*"
	Secret    string    `json:"-"`      // signing secret; plaintext only inside the gateway, sealed at rest
	Enabled   bool      `json:"enabled"`
	Failures  int       `json:"failures"` // consecutive failed deliveries
	CreatedAt time.Time `json:"created_at"`
}

// storedWebhook keeps the sealed secret in the persisted form while hiding it from API JSON.
type storedWebhook struct {
	Webhook
	Sealed string `json:"sealed_secret"`
}

// hookKey is a webhook's KV key. Webhook writes skip the config version bump: no catalog
// snapshot depends on them, and the failure counter changes on every failed delivery.
func hookKey(orgID, id string) string { return "hook/" + orgID + "/" + id }

func (s *Store) seal(w *Webhook) (storedWebhook, error) {
	sealed, err := s.box.Seal(w.Secret)
	return storedWebhook{*w, sealed}, err
}

func (s *Store) open(sw *storedWebhook) (*Webhook, error) {
	w := sw.Webhook
	var err error
	if w.Secret, err = s.box.Open(sw.Sealed); err != nil {
		return nil, fmt.Errorf("webhook %s: %w", w.ID, err)
	}
	return &w, nil
}

// CreateWebhook stores w with its plaintext Secret sealed. Callers validate URL and events.
func (s *Store) CreateWebhook(ctx context.Context, w *Webhook) error {
	if w.OrgID == "" || w.URL == "" || w.Secret == "" {
		return fmt.Errorf("%w: org, url and secret required", ErrInvalid)
	}
	w.ID, w.CreatedAt = NewID(), time.Now().UTC()
	sw, err := s.seal(w)
	if err != nil {
		return err
	}
	return s.KV.Update(ctx, func(tx Tx) error {
		if _, err := tx.Get("org/" + w.OrgID); err != nil {
			return err
		}
		return putJSON(tx, hookKey(w.OrgID, w.ID), sw)
	})
}

// GetWebhook returns the webhook with its secret decrypted.
func (s *Store) GetWebhook(ctx context.Context, orgID, id string) (*Webhook, error) {
	sw, err := getJSON[storedWebhook](s.get(ctx), hookKey(orgID, id))
	if err != nil {
		return nil, err
	}
	return s.open(sw)
}

// ListWebhooks returns an org's webhooks with secrets decrypted (gateway use; JSON hides them).
func (s *Store) ListWebhooks(ctx context.Context, orgID string) ([]Webhook, error) {
	sws, err := listJSON[storedWebhook](ctx, s.KV, "hook/"+orgID+"/", "", 0)
	if err != nil {
		return nil, err
	}
	out := make([]Webhook, 0, len(sws))
	for i := range sws {
		w, err := s.open(&sws[i])
		if err != nil {
			return nil, err
		}
		out = append(out, *w)
	}
	return out, nil
}

// UpdateWebhook applies fn to the stored webhook; id, org and secret cannot change.
func (s *Store) UpdateWebhook(ctx context.Context, orgID, id string, fn func(*Webhook) error) (*Webhook, error) {
	var out *Webhook
	err := s.KV.Update(ctx, func(tx Tx) error {
		sw, err := getJSON[storedWebhook](tx.Get, hookKey(orgID, id))
		if err != nil {
			return err
		}
		w := sw.Webhook
		if err := fn(&w); err != nil {
			return err
		}
		w.ID, w.OrgID, w.CreatedAt, w.Secret = id, orgID, sw.CreatedAt, ""
		out = &w
		return putJSON(tx, hookKey(orgID, id), storedWebhook{w, sw.Sealed})
	})
	return out, err
}

func (s *Store) DeleteWebhook(ctx context.Context, orgID, id string) error {
	return s.KV.Update(ctx, func(tx Tx) error {
		if _, err := tx.Get(hookKey(orgID, id)); err != nil {
			return err
		}
		return tx.Delete(hookKey(orgID, id))
	})
}
