package gateway

// Guardrail policies (D26): an org's policies that apply to one API key are compiled into an
// EffectivePolicy when the key is authenticated and cached with it in the auth cache (dropped by
// InvalidateAuth and by every catalog reload, which policy writes trigger).

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/polyrail/polyrail/internal/store"
)

// EffectivePolicy is the combination of every policy applying to one key: allowlists intersect,
// require_zdr and deny_data_collection are OR-ed, filters are the union (a block beats a redact),
// and each budget is checked on its own. The nil policy allows everything.
type EffectivePolicy struct {
	Policies           []PolicyRef       `json:"policies"`
	AllowedModels      [][]string        `json:"allowed_models,omitempty"`    // a model must match a glob of every list
	AllowedProviders   [][]string        `json:"allowed_providers,omitempty"` // a provider must match an entry of every list
	AllowedRegions     [][]string        `json:"allowed_regions,omitempty"`   // a provider's region must be in every list
	RequireZDR         bool              `json:"require_zdr"`
	DenyDataCollection bool              `json:"deny_data_collection"`
	Filters            []EffectiveFilter `json:"filters,omitempty"`
	Budgets            []EffectiveBudget `json:"budgets,omitempty"`

	models      [][]*regexp.Regexp
	modelBy     []string // policy name of each models list
	providersBy []string // policy names constraining providers (allowlist, ZDR, data collection)
	res         []*regexp.Regexp
}

// PolicyRef names an applied policy and how it reached the key: org_default, member, key.
type PolicyRef struct {
	ID   string   `json:"id"`
	Name string   `json:"name"`
	Via  []string `json:"via"`
}

type EffectiveFilter struct {
	Policy  string `json:"policy"`
	Name    string `json:"name,omitempty"`
	Pattern string `json:"pattern"`
	Action  string `json:"action"`
}

// EffectiveBudget is one policy budget on one scope: the spend of the member who created the key
// (all their keys) or of the key alone.
type EffectiveBudget struct {
	PolicyID    string    `json:"policy_id"`
	Policy      string    `json:"policy"`
	Scope       string    `json:"scope"`   // member | key
	Subject     string    `json:"subject"` // user id or key id
	LimitMicros int64     `json:"limit_micros"`
	Interval    string    `json:"interval"`
	IncludeBYOK bool      `json:"include_byok"`
	SpentMicros int64     `json:"spent_micros"`       // filled by Gateway.EffectivePolicy
	ResetsAt    time.Time `json:"resets_at,omitzero"` // start of the next interval (UTC); zero for lifetime
}

// glob compiles a model glob: "*" matches any run of characters, including "/".
func glob(g string) *regexp.Regexp {
	return regexp.MustCompile("^" + strings.ReplaceAll(regexp.QuoteMeta(g), `\*`, ".*") + "$")
}

// compilePolicy combines the policies of key k's org that apply to k; nil when none does.
func compilePolicy(k store.APIKey, pols []store.Policy) (*EffectivePolicy, error) {
	var ep *EffectivePolicy
	for _, p := range pols {
		var via []string
		member := k.CreatedBy != ""
		if p.IsOrgDefault && !slices.Contains(p.OptedOutKeyIDs, k.ID) && !(member && slices.Contains(p.OptedOutMemberIDs, k.CreatedBy)) {
			via = append(via, "org_default")
		}
		if member && slices.Contains(p.MemberIDs, k.CreatedBy) {
			via = append(via, "member")
		}
		if slices.Contains(p.KeyIDs, k.ID) {
			via = append(via, "key")
		}
		if len(via) == 0 {
			continue
		}
		if ep == nil {
			ep = &EffectivePolicy{}
		}
		ep.Policies = append(ep.Policies, PolicyRef{ID: p.ID, Name: p.Name, Via: via})
		if len(p.AllowedModels) > 0 {
			res := make([]*regexp.Regexp, len(p.AllowedModels))
			for i, g := range p.AllowedModels {
				res[i] = glob(g)
			}
			ep.AllowedModels, ep.models, ep.modelBy = append(ep.AllowedModels, p.AllowedModels), append(ep.models, res), append(ep.modelBy, p.Name)
		}
		if len(p.AllowedProviders) > 0 {
			ep.AllowedProviders = append(ep.AllowedProviders, p.AllowedProviders)
		}
		if len(p.AllowedRegions) > 0 {
			ep.AllowedRegions = append(ep.AllowedRegions, p.AllowedRegions)
		}
		if len(p.AllowedProviders) > 0 || len(p.AllowedRegions) > 0 || p.RequireZDR || p.DenyDataCollection {
			ep.providersBy = append(ep.providersBy, p.Name)
		}
		ep.RequireZDR = ep.RequireZDR || p.RequireZDR
		ep.DenyDataCollection = ep.DenyDataCollection || p.DenyDataCollection
		for _, f := range p.Filters {
			re, err := regexp.Compile(f.Pattern)
			if err != nil {
				return nil, fmt.Errorf("policy %q filter: %w", p.Name, err)
			}
			ep.Filters = append(ep.Filters, EffectiveFilter{Policy: p.Name, Name: f.Name, Pattern: f.Pattern, Action: f.Action})
			ep.res = append(ep.res, re)
		}
		if b := p.Budget; b != nil {
			eb := EffectiveBudget{PolicyID: p.ID, Policy: p.Name, LimitMicros: b.LimitMicros, Interval: b.Interval, IncludeBYOK: b.IncludeBYOK}
			perMember := member && (slices.Contains(via, "org_default") || slices.Contains(via, "member"))
			if perMember {
				eb.Scope, eb.Subject = "member", k.CreatedBy
				ep.Budgets = append(ep.Budgets, eb)
			}
			if slices.Contains(via, "key") || !perMember { // a key without a creator is budgeted per key
				eb.Scope, eb.Subject = "key", k.ID
				ep.Budgets = append(ep.Budgets, eb)
			}
		}
	}
	return ep, nil
}

// AllowModel reports whether every model allowlist admits model id.
func (p *EffectivePolicy) AllowModel(id string) bool { return p.modelDenier(id) == "" }

// modelDenier is the name of the first policy whose allowlist rejects id ("" = allowed).
func (p *EffectivePolicy) modelDenier(id string) string {
	if p == nil {
		return ""
	}
	for i, res := range p.models {
		if !slices.ContainsFunc(res, func(re *regexp.Regexp) bool { return re.MatchString(id) }) {
			return p.modelBy[i]
		}
	}
	return ""
}

// AllowTarget reports whether requests under this policy may be sent to prov: it is in every
// provider allowlist (by type, name or id, case-insensitive) and declares zero data retention
// (require_zdr) and no training on data (deny_data_collection) when those are set. Undeclared
// privacy fails both. Its data region (store.Provider.Region; undeclared = global) must be in
// every allowed_regions list (entries normalized when the policy is saved). Request-level provider filters compose with this one (both must pass).
func (p *EffectivePolicy) AllowTarget(prov store.Provider) bool {
	if p == nil {
		return true
	}
	for _, list := range p.AllowedProviders {
		if !slices.ContainsFunc(list, func(e string) bool {
			return strings.EqualFold(e, prov.Type) || strings.EqualFold(e, prov.Name) || e == prov.ID
		}) {
			return false
		}
	}
	if len(p.AllowedRegions) > 0 {
		r := prov.Region()
		for _, list := range p.AllowedRegions {
			if !slices.ContainsFunc(list, func(e string) bool { return store.NormalizeRegion(e) == r }) {
				return false
			}
		}
	}
	pv := prov.Privacy
	if p.RequireZDR && (pv == nil || !pv.ZDR) {
		return false
	}
	if p.DenyDataCollection && (pv == nil || pv.TrainsOnData) {
		return false
	}
	return true
}

// filter applies the filters to segs: if any block filter matches it returns that policy's name
// and changes nothing; otherwise redact matches become [REDACTED:<filter name>] (or [REDACTED]).
func (p *EffectivePolicy) filter(segs []*string) string {
	if p == nil || len(p.res) == 0 {
		return ""
	}
	for i, f := range p.Filters {
		if f.Action != "block" {
			continue
		}
		for _, s := range segs {
			if p.res[i].MatchString(*s) {
				return f.Policy
			}
		}
	}
	for i, f := range p.Filters {
		if f.Action != "redact" {
			continue
		}
		mark := "[REDACTED]"
		if f.Name != "" {
			mark = "[REDACTED:" + f.Name + "]"
		}
		for _, s := range segs {
			*s = p.res[i].ReplaceAllLiteralString(*s, mark)
		}
	}
	return ""
}

// budgetPeriod is the UTC period of t for an interval, and when the next period starts (zero for
// lifetime). Weeks start on Monday.
func budgetPeriod(interval string, t time.Time) (string, time.Time) {
	t = t.UTC()
	day := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	switch interval {
	case "daily":
		return "d" + day.Format("20060102"), day.AddDate(0, 0, 1)
	case "weekly":
		mon := day.AddDate(0, 0, -((int(day.Weekday()) + 6) % 7))
		return "w" + mon.Format("20060102"), mon.AddDate(0, 0, 7)
	case "monthly":
		first := time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
		return "m" + first.Format("200601"), first.AddDate(0, 1, 0)
	}
	return "l", time.Time{}
}

// counter is the spend counter of budget b at t. Budgets without BYOK read a counter that only
// platform-model spend goes to ("/p"); the others read all spend ("/a").
func (b EffectiveBudget) counter(orgID string, t time.Time) string {
	period, _ := budgetPeriod(b.Interval, t)
	kind := "/p"
	if b.IncludeBYOK {
		kind = "/a"
	}
	return "spend/pol/" + orgID + "/" + b.Scope[:1] + "/" + b.Subject + "/" + period + kind
}

func (g *Gateway) clock() time.Time {
	if g.now != nil {
		return g.now()
	}
	return time.Now()
}

func policyErr(status int, code, policy, format string, args ...any) *apiError {
	e := errf(status, "permission_error", code, format, args...)
	e.policy = policy
	return e
}

// loadPolicy compiles the effective policy of key k (nil when no policy applies).
func (g *Gateway) loadPolicy(ctx context.Context, k store.APIKey) (*EffectivePolicy, error) {
	pols, err := g.S.ListPolicies(ctx, k.OrgID)
	if err != nil {
		return nil, err
	}
	return compilePolicy(k, pols)
}

// admitPolicy checks every policy budget of p against the spend so far in its interval
// (stored + this node's unflushed spend, D7).
func (g *Gateway) admitPolicy(ctx context.Context, p *principal) *apiError {
	if p.pol == nil {
		return nil
	}
	now := g.clock()
	for _, b := range p.pol.Budgets {
		spent, err := g.Usage.Spent(ctx, b.counter(p.Org.ID, now))
		if err != nil {
			return errf(503, "api_error", "store_unavailable", "temporarily unavailable")
		}
		if spent >= b.LimitMicros {
			g.emit(p.Org.ID, "budget.exceeded", map[string]any{"code": "policy_budget_exceeded", "key_id": p.Key.ID, "policy": b.Policy})
			return policyErr(403, "policy_budget_exceeded", b.Policy, "%s budget of policy %q is exhausted for this %s", b.Interval, b.Policy, b.Scope)
		}
	}
	return nil
}

// recordPolicySpend adds a request's cost to the counters of p's budgets. Spend on org (BYOK)
// models only counts for budgets that include BYOK.
func (g *Gateway) recordPolicySpend(p *principal, m store.Model, cost int64) {
	if p.pol == nil || cost <= 0 || len(p.pol.Budgets) == 0 {
		return
	}
	now := g.clock()
	var keys []string
	for _, b := range p.pol.Budgets {
		if m.OrgID != "" && !b.IncludeBYOK {
			continue
		}
		if k := b.counter(p.Org.ID, now); !slices.Contains(keys, k) {
			keys = append(keys, k)
		}
	}
	g.Usage.AddSpend(cost, keys...)
}

// policyTargets keeps the targets p's policy and the deployment's REGION_LOCK allow; an error
// when they remove them all.
func (g *Gateway) policyTargets(p *principal, snap *Snapshot, m store.Model, ts []store.Target) ([]store.Target, *apiError) {
	lock := g.cfg.RegionLock
	if (p.pol == nil && lock == "") || len(ts) == 0 {
		return ts, nil
	}
	ts = slices.DeleteFunc(ts, func(t store.Target) bool {
		prov := snap.Providers[t.ProviderID]
		return (lock != "" && prov.Region() != lock) || !p.pol.AllowTarget(prov)
	})
	if len(ts) == 0 {
		var by []string
		if lock != "" {
			by = append(by, "REGION_LOCK")
		}
		if p.pol != nil {
			by = append(by, p.pol.providersBy...)
		}
		return nil, policyErr(403, "policy_no_eligible_provider", strings.Join(by, ", "),
			"no provider of model %q meets the key's policy or the deployment's region lock (allowed providers, data regions, zero data retention, data collection)", m.ID)
	}
	return ts, nil
}

// EffectivePolicy returns key k's combined policy with each budget's spend in its current
// interval, for the admin API; an empty policy when none applies.
func (g *Gateway) EffectivePolicy(ctx context.Context, k store.APIKey) (*EffectivePolicy, error) {
	ep, err := g.loadPolicy(ctx, k)
	if err != nil || ep == nil {
		return &EffectivePolicy{Policies: []PolicyRef{}}, err
	}
	now := g.clock()
	for i := range ep.Budgets {
		b := &ep.Budgets[i]
		if b.SpentMicros, err = g.Usage.Spent(ctx, b.counter(k.OrgID, now)); err != nil {
			return nil, err
		}
		_, b.ResetsAt = budgetPeriod(b.Interval, now)
	}
	return ep, nil
}

func logPolicyErr(k store.APIKey, err error) {
	slog.Error("policy load failed", "org", k.OrgID, "key", k.ID, "err", err)
}
