package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/polyrail/polyrail/internal/store"
)

// OpenCode Go refuses requests without x-opencode-session. The gateway sends one per conversation
// (stable across turns, different per conversation and per key), forwards the caller's own id,
// and names itself in User-Agent; other providers get neither header.
func TestOpencodeSessionHeader(t *testing.T) {
	var hits atomic.Int64
	inner := mockUpstream(t, false, &hits)
	type seen struct{ session, ua string }
	var mu sync.Mutex
	var got []seen
	vendor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got = append(got, seen{r.Header.Get("x-opencode-session"), r.Header.Get("User-Agent")})
		mu.Unlock()
		inner.Config.Handler.ServeHTTP(w, r)
	}))
	t.Cleanup(vendor.Close)
	for _, typ := range []string{"opencode-go", "groq"} {
		old := OpenAICompatible[typ]
		OpenAICompatible[typ] = vendor.URL
		t.Cleanup(func() { OpenAICompatible[typ] = old })
	}
	f := setup(t)
	ctx := context.Background()
	for _, pm := range []struct{ typ, model string }{{"opencode-go", "acme/oc"}, {"groq", "acme/groq"}} {
		p := &store.Provider{OrgID: f.org.ID, Name: pm.typ, Type: pm.typ, Enabled: true, Keys: []store.ProviderKey{{Value: "k"}}}
		if err := f.g.S.PutProvider(ctx, p); err != nil {
			t.Fatal(err)
		}
		m := &store.Model{OrgID: f.org.ID, ID: pm.model, Name: pm.model, Enabled: true, Targets: []store.Target{{ProviderID: p.ID, Model: "m"}}}
		if err := f.g.S.PutModel(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.g.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	chat := func(model, msgs string, hdr ...string) seen {
		t.Helper()
		resp := f.post(t, "/v1/chat/completions", f.key, `{"model":"`+model+`","messages":[`+msgs+`]}`, hdr...)
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("%s: %d", model, resp.StatusCode)
		}
		mu.Lock()
		defer mu.Unlock()
		return got[len(got)-1]
	}
	first := `{"role":"system","content":"be brief"},{"role":"user","content":"plan a trip"}`
	turn1 := chat("acme/oc", first)
	turn2 := chat("acme/oc", first+`,{"role":"assistant","content":"ok"},{"role":"user","content":"and food?"}`)
	other := chat("acme/oc", `{"role":"user","content":"something else"}`)
	if !strings.HasPrefix(turn1.session, "polyrail-") || turn1.session != turn2.session {
		t.Fatalf("one conversation keeps one session: %q then %q", turn1.session, turn2.session)
	}
	if other.session == turn1.session {
		t.Fatal("another conversation must get another session")
	}
	if !strings.HasPrefix(turn1.ua, "Polyrail/") {
		t.Fatalf("User-Agent %q", turn1.ua)
	}
	if fwd := chat("acme/oc", first, "x-opencode-session", "client-sess-1"); fwd.session != "client-sess-1" {
		t.Fatalf("caller's session id is forwarded: %q", fwd.session)
	}
	if bad := chat("acme/oc", first, "x-opencode-session", "bad id with spaces"); bad.session != turn1.session {
		t.Fatalf("an invalid caller id falls back to the derived one: %q", bad.session)
	}
	if g := chat("acme/groq", first); g.session != "" || strings.HasPrefix(g.ua, "Polyrail/") {
		t.Fatalf("non-OpenCode providers get no OpenCode headers: %+v", g)
	}
}
