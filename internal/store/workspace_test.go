package store

import (
	"context"
	"errors"
	"testing"
)

func TestWorkspacesLegacyDefaultAndLifecycle(t *testing.T) {
	ctx := context.Background()
	kv, err := OpenPebble(t.TempDir(), 8)
	if err != nil {
		t.Fatal(err)
	}
	box, _ := NewBox("test-master-key-123456")
	s := New(kv, box)
	// an org stored before workspaces: no workspace record, a key with no workspace_id
	if err := s.KV.Update(ctx, func(tx Tx) error { return putJSON(tx, "org/legacy", Org{ID: "legacy", Name: "L"}) }); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateKey(ctx, &APIKey{OrgID: "legacy", Name: "old"}, "h1"); err != nil {
		t.Fatal(err)
	}
	ws, err := s.ListWorkspaces(ctx, "legacy")
	if err != nil || len(ws) != 1 || !ws[0].IsDefault || ws[0].ID != DefaultWorkspace {
		t.Fatalf("legacy org workspaces: %+v %v", ws, err)
	}
	if n, _ := s.WorkspaceContents(ctx, "legacy", DefaultWorkspace); n["api_keys"] != 1 {
		t.Fatalf("legacy key counts in the default workspace: %v", n)
	}
	if _, err := s.ListWorkspaces(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown org: %v", err)
	}

	o := &Org{Name: "A"}
	if err := s.CreateOrg(ctx, o, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateUser(ctx, &User{Email: "m@x.io"}); err != nil {
		t.Fatal(err)
	}
	u, _ := s.GetUserByEmail(ctx, "m@x.io")
	if err := s.SetMember(ctx, Member{OrgID: o.ID, UserID: u.ID, Role: RoleMember}); err != nil {
		t.Fatal(err)
	}
	w := &Workspace{OrgID: o.ID, Name: "Eng", Slug: "eng"}
	if err := s.CreateWorkspace(ctx, w); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateWorkspace(ctx, &Workspace{OrgID: o.ID, Name: "Eng 2", Slug: "eng"}); !errors.Is(err, ErrExists) {
		t.Fatalf("duplicate slug: %v", err)
	}
	// roles: member of the org = member of default, nothing in eng until added
	for _, c := range []struct {
		ws   string
		org  Role
		want Role
	}{{"", RoleMember, RoleMember}, {w.ID, RoleMember, ""}, {w.ID, RoleAdmin, RoleAdmin}, {w.ID, RoleOwner, RoleAdmin}} {
		if got, _ := s.WorkspaceRole(ctx, o.ID, c.ws, u.ID, c.org); got != c.want {
			t.Errorf("role in %q as org %s: %q, want %q", c.ws, c.org, got, c.want)
		}
	}
	if err := s.SetWorkspaceMember(ctx, WorkspaceMember{OrgID: o.ID, WorkspaceID: DefaultWorkspace, UserID: u.ID, Role: RoleAdmin}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("explicit default membership: %v", err)
	}
	if err := s.SetWorkspaceMember(ctx, WorkspaceMember{OrgID: o.ID, WorkspaceID: w.ID, UserID: u.ID, Role: RoleAdmin}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.WorkspaceRole(ctx, o.ID, w.ID, u.ID, RoleMember); got != RoleAdmin {
		t.Fatalf("workspace admin: %q", got)
	}
	// leaving the org ends workspace memberships
	if err := s.RemoveMember(ctx, o.ID, u.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetWorkspaceMember(ctx, o.ID, w.ID, u.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("membership after leaving the org: %v", err)
	}
	// contents block deletion; the default workspace is never deleted
	if err := s.CreateKey(ctx, &APIKey{OrgID: o.ID, WorkspaceID: w.ID, Name: "k"}, "h2"); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.WorkspaceContents(ctx, o.ID, w.ID); n["api_keys"] != 1 || len(n) != 1 {
		t.Fatalf("contents: %v", n)
	}
	if n, _ := s.WorkspaceContents(ctx, o.ID, ""); len(n) != 0 {
		t.Fatalf("default workspace contents: %v", n)
	}
	if err := s.DeleteWorkspace(ctx, o.ID, DefaultWorkspace); !errors.Is(err, ErrInvalid) {
		t.Fatalf("delete default: %v", err)
	}
	if err := s.DeleteWorkspace(ctx, o.ID, w.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetWorkspace(ctx, o.ID, w.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted workspace: %v", err)
	}
	if err := s.DeleteOrg(ctx, o.ID); err != nil {
		t.Fatal(err)
	}
	if items, _ := s.KV.Scan(ctx, "ws/"+o.ID+"/", "", 0); len(items) != 0 {
		t.Fatalf("workspaces left after org delete: %d", len(items))
	}
}
