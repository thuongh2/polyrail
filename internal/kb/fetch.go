package kb

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/polyrail/polyrail/internal/store"
)

const (
	fetchTimeout   = 15 * time.Second
	fetchMaxBytes  = 5 << 20
	fetchRedirects = 3
)

// fetched is a downloaded URL document.
type fetched struct {
	filename, contentType string
	data                  []byte
}

// fetchURL downloads a tenant-supplied URL: the URL and every redirect pass cfg.CheckURL, the
// connection goes through cfg.DialContext (which refuses private addresses), at most 3 redirects,
// 15 s, 5 MB and supported content types only. Errors never include the response body.
func (s *Service) fetchURL(ctx context.Context, raw string) (*fetched, error) {
	if s.cfg.CheckURL == nil || s.cfg.DialContext == nil {
		return nil, fmt.Errorf("%w: URL ingestion is not configured", ErrUnsupported)
	}
	if len(raw) > 2048 {
		return nil, fmt.Errorf("%w: url too long", store.ErrInvalid)
	}
	if err := s.cfg.CheckURL(raw); err != nil {
		return nil, fmt.Errorf("%w: url: %v", store.ErrInvalid, err)
	}
	client := &http.Client{
		Timeout: fetchTimeout,
		Transport: &http.Transport{
			Proxy:                  nil,
			DialContext:            s.cfg.DialContext,
			TLSHandshakeTimeout:    10 * time.Second,
			ResponseHeaderTimeout:  fetchTimeout,
			MaxResponseHeaderBytes: 64 << 10,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > fetchRedirects {
				return errors.New("too many redirects")
			}
			return s.cfg.CheckURL(req.URL.String())
		},
	}
	defer client.CloseIdleConnections()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: url", store.ErrInvalid)
	}
	req.Header.Set("User-Agent", "Polyrail")
	req.Header.Set("Accept", "text/html, text/markdown, text/plain, text/csv, application/json")
	resp, err := client.Do(req)
	if err != nil {
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			return nil, fmt.Errorf("%w: fetch timed out", store.ErrInvalid)
		}
		return nil, fmt.Errorf("%w: fetch failed", store.ErrInvalid) // no detail: could leak internal topology
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: fetch failed: HTTP %d", store.ErrInvalid, resp.StatusCode)
	}
	name := urlFilename(resp.Request.URL)
	ct := resp.Header.Get("Content-Type")
	if _, err := mediaType(ct, name); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, fetchMaxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%w: fetch failed", store.ErrInvalid)
	}
	if len(data) > fetchMaxBytes {
		return nil, fmt.Errorf("%w: fetched document over %d MB", ErrLimit, fetchMaxBytes>>20)
	}
	return &fetched{filename: name, contentType: ct, data: data}, nil
}

// urlFilename names a fetched doc after the last path segment, else the host.
func urlFilename(u *url.URL) string {
	if b := path.Base(u.Path); b != "" && b != "/" && b != "." {
		return b
	}
	return strings.TrimPrefix(u.Hostname(), "www.")
}
