package gateway

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"time"
)

const idemTTL = 24 * time.Hour

// idemEntry is one Idempotency-Key: in flight until done, then the stored 2xx response.
type idemEntry struct {
	hash   [32]byte // request body
	done   bool
	status int
	header http.Header // Content-Type and x-gateway-provider
	body   []byte
}

// idemStore holds Idempotency-Key results. The zero value is ready to use.
// shortcut: per-node memory; a retry routed to another node runs again. Upgrade to Redis for multi-node.
type idemStore struct {
	mu sync.Mutex // makes check-and-claim atomic
	c  *ttlCache[*idemEntry]
}

func (s *idemStore) cache() *ttlCache[*idemEntry] {
	if s.c == nil {
		s.c = newTTLCache[*idemEntry](100_000, 256<<20)
	}
	return s.c
}

// recorder passes the response through and keeps a copy of it.
type recorder struct {
	http.ResponseWriter
	status int
	buf    bytes.Buffer
}

func (r *recorder) WriteHeader(code int) {
	if r.status == 0 {
		r.status = code
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *recorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	r.buf.Write(b)
	return r.ResponseWriter.Write(b)
}

// Idempotent wraps a non-stream JSON POST handler with Idempotency-Key support, scoped per API
// key, method and path: a 2xx response is stored for 24 h and replayed (not billed again) with
// Idempotent-Replayed: true. The same key with another body is 422; while the first request is
// still running, 409. Errors are not stored, so a retry runs again. Streams ignore the header.
func (g *Gateway) Idempotent(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ik := r.Header.Get("Idempotency-Key")
		if ik == "" {
			h(w, r)
			return
		}
		if len(ik) > 255 {
			writeError(w, errf(400, "invalid_request_error", "invalid_idempotency_key", "Idempotency-Key must be at most 255 characters"))
			return
		}
		p, e := g.authenticate(r.Context(), r)
		if e != nil {
			writeError(w, e)
			return
		}
		body, e := g.readBody(w, r)
		if e != nil {
			writeError(w, e)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		var s struct {
			Stream bool `json:"stream"`
		}
		if json.Unmarshal(body, &s) == nil && s.Stream {
			h(w, r)
			return
		}
		key := p.Key.ID + "\x00" + r.Method + "\x00" + r.URL.Path + "\x00" + ik
		hash := sha256.Sum256(body)
		c := g.idem.cache()
		g.idem.mu.Lock()
		prev, ok := c.Get(key)
		if !ok {
			c.Set(key, &idemEntry{hash: hash}, len(key), idemTTL) // claim
		}
		g.idem.mu.Unlock()
		switch {
		case ok && prev.hash != hash:
			writeError(w, errf(422, "invalid_request_error", "idempotency_key_reuse", "Idempotency-Key was already used with a different request body"))
			return
		case ok && !prev.done:
			writeError(w, errf(409, "invalid_request_error", "idempotency_key_in_progress", "a request with this Idempotency-Key is still in progress"))
			return
		case ok:
			for k, v := range prev.header {
				w.Header()[k] = v
			}
			w.Header().Set("Idempotent-Replayed", "true")
			w.WriteHeader(prev.status)
			_, _ = w.Write(prev.body)
			return
		}
		rec := &recorder{ResponseWriter: w}
		defer func() {
			g.idem.mu.Lock()
			defer g.idem.mu.Unlock()
			c.Delete(key) // release the claim; Set below may refuse an oversized body
			if rec.status >= 200 && rec.status < 300 {
				hdr := http.Header{}
				for _, k := range []string{"Content-Type", "x-gateway-provider"} {
					if v := w.Header().Get(k); v != "" {
						hdr.Set(k, v)
					}
				}
				c.Set(key, &idemEntry{hash: hash, done: true, status: rec.status, header: hdr, body: rec.buf.Bytes()}, len(key)+rec.buf.Len(), idemTTL)
			}
		}()
		h(rec, r)
	}
}
