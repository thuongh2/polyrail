package gateway

import (
	"context"
	"testing"

	"github.com/polyrail/polyrail/internal/store"
)

// M15: the response cache and the semantic cache are per workspace, so a key of one workspace
// never receives an answer produced for another workspace's prompt.
func TestCachesArePerWorkspace(t *testing.T) {
	f := setupWS(t)
	hdr := []string{"x-gateway-cache", "60"}
	body := `{"model":"test/ok","messages":[{"role":"user","content":"ws secret"}]}`
	f.post(t, "/v1/chat/completions", f.keyA, body, hdr...).Body.Close()
	r := f.post(t, "/v1/chat/completions", f.keyA, body, hdr...)
	r.Body.Close()
	if r.Header.Get("x-gateway-cache-hit") != "true" {
		t.Fatal("same workspace: cache miss")
	}
	r = f.post(t, "/v1/chat/completions", f.keyB, body, hdr...)
	r.Body.Close()
	if r.Header.Get("x-gateway-cache-hit") == "true" {
		t.Fatal("WS-B key got WS-A's cached response")
	}

	s := setupSem(t, false, &store.SemanticCache{Model: "sem/emb"})
	ctx := context.Background()
	w := &store.Workspace{OrgID: s.org.ID, Name: "W", Slug: "w"}
	if err := s.g.S.CreateWorkspace(ctx, w); err != nil {
		t.Fatal(err)
	}
	wkey := KeyPrefix + "semW"
	if err := s.g.S.CreateKey(ctx, &store.APIKey{OrgID: s.org.ID, WorkspaceID: w.ID, Name: "w"}, HashKey(wkey)); err != nil {
		t.Fatal(err)
	}
	s.ask(t, "tell me about cat", temp0)
	if _, r := s.ask(t, "tell me about cat", temp0); r.Header.Get("X-GW-Cache") != "semantic" {
		t.Fatal("same workspace: semantic miss")
	}
	r = s.post(t, "/v1/chat/completions", wkey, `{"model":"sem/chat","temperature":0,"messages":[{"role":"system","content":"be brief"},{"role":"user","content":"tell me about cat"}]}`)
	r.Body.Close()
	if r.StatusCode != 200 || r.Header.Get("X-GW-Cache") != "" {
		t.Fatalf("other workspace: status %d, X-GW-Cache %q", r.StatusCode, r.Header.Get("X-GW-Cache"))
	}
}

// M16: a policy of a non-default workspace assigned to members reaches only those members'
// keys in that workspace; default-workspace (org admin) policies reach their keys everywhere.
func TestMemberPolicyStaysInItsWorkspace(t *testing.T) {
	k := store.APIKey{ID: "k1", OrgID: "o", CreatedBy: "u1", WorkspaceID: "wsB"}
	wsA := store.Policy{ID: "p1", Name: "ws-a", WorkspaceID: "wsA", MemberIDs: []string{"u1"}, AllowedModels: []string{"x/*"}}
	if ep, err := compilePolicy(k, []store.Policy{wsA}); err != nil || ep != nil {
		t.Fatalf("WS-A member policy applies to the member's WS-B key: %+v %v", ep, err)
	}
	k.WorkspaceID = "wsA"
	if ep, _ := compilePolicy(k, []store.Policy{wsA}); ep == nil {
		t.Fatal("WS-A member policy does not apply to the member's WS-A key")
	}
	org := store.Policy{ID: "p2", Name: "org", MemberIDs: []string{"u1"}, AllowedModels: []string{"x/*"}}
	k.WorkspaceID = "wsB"
	if ep, _ := compilePolicy(k, []store.Policy{org}); ep == nil {
		t.Fatal("default-workspace member policy does not reach the member's WS-B key")
	}
}
