package store

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Policy is an org guardrail policy (D26, docs/API.md "Policies"). It applies to every key of the
// org when IsOrgDefault (minus the opted-out members and keys), to every key created by a member
// in MemberIDs, and to each key in KeyIDs. The gateway combines all policies that apply to a key
// (gateway/policy.go); Org.Guardrails and the key's own limits still apply on top.
type Policy struct {
	ID                 string         `json:"id"`
	OrgID              string         `json:"org_id"`
	Name               string         `json:"name"`
	Description        string         `json:"description,omitempty"`
	IsOrgDefault       bool           `json:"is_org_default"`
	MemberIDs          []string       `json:"member_ids,omitempty"` // user ids
	KeyIDs             []string       `json:"key_ids,omitempty"`
	OptedOutMemberIDs  []string       `json:"opted_out_member_ids,omitempty"` // org default only
	OptedOutKeyIDs     []string       `json:"opted_out_key_ids,omitempty"`    // org default only
	Budget             *PolicyBudget  `json:"budget,omitempty"`
	AllowedModels      []string       `json:"allowed_models,omitempty"`    // globs ("*" = any run); empty = any
	AllowedProviders   []string       `json:"allowed_providers,omitempty"` // provider types or names; empty = any
	Filters            []PolicyFilter `json:"filters,omitempty"`
	RequireZDR         bool           `json:"require_zdr,omitempty"`          // only providers declaring zero data retention
	DenyDataCollection bool           `json:"deny_data_collection,omitempty"` // only providers declaring no training on data
	AllowedRegions     []string       `json:"allowed_regions,omitempty"`      // stored for data regions (W2); not enforced yet
	CreatedAt          time.Time      `json:"created_at"`
	UpdatedAt          time.Time      `json:"updated_at"`
}

// PolicyBudget limits spend per member and per key over a UTC interval.
type PolicyBudget struct {
	LimitMicros int64  `json:"limit_micros"`
	Interval    string `json:"interval"`               // one of BudgetIntervals
	IncludeBYOK bool   `json:"include_byok,omitempty"` // also count spend on org (BYOK) providers' models
}

// PolicyFilter is an RE2 pattern matched on request text: redact replaces matches, block refuses.
type PolicyFilter struct {
	Name    string `json:"name,omitempty"` // shown in the redaction marker [REDACTED:<name>]
	Pattern string `json:"pattern"`
	Action  string `json:"action"` // redact | block
}

// BudgetIntervals are the policy budget reset intervals (UTC; weeks start on Monday).
var BudgetIntervals = []string{"daily", "weekly", "monthly", "lifetime"}

// MaxPolicyPatternBytes caps the total pattern size of one policy; MaxPoliciesPerOrg the policy count.
const (
	MaxPolicyPatternBytes = 100 << 10
	MaxPoliciesPerOrg     = 100
	maxPolicyList         = 1000
)

var reFilterName = regexp.MustCompile(`^[A-Za-z0-9 _.-]{0,64}$`)

func policyKey(orgID, id string) string { return "policy/" + orgID + "/" + id }

func (p *Policy) validate() error {
	inv := func(f string, a ...any) error { return fmt.Errorf("%w: "+f, append([]any{ErrInvalid}, a...)...) }
	p.Name = strings.TrimSpace(p.Name)
	if p.Name == "" || len(p.Name) > 100 {
		return inv("policy name required (at most 100 characters)")
	}
	if len(p.Description) > 1000 {
		return inv("description is at most 1000 characters")
	}
	for name, l := range map[string][]string{"member_ids": p.MemberIDs, "key_ids": p.KeyIDs, "opted_out_member_ids": p.OptedOutMemberIDs,
		"opted_out_key_ids": p.OptedOutKeyIDs, "allowed_models": p.AllowedModels, "allowed_providers": p.AllowedProviders, "allowed_regions": p.AllowedRegions} {
		if len(l) > maxPolicyList {
			return inv("%s has more than %d entries", name, maxPolicyList)
		}
		if slices.ContainsFunc(l, func(s string) bool { return strings.TrimSpace(s) == "" || len(s) > 200 }) {
			return inv("%s entries must be non-empty (at most 200 characters)", name)
		}
	}
	if b := p.Budget; b != nil {
		if b.LimitMicros <= 0 {
			return inv("budget limit must be > 0")
		}
		if !slices.Contains(BudgetIntervals, b.Interval) {
			return inv("budget interval must be one of %s", strings.Join(BudgetIntervals, ", "))
		}
	}
	size := 0
	for _, f := range p.Filters {
		if f.Action != "redact" && f.Action != "block" {
			return inv("filter action must be redact or block")
		}
		if !reFilterName.MatchString(f.Name) {
			return inv("filter name: letters, digits, space . _ - (at most 64)")
		}
		if size += len(f.Pattern); size > MaxPolicyPatternBytes {
			return inv("filter patterns exceed %d bytes", MaxPolicyPatternBytes)
		}
		if f.Pattern == "" {
			return inv("filter pattern required")
		}
		if _, err := regexp.Compile(f.Pattern); err != nil { // RE2: linear time on any input
			return inv("filter pattern %q: %v", f.Name, err)
		}
	}
	return nil
}

// PutPolicy creates (empty ID) or replaces a policy of p.OrgID. Callers check that assigned
// members and keys belong to the org.
func (s *Store) PutPolicy(ctx context.Context, p *Policy) error {
	if err := p.validate(); err != nil {
		return err
	}
	create := p.ID == ""
	now := time.Now().UTC()
	if create {
		p.ID, p.CreatedAt = NewID(), now
	}
	p.UpdatedAt = now
	if create { // shortcut: counted outside the transaction, so concurrent creates can pass the cap by a few
		n, err := s.KV.Scan(ctx, "policy/"+p.OrgID+"/", "", MaxPoliciesPerOrg)
		if err != nil {
			return err
		}
		if len(n) >= MaxPoliciesPerOrg {
			return fmt.Errorf("%w: at most %d policies per organization", ErrInvalid, MaxPoliciesPerOrg)
		}
	}
	return s.update(ctx, func(tx Tx) error {
		if _, err := tx.Get("org/" + p.OrgID); err != nil {
			return err
		}
		old, err := getJSON[Policy](tx.Get, policyKey(p.OrgID, p.ID))
		switch {
		case err == nil && create:
			return ErrExists
		case errors.Is(err, ErrNotFound) && !create:
			return ErrNotFound
		case err != nil && !errors.Is(err, ErrNotFound):
			return err
		case old != nil:
			p.CreatedAt = old.CreatedAt
		}
		return putJSON(tx, policyKey(p.OrgID, p.ID), p)
	})
}

func (s *Store) GetPolicy(ctx context.Context, orgID, id string) (*Policy, error) {
	return getJSON[Policy](s.get(ctx), policyKey(orgID, id))
}

func (s *Store) ListPolicies(ctx context.Context, orgID string) ([]Policy, error) {
	return listJSON[Policy](ctx, s.KV, "policy/"+orgID+"/", "", 0)
}

func (s *Store) DeletePolicy(ctx context.Context, orgID, id string) error {
	return s.update(ctx, func(tx Tx) error {
		if _, err := tx.Get(policyKey(orgID, id)); err != nil {
			return err
		}
		return tx.Delete(policyKey(orgID, id))
	})
}
