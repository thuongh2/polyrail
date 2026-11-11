package gateway

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"regexp"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/polyrail/polyrail/internal/store"
)

// OpenCode Go and Zen route and prompt-cache by conversation: they ask every client to send a
// stable `x-opencode-session` per conversation and to name itself in User-Agent, and refuse
// requests without the session header (https://opencode.ai/docs/console/go/).
var opencodeTypes = map[string]bool{"opencode-go": true, "opencode-zen": true}

const opencodeSessionHeader = "x-opencode-session"

var sessionID = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

// opencodeHeaders sets the session and User-Agent headers on bctx when any target is an
// OpenCode provider. A valid session id from the caller is forwarded as is (clients such as
// OpenCode or Codex already send one); otherwise one is derived from the API key and the
// conversation's opening (seed), so every turn of the same conversation keeps the same id and
// different keys never share one.
func opencodeHeaders(bctx *schemas.BifrostContext, r *http.Request, p *principal, snap *Snapshot, ts []store.Target, seed func() string) {
	if !anyOpencode(snap, ts) {
		return
	}
	id := r.Header.Get(opencodeSessionHeader)
	if !sessionID.MatchString(id) {
		sum := sha256.Sum256([]byte(p.Org.ID + "\x00" + p.Key.ID + "\x00" + seed()))
		id = "polyrail-" + hex.EncodeToString(sum[:16])
	}
	h, _ := bctx.Value(schemas.BifrostContextKeyExtraHeaders).(map[string][]string)
	if h == nil {
		h = map[string][]string{}
	}
	h[opencodeSessionHeader] = []string{id}
	h["User-Agent"] = []string{"Polyrail/" + Version}
	bctx.SetValue(schemas.BifrostContextKeyExtraHeaders, h)
}

// Version is the build's version string, set by main; it names the gateway in User-Agent.
var Version = "dev"

func anyOpencode(snap *Snapshot, ts []store.Target) bool {
	for _, t := range ts {
		if pr, ok := snap.Providers[t.ProviderID]; ok && opencodeTypes[pr.Type] {
			return true
		}
	}
	return false
}

// conversationSeed is the opening of a conversation (its first two messages: usually the system
// prompt and the first user turn), which stays the same on every later turn.
func conversationSeed[T any](msgs []T) string {
	b, _ := json.Marshal(msgs[:min(2, len(msgs))])
	return string(b)
}
