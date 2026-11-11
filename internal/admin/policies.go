package admin

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/polyrail/polyrail/internal/store"
)

// Guardrail policies (D26): org-scoped, managed by admins; members read the effective policy of
// their own keys.
func (a *API) registerPolicies(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/orgs/{org}/policies", a.org(store.RoleAdmin, a.listPolicies))
	mux.HandleFunc("POST /api/orgs/{org}/policies", a.org(store.RoleAdmin, a.putPolicy))
	mux.HandleFunc("GET /api/orgs/{org}/policies/{pid}", a.org(store.RoleAdmin, a.getPolicy))
	mux.HandleFunc("PUT /api/orgs/{org}/policies/{pid}", a.org(store.RoleAdmin, a.putPolicy))
	mux.HandleFunc("DELETE /api/orgs/{org}/policies/{pid}", a.org(store.RoleAdmin, a.deletePolicy))
	mux.HandleFunc("GET /api/orgs/{org}/keys/{id}/effective-policy", a.org(store.RoleViewer, a.effectivePolicy))
}

// policyInput is store.Policy as the API takes it: the budget limit in USD (or micro-USD).
type policyInput struct {
	Name              string   `json:"name"`
	Description       string   `json:"description"`
	IsOrgDefault      bool     `json:"is_org_default"`
	MemberIDs         []string `json:"member_ids"`
	KeyIDs            []string `json:"key_ids"`
	OptedOutMemberIDs []string `json:"opted_out_member_ids"`
	OptedOutKeyIDs    []string `json:"opted_out_key_ids"`
	Budget            *struct {
		LimitUSD    *float64 `json:"limit_usd"`
		LimitMicros *int64   `json:"limit_micros"`
		Interval    string   `json:"interval"`
		IncludeBYOK bool     `json:"include_byok"`
	} `json:"budget"`
	AllowedModels      []string             `json:"allowed_models"`
	AllowedProviders   []string             `json:"allowed_providers"`
	Filters            []store.PolicyFilter `json:"filters"`
	RequireZDR         bool                 `json:"require_zdr"`
	DenyDataCollection bool                 `json:"deny_data_collection"`
	AllowedRegions     []string             `json:"allowed_regions"`
}

func (a *API) listPolicies(w http.ResponseWriter, r *http.Request) {
	ps, err := a.S.ListPolicies(r.Context(), r.PathValue("org"))
	if err != nil {
		fail(w, err)
		return
	}
	if ps == nil {
		ps = []store.Policy{}
	}
	writeJSON(w, 200, ps)
}

func (a *API) getPolicy(w http.ResponseWriter, r *http.Request) {
	p, err := a.S.GetPolicy(r.Context(), r.PathValue("org"), r.PathValue("pid"))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, p)
}

// putPolicy creates (POST) or replaces (PUT) a policy. Assigned and opted-out members and keys
// must belong to the org.
func (a *API) putPolicy(w http.ResponseWriter, r *http.Request) {
	var in policyInput
	if !decode(w, r, &in) {
		return
	}
	org, id := r.PathValue("org"), r.PathValue("pid")
	p := &store.Policy{ID: id, OrgID: org, Name: in.Name, Description: in.Description, IsOrgDefault: in.IsOrgDefault,
		MemberIDs: in.MemberIDs, KeyIDs: in.KeyIDs, OptedOutMemberIDs: in.OptedOutMemberIDs, OptedOutKeyIDs: in.OptedOutKeyIDs,
		AllowedModels: in.AllowedModels, AllowedProviders: in.AllowedProviders, Filters: in.Filters, RequireZDR: in.RequireZDR,
		DenyDataCollection: in.DenyDataCollection, AllowedRegions: in.AllowedRegions}
	if b := in.Budget; b != nil {
		if (b.LimitUSD == nil) == (b.LimitMicros == nil) {
			fail(w, fmt.Errorf("%w: budget needs one of limit_usd or limit_micros", store.ErrInvalid))
			return
		}
		p.Budget = &store.PolicyBudget{Interval: b.Interval, IncludeBYOK: b.IncludeBYOK}
		if b.LimitUSD != nil {
			p.Budget.LimitMicros = usdToMicros(*b.LimitUSD)
		} else {
			p.Budget.LimitMicros = *b.LimitMicros
		}
	}
	if err := a.checkAssignees(r, p); err != nil {
		fail(w, err)
		return
	}
	if err := a.S.PutPolicy(r.Context(), p); err != nil {
		fail(w, err)
		return
	}
	a.G.InvalidateAuth()
	action, status := "policy.update", http.StatusOK
	if id == "" {
		action, status = "policy.create", http.StatusCreated
	}
	a.audit(r, org, action, p.ID, fmt.Sprintf("name=%q default=%v members=%d keys=%d budget=%v filters=%d zdr=%v deny_data_collection=%v",
		p.Name, p.IsOrgDefault, len(p.MemberIDs), len(p.KeyIDs), p.Budget != nil, len(p.Filters), p.RequireZDR, p.DenyDataCollection))
	writeJSON(w, status, p)
}

func (a *API) checkAssignees(r *http.Request, p *store.Policy) error {
	ctx, org := r.Context(), p.OrgID
	ms, err := a.S.ListMembers(ctx, org)
	if err != nil {
		return err
	}
	ks, err := a.S.ListKeys(ctx, org)
	if err != nil {
		return err
	}
	members, keys := map[string]bool{}, map[string]bool{}
	for _, m := range ms {
		members[m.UserID] = true
	}
	for _, k := range ks {
		keys[k.ID] = true
	}
	for _, id := range append(append([]string{}, p.MemberIDs...), p.OptedOutMemberIDs...) {
		if !members[id] {
			return fmt.Errorf("%w: %q is not a member of this organization", store.ErrInvalid, id)
		}
	}
	for _, id := range append(append([]string{}, p.KeyIDs...), p.OptedOutKeyIDs...) {
		if !keys[id] {
			return fmt.Errorf("%w: %q is not an API key of this organization", store.ErrInvalid, id)
		}
	}
	return nil
}

func (a *API) deletePolicy(w http.ResponseWriter, r *http.Request) {
	org, id := r.PathValue("org"), r.PathValue("pid")
	if err := a.S.DeletePolicy(r.Context(), org, id); err != nil {
		fail(w, err)
		return
	}
	a.G.InvalidateAuth()
	a.audit(r, org, "policy.delete", id, "")
	w.WriteHeader(http.StatusNoContent)
}

// effectivePolicy answers the combined policy of one key with its budgets' current spend.
// Members and viewers see only keys they created (404 otherwise, like a missing key).
func (a *API) effectivePolicy(w http.ResponseWriter, r *http.Request) {
	k, err := a.S.GetKey(r.Context(), r.PathValue("org"), r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	if role(r).Rank() < store.RoleAdmin.Rank() && k.CreatedBy != user(r).ID {
		fail(w, store.ErrNotFound)
		return
	}
	ep, err := a.G.EffectivePolicy(r.Context(), *k)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, ep)
}

var rePrivacyRegion = regexp.MustCompile(`^[a-z0-9-]{0,32}$`)

// checkPrivacy validates a provider's declared data handling.
func checkPrivacy(pv *store.ProviderPrivacy) error {
	if pv == nil {
		return nil
	}
	pv.Region = strings.TrimSpace(pv.Region)
	switch {
	case pv.RetentionDays < 0 || pv.RetentionDays > 36500:
		return fmt.Errorf("%w: privacy.retention_days must be 0..36500", store.ErrInvalid)
	case pv.ZDR && pv.RetentionDays > 0:
		return fmt.Errorf("%w: privacy: a zero data retention provider cannot retain data for days", store.ErrInvalid)
	case !rePrivacyRegion.MatchString(pv.Region):
		return fmt.Errorf("%w: privacy.region: lowercase letters, digits and - (e.g. us, eu)", store.ErrInvalid)
	}
	return nil
}
