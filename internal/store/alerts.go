package store

import (
	"context"
	"fmt"
	"time"
)

// AlertChannel sends gateway events to a chat or push service through a shoutrrr URL (see
// internal/alert and docs/KEYS.md). OrgID "" is a platform channel.
type AlertChannel struct {
	ID        string    `json:"id"`
	OrgID     string    `json:"org_id"`
	Name      string    `json:"name"`
	URL       string    `json:"-"`       // write-only: plaintext only inside the gateway, sealed at rest
	Service   string    `json:"service"` // the URL's scheme service (telegram, slack, ...), safe to show
	Events    []string  `json:"events"`  // event types, or "*"
	Enabled   bool      `json:"enabled"`
	Failures  int       `json:"failures"` // consecutive failed sends
	CreatedAt time.Time `json:"created_at"`
}

// storedAlertChannel keeps the sealed URL in the persisted form while hiding it from API JSON.
type storedAlertChannel struct {
	AlertChannel
	Sealed string `json:"sealed_url"`
}

// alertKey is a channel's KV key. Like webhooks, writes skip the config version bump: no
// catalog snapshot depends on them and the failure counter changes on failed sends.
func alertKey(orgID, id string) string {
	if orgID == "" {
		orgID = "_"
	}
	return "alert/" + orgID + "/" + id
}

func alertPrefix(orgID string) string {
	if orgID == "" {
		orgID = "_"
	}
	return "alert/" + orgID + "/"
}

func (s *Store) openAlert(sc *storedAlertChannel) (*AlertChannel, error) {
	c := sc.AlertChannel
	var err error
	if c.URL, err = s.box.Open(sc.Sealed); err != nil {
		return nil, fmt.Errorf("alert channel %s: %w", c.ID, err)
	}
	return &c, nil
}

// CreateAlertChannel stores c with its URL sealed. Callers validate URL and events.
func (s *Store) CreateAlertChannel(ctx context.Context, c *AlertChannel) error {
	if c.URL == "" || c.Name == "" {
		return fmt.Errorf("%w: name and url required", ErrInvalid)
	}
	c.ID, c.CreatedAt = NewID(), time.Now().UTC()
	sealed, err := s.box.Seal(c.URL)
	if err != nil {
		return err
	}
	return s.KV.Update(ctx, func(tx Tx) error {
		if c.OrgID != "" {
			if _, err := tx.Get("org/" + c.OrgID); err != nil {
				return err
			}
		}
		return putJSON(tx, alertKey(c.OrgID, c.ID), storedAlertChannel{*c, sealed})
	})
}

// GetAlertChannel returns the channel with its URL decrypted.
func (s *Store) GetAlertChannel(ctx context.Context, orgID, id string) (*AlertChannel, error) {
	sc, err := getJSON[storedAlertChannel](s.get(ctx), alertKey(orgID, id))
	if err != nil {
		return nil, err
	}
	return s.openAlert(sc)
}

// ListAlertChannels returns the channels of an org ("" = platform) with URLs decrypted
// (gateway use; JSON hides them).
func (s *Store) ListAlertChannels(ctx context.Context, orgID string) ([]AlertChannel, error) {
	scs, err := listJSON[storedAlertChannel](ctx, s.KV, alertPrefix(orgID), "", 0)
	if err != nil {
		return nil, err
	}
	out := make([]AlertChannel, 0, len(scs))
	for i := range scs {
		c, err := s.openAlert(&scs[i])
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, nil
}

// UpdateAlertChannel applies fn to the stored channel (its URL is "" inside fn). A URL that fn
// sets replaces the stored one; id, org and created_at cannot change.
func (s *Store) UpdateAlertChannel(ctx context.Context, orgID, id string, fn func(*AlertChannel) error) (*AlertChannel, error) {
	var out *AlertChannel
	err := s.KV.Update(ctx, func(tx Tx) error {
		sc, err := getJSON[storedAlertChannel](tx.Get, alertKey(orgID, id))
		if err != nil {
			return err
		}
		c := sc.AlertChannel
		c.URL = ""
		if err := fn(&c); err != nil {
			return err
		}
		sealed := sc.Sealed
		if c.URL != "" {
			if sealed, err = s.box.Seal(c.URL); err != nil {
				return err
			}
		}
		c.ID, c.OrgID, c.CreatedAt, c.URL = id, sc.OrgID, sc.CreatedAt, ""
		out = &c
		return putJSON(tx, alertKey(orgID, id), storedAlertChannel{c, sealed})
	})
	return out, err
}

func (s *Store) DeleteAlertChannel(ctx context.Context, orgID, id string) error {
	return s.KV.Update(ctx, func(tx Tx) error {
		if _, err := tx.Get(alertKey(orgID, id)); err != nil {
			return err
		}
		return tx.Delete(alertKey(orgID, id))
	})
}
