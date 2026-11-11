package admin

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/elimity-com/scim"
	scimerr "github.com/elimity-com/scim/errors"
	"github.com/elimity-com/scim/optional"
	"github.com/elimity-com/scim/schema"
	"github.com/polyrail/polyrail/internal/gateway"
	"github.com/polyrail/polyrail/internal/store"
	filter "github.com/scim2/filter-parser/v2"
)

// SCIM 2.0 provisioning per org (RFC 7643/7644) at /scim/v2/{org}/. github.com/elimity-com/scim
// serves the protocol (routing, schema and PATCH validation, filters, list responses, discovery
// endpoints); the handlers below map Users and Groups onto Polyrail users, memberships and
// group → role mappings. Deprovisioning (active=false or DELETE) removes the org membership and
// revokes the API keys and management tokens the user created in the org; reactivation brings
// the membership back but never the revoked credentials.

type scimCtx struct{}

func scimIdentity(r *http.Request) *store.Identity {
	return r.Context().Value(scimCtx{}).(*store.Identity)
}

// scimMu serializes SCIM writes on this node, so group membership and role recomputation of one
// request see each other's results.
// shortcut: per-node lock; two nodes provisioning the same org at once can interleave group
// edits (the IdP retries and converges); add a KV lease if that shows up.
var scimMu sync.Mutex

func (a *API) newSCIMServer() (http.Handler, error) {
	return scim.NewServer(&scim.ServerArgs{
		ServiceProviderConfig: &scim.ServiceProviderConfig{
			DocumentationURI: optional.NewString("https://github.com/polyrail/polyrail/blob/main/docs/API.md#scim-20"),
			AuthenticationSchemes: []scim.AuthenticationScheme{{Type: scim.AuthenticationTypeOauthBearerToken,
				Name: "Bearer token", Description: "Per-organization SCIM token from the SSO settings", Primary: true}},
			MaxResults: 100, SupportFiltering: true, SupportPatch: true,
		},
		ResourceTypes: []scim.ResourceType{
			{ID: optional.NewString("User"), Name: "User", Endpoint: "/Users", Description: optional.NewString("User Account"),
				Schema: schema.CoreUserSchema(), Handler: scimUsers{a},
				SchemaExtensions: []scim.SchemaExtension{{Schema: schema.ExtensionEnterpriseUser()}}}, // accepted, not stored
			{ID: optional.NewString("Group"), Name: "Group", Endpoint: "/Groups", Description: optional.NewString("Group"),
				Schema: schema.CoreGroupSchema(), Handler: scimGroups{a}},
		},
	}, scim.WithLogger(scimLogger{}))
}

type scimLogger struct{}

func (scimLogger) Error(args ...interface{}) { slog.Error("scim", "detail", fmt.Sprint(args...)) }

func writeSCIMError(w http.ResponseWriter, status int, detail string) {
	w.Header().Set("Content-Type", "application/scim+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"schemas": []string{"urn:ietf:params:scim:api:messages:2.0:Error"},
		"detail": detail, "status": fmt.Sprint(status)})
}

// scimEntry authenticates the org's SCIM bearer token and hands the request to the SCIM server.
func (a *API) scimEntry(w http.ResponseWriter, r *http.Request) {
	ctx, org := r.Context(), r.PathValue("org")
	if n, _ := a.Lim.Incr(ctx, "scim:"+org, 1); n > 1200 {
		writeSCIMError(w, http.StatusTooManyRequests, "too many requests, wait a minute")
		return
	}
	tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	id, err := a.S.GetIdentity(ctx, org)
	if ok && err == nil && id.SCIM.Enabled {
		h, herr := a.S.SCIMTokenHash(ctx, org)
		ok = herr == nil && subtle.ConstantTimeCompare([]byte(h), []byte(gateway.HashKey(strings.TrimSpace(tok)))) == 1
	} else {
		ok = false
	}
	if !ok {
		writeSCIMError(w, http.StatusUnauthorized, "invalid or missing SCIM bearer token")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	r2 := r.Clone(context.WithValue(ctx, scimCtx{}, id))
	r2.URL.Path, r2.URL.RawPath = strings.TrimPrefix(r.URL.Path, "/scim/v2/"+org), ""
	a.scim.ServeHTTP(w, r2)
}

// scimFail maps store errors to SCIM errors; internal details go to the log only.
func scimFail(err error) error {
	var se scimerr.ScimError
	switch {
	case errors.As(err, &se):
		return se
	case errors.Is(err, store.ErrNotFound):
		return scimerr.ScimError{Status: http.StatusNotFound, Detail: "Resource not found."}
	case errors.Is(err, store.ErrExists):
		return scimerr.ScimErrorUniqueness
	case errors.Is(err, store.ErrInvalid):
		return scimerr.ScimErrorBadRequest(err.Error())
	}
	slog.Error("scim", "err", err)
	return scimerr.ScimErrorInternal
}

func (a *API) scimAudit(ctx context.Context, org, action, target, detail string) {
	if err := a.S.Audit(ctx, store.AuditEntry{OrgID: org, ActorID: "scim", Actor: "scim", Action: action, Target: target, Detail: detail}); err != nil {
		slog.Error("audit write failed", "err", err, "action", action)
	}
}

// attr looks up a top-level attribute case-insensitively.
func attr(m map[string]interface{}, name string) (interface{}, bool) {
	for k, v := range m {
		if strings.EqualFold(k, name) {
			return v, true
		}
	}
	return nil, false
}

func attrStr(m map[string]interface{}, name string) string {
	v, _ := attr(m, name)
	s, _ := v.(string)
	return strings.TrimSpace(s)
}

func asMap(v interface{}) map[string]interface{} { m, _ := v.(map[string]interface{}); return m }

func asList(v interface{}) []interface{} {
	switch x := v.(type) {
	case []interface{}:
		return x
	case map[string]interface{}:
		return []interface{}{x}
	}
	return nil
}

// ---- Users ----

type scimUsers struct{ a *API }

// emailOf picks the email of a SCIM user: the primary email, else the first, else an
// email-shaped userName.
func emailOf(attrs map[string]interface{}) string {
	emails, _ := attr(attrs, "emails")
	var first string
	for _, e := range asList(emails) {
		m := asMap(e)
		v := attrStr(m, "value")
		if v == "" {
			continue
		}
		if p, _ := attr(m, "primary"); p == true {
			return strings.ToLower(v)
		}
		if first == "" {
			first = v
		}
	}
	if first != "" {
		return strings.ToLower(first)
	}
	if u := attrStr(attrs, "userName"); strings.Contains(u, "@") {
		return strings.ToLower(u)
	}
	return ""
}

func displayNameOf(su *store.SCIMUser, attrs map[string]interface{}) string {
	if d := attrStr(attrs, "displayName"); d != "" {
		return d
	}
	if su.Formatted != "" {
		return su.Formatted
	}
	return strings.TrimSpace(su.GivenName + " " + su.FamilyName)
}

func setNames(su *store.SCIMUser, name map[string]interface{}) {
	su.GivenName, su.FamilyName, su.Formatted = attrStr(name, "givenName"), attrStr(name, "familyName"), attrStr(name, "formatted")
}

func userResource(u *store.User, su *store.SCIMUser) scim.Resource {
	attrs := scim.ResourceAttributes{"userName": su.UserName, "active": su.Active, "displayName": u.Name,
		"emails": []interface{}{map[string]interface{}{"value": u.Email, "primary": true, "type": "work"}}}
	name := map[string]interface{}{}
	for k, v := range map[string]string{"givenName": su.GivenName, "familyName": su.FamilyName, "formatted": su.Formatted} {
		if v != "" {
			name[k] = v
		}
	}
	if len(name) > 0 {
		attrs["name"] = name
	}
	var ext optional.String
	if su.ExternalID != "" {
		ext = optional.NewString(su.ExternalID)
	}
	created, updated := su.Created, su.Updated
	return scim.Resource{ID: u.ID, ExternalID: ext, Attributes: attrs, Meta: scim.Meta{Created: &created, LastModified: &updated}}
}

// load returns the org's live SCIM record for user id and the user.
func (h scimUsers) load(ctx context.Context, org, id string) (*store.SCIMUser, *store.User, error) {
	su, err := h.a.S.GetSCIMUser(ctx, org, id)
	if err == nil && su.Deleted {
		err = store.ErrNotFound
	}
	if err != nil {
		return nil, nil, scimerr.ScimErrorResourceNotFound(id)
	}
	u, err := h.a.S.GetUser(ctx, id)
	if err != nil {
		return nil, nil, scimFail(err)
	}
	return su, u, nil
}

// save stores the SCIM record and, when the IdP sent a display name, the user's name.
// Operators' names are left alone: an org's IdP manages org accounts, not platform ones.
func (h scimUsers) save(ctx context.Context, u *store.User, su *store.SCIMUser, name string) error {
	su.Updated = time.Now().UTC()
	if err := h.a.S.PutSCIMUser(ctx, su); err != nil {
		return err
	}
	if name != "" && name != u.Name && !u.Operator {
		nu, err := h.a.S.UpdateUser(ctx, u.ID, func(x *store.User) error { x.Name = name; return nil })
		if err != nil {
			return err
		}
		*u = *nu
	}
	return nil
}

func (h scimUsers) Create(r *http.Request, attrs scim.ResourceAttributes) (scim.Resource, error) {
	ctx, id := r.Context(), scimIdentity(r)
	email := emailOf(attrs)
	if email == "" {
		return scim.Resource{}, scimerr.ScimErrorBadRequest("the user needs an email (emails or an email-shaped userName)")
	}
	if owner, err := h.a.S.DomainOrg(ctx, store.EmailDomain(email)); err != nil || owner != id.OrgID {
		return scim.Resource{}, scimerr.ScimErrorBadRequest("the email domain is not verified for this organization")
	}
	scimMu.Lock()
	defer scimMu.Unlock()
	u, err := h.a.S.GetUserByEmail(ctx, email)
	if errors.Is(err, store.ErrNotFound) {
		u = &store.User{Email: email}
		err = h.a.S.CreateUser(ctx, u)
	}
	if err != nil {
		return scim.Resource{}, scimFail(err)
	}
	now := time.Now().UTC()
	su, err := h.a.S.GetSCIMUser(ctx, id.OrgID, u.ID)
	switch {
	case err == nil && !su.Deleted:
		return scim.Resource{}, scimerr.ScimErrorUniqueness
	case err == nil: // re-created after a DELETE
		su.Deleted, su.Created = false, now
	case errors.Is(err, store.ErrNotFound):
		su = &store.SCIMUser{OrgID: id.OrgID, UserID: u.ID, Created: now}
	default:
		return scim.Resource{}, scimFail(err)
	}
	su.UserName, su.ExternalID = attrStr(attrs, "userName"), attrStr(attrs, "externalId")
	n, _ := attr(attrs, "name")
	setNames(su, asMap(n))
	active, _ := attr(attrs, "active")
	if err := h.a.scimSetActive(ctx, id, u, su, active != false); err != nil {
		return scim.Resource{}, scimFail(err)
	}
	if err := h.save(ctx, u, su, displayNameOf(su, attrs)); err != nil {
		return scim.Resource{}, scimFail(err)
	}
	h.a.scimAudit(ctx, id.OrgID, "scim.user.create", u.ID, u.Email)
	return userResource(u, su), nil
}

func (h scimUsers) Get(r *http.Request, id string) (scim.Resource, error) {
	su, u, err := h.load(r.Context(), scimIdentity(r).OrgID, id)
	if err != nil {
		return scim.Resource{}, err
	}
	return userResource(u, su), nil
}

// GetAll lists the org's SCIM users, filtered (userName eq "…" is what IdPs send) and paged.
// shortcut: linear scan of the org's SCIM users per list; add a userName index past ~10k users.
func (h scimUsers) GetAll(r *http.Request, p scim.ListRequestParams) (scim.Page, error) {
	ctx, org := r.Context(), scimIdentity(r).OrgID
	sus, err := h.a.S.ListSCIMUsers(ctx, org)
	if err != nil {
		return scim.Page{}, scimFail(err)
	}
	var all []scim.Resource
	for i := range sus {
		su := &sus[i]
		if su.Deleted {
			continue
		}
		u, err := h.a.S.GetUser(ctx, su.UserID)
		if err != nil {
			continue // user removed by an operator
		}
		res := userResource(u, su)
		if p.FilterValidator != nil && p.FilterValidator.PassesFilter(filterView(res)) != nil {
			continue
		}
		all = append(all, res)
	}
	return page(all, p), nil
}

// filterView adds id and externalId, which filters may name, to a resource's attributes.
func filterView(res scim.Resource) map[string]interface{} {
	m := make(map[string]interface{}, len(res.Attributes)+2)
	for k, v := range res.Attributes {
		m[k] = v
	}
	m["id"] = res.ID
	if res.ExternalID.Present() {
		m["externalId"] = res.ExternalID.Value()
	}
	return m
}

func page(all []scim.Resource, p scim.ListRequestParams) scim.Page {
	out := scim.Page{TotalResults: len(all), Resources: []scim.Resource{}}
	start := max(p.StartIndex, 1) - 1
	if start < len(all) && p.Count > 0 {
		out.Resources = all[start:min(len(all), start+p.Count)]
	}
	return out
}

func (h scimUsers) Replace(r *http.Request, id string, attrs scim.ResourceAttributes) (scim.Resource, error) {
	ctx, ident := r.Context(), scimIdentity(r)
	scimMu.Lock()
	defer scimMu.Unlock()
	su, u, err := h.load(ctx, ident.OrgID, id)
	if err != nil {
		return scim.Resource{}, err
	}
	if e := emailOf(attrs); e != "" && e != u.Email {
		return scim.Resource{}, scimerr.ScimError{ScimType: scimerr.ScimTypeMutability, Status: http.StatusBadRequest,
			Detail: "changing a user's email is not supported; deprovision and create the user again"}
	}
	su.UserName, su.ExternalID = attrStr(attrs, "userName"), attrStr(attrs, "externalId")
	n, _ := attr(attrs, "name")
	setNames(su, asMap(n))
	active, _ := attr(attrs, "active")
	if err := h.a.scimSetActive(ctx, ident, u, su, active != false); err != nil {
		return scim.Resource{}, scimFail(err)
	}
	if err := h.save(ctx, u, su, displayNameOf(su, attrs)); err != nil {
		return scim.Resource{}, scimFail(err)
	}
	h.a.scimAudit(ctx, ident.OrgID, "scim.user.replace", u.ID, fmt.Sprintf("active=%v", su.Active))
	return userResource(u, su), nil
}

// opPath is a PATCH path as lower-case "attr" or "attr.sub" ("" when the op has no path).
func opPath(op scim.PatchOperation) string {
	if op.Path == nil {
		return ""
	}
	p := op.Path.AttributePath.AttributeName
	if s := op.Path.AttributePath.SubAttribute; s != nil {
		p += "." + *s
	} else if s := op.Path.SubAttribute; s != nil {
		p += "." + *s
	}
	return strings.ToLower(p)
}

func (h scimUsers) Patch(r *http.Request, id string, ops []scim.PatchOperation) (scim.Resource, error) {
	ctx, ident := r.Context(), scimIdentity(r)
	scimMu.Lock()
	defer scimMu.Unlock()
	su, u, err := h.load(ctx, ident.OrgID, id)
	if err != nil {
		return scim.Resource{}, err
	}
	active, display := su.Active, ""
	var apply func(path string, v interface{}, remove bool) error
	apply = func(path string, v interface{}, remove bool) error {
		str, _ := v.(string)
		if remove {
			str = ""
		}
		switch path {
		case "active":
			if b, ok := v.(bool); ok && !remove {
				active = b
			}
		case "username": // the IdP's login name (Entra: the UPN); the account email stays fixed
			if str != "" {
				su.UserName = str
			}
		case "externalid":
			su.ExternalID = str
		case "displayname":
			display = str
		case "name":
			if remove {
				su.GivenName, su.FamilyName, su.Formatted = "", "", ""
			} else {
				for k, sv := range asMap(v) {
					if err := apply("name."+strings.ToLower(k), sv, false); err != nil {
						return err
					}
				}
			}
		case "name.givenname":
			su.GivenName = str
		case "name.familyname":
			su.FamilyName = str
		case "name.formatted":
			su.Formatted = str
		case "emails", "emails.value":
			if remove {
				return nil
			}
			e := str
			if path == "emails" {
				e = emailOf(map[string]interface{}{"emails": v})
			}
			if e != "" && !strings.EqualFold(e, u.Email) {
				return scimerr.ScimError{ScimType: scimerr.ScimTypeMutability, Status: http.StatusBadRequest,
					Detail: "changing a user's email is not supported; deprovision and create the user again"}
			}
		}
		return nil // other attributes (title, locale, enterprise extension, ...) are accepted and not stored
	}
	for _, op := range ops {
		remove := op.Op == scim.PatchOperationRemove
		if path := opPath(op); path != "" {
			if err := apply(path, op.Value, remove); err != nil {
				return scim.Resource{}, err
			}
			continue
		}
		for k, v := range asMap(op.Value) { // no path: a map of attribute paths to values (Okta, Entra)
			if err := apply(strings.ToLower(k), v, remove); err != nil {
				return scim.Resource{}, err
			}
		}
	}
	wasActive := su.Active
	if err := h.a.scimSetActive(ctx, ident, u, su, active); err != nil {
		return scim.Resource{}, scimFail(err)
	}
	if err := h.save(ctx, u, su, display); err != nil {
		return scim.Resource{}, scimFail(err)
	}
	h.a.scimAudit(ctx, ident.OrgID, "scim.user.patch", u.ID, fmt.Sprintf("active=%v→%v", wasActive, su.Active))
	return userResource(u, su), nil
}

func (h scimUsers) Delete(r *http.Request, id string) error {
	ctx, ident := r.Context(), scimIdentity(r)
	scimMu.Lock()
	defer scimMu.Unlock()
	su, u, err := h.load(ctx, ident.OrgID, id)
	if err != nil {
		return err
	}
	if err := h.a.scimSetActive(ctx, ident, u, su, false); err != nil {
		return scimFail(err)
	}
	su.Deleted = true // kept as a tombstone: SAML sign-in stays refused
	if err := h.save(ctx, u, su, ""); err != nil {
		return scimFail(err)
	}
	gs, err := h.a.S.ListSCIMGroups(ctx, ident.OrgID)
	if err != nil {
		return scimFail(err)
	}
	for i := range gs {
		if g := &gs[i]; slices.Contains(g.Members, u.ID) {
			g.Members = slices.DeleteFunc(g.Members, func(m string) bool { return m == u.ID })
			if err := h.a.S.PutSCIMGroup(ctx, g); err != nil {
				return scimFail(err)
			}
		}
	}
	h.a.scimAudit(ctx, ident.OrgID, "scim.user.delete", u.ID, u.Email)
	return nil
}

// scimSetActive applies the IdP's active flag. Activation (re)creates the org membership with
// the mapped or default role; deactivation removes it and revokes the user's credentials in
// the org.
func (a *API) scimSetActive(ctx context.Context, id *store.Identity, u *store.User, su *store.SCIMUser, active bool) error {
	org := id.OrgID
	if active {
		su.Active = true
		if _, err := a.S.GetMember(ctx, org, u.ID); errors.Is(err, store.ErrNotFound) {
			rl, err := a.scimRole(ctx, id, u.ID)
			if err != nil {
				return err
			}
			return a.S.SetMember(ctx, store.Member{OrgID: org, UserID: u.ID, Role: rl})
		} else if err != nil {
			return err
		}
		return nil
	}
	su.Active = false
	m, err := a.S.GetMember(ctx, org, u.ID)
	switch {
	case err == nil:
		if m.Role == store.RoleOwner {
			if last, err := a.lastOwner(ctx, org); err != nil || last {
				return orScim(err, "the last owner of the organization cannot be deprovisioned")
			}
		}
		if err := a.S.RemoveMember(ctx, org, u.ID); err != nil {
			return err
		}
	case !errors.Is(err, store.ErrNotFound):
		return err
	}
	keys, tokens, err := a.revokeUserCredentials(ctx, org, u.ID)
	if err != nil {
		return err
	}
	a.scimAudit(ctx, org, "scim.user.deprovision", u.ID, fmt.Sprintf("%s keys_revoked=%d tokens_revoked=%d", u.Email, keys, tokens))
	return nil
}

func orScim(err error, msg string) error {
	if err != nil {
		return err
	}
	return scimerr.ScimError{ScimType: scimerr.ScimTypeMutability, Status: http.StatusBadRequest, Detail: msg}
}

// revokeUserCredentials revokes the API keys and management tokens uid created in org.
func (a *API) revokeUserCredentials(ctx context.Context, org, uid string) (keys, tokens int, err error) {
	ks, err := a.S.ListKeys(ctx, org)
	if err != nil {
		return 0, 0, err
	}
	for _, k := range ks {
		if k.CreatedBy == uid && !k.Revoked {
			if _, err := a.S.UpdateKey(ctx, org, k.ID, func(x *store.APIKey) error { x.Revoked = true; return nil }); err != nil {
				return keys, tokens, err
			}
			keys++
		}
	}
	ts, err := a.S.ListMgmtTokens(ctx, org)
	if err != nil {
		return keys, tokens, err
	}
	for _, t := range ts {
		if t.CreatedBy == uid && !t.Revoked {
			if _, err := a.S.UpdateMgmtToken(ctx, org, t.ID, func(x *store.MgmtToken) { x.Revoked = true }); err != nil {
				return keys, tokens, err
			}
			tokens++
		}
	}
	if keys > 0 {
		a.G.InvalidateAuth()
	}
	return keys, tokens, nil
}

// scimRole is the role a provisioned user gets: the highest mapping of the SCIM groups they
// are in, else the SCIM default role.
func (a *API) scimRole(ctx context.Context, id *store.Identity, uid string) (store.Role, error) {
	def := id.SCIM.DefaultRole
	if def == "" {
		def = store.RoleMember
	}
	if len(id.GroupMappings) == 0 {
		return def, nil
	}
	gs, err := a.S.ListSCIMGroups(ctx, id.OrgID)
	if err != nil {
		return "", err
	}
	var names []string
	for _, g := range gs {
		if slices.Contains(g.Members, uid) {
			names = append(names, g.DisplayName)
		}
	}
	if rl, ok := mappedRole(id.GroupMappings, names); ok {
		return rl, nil
	}
	return def, nil
}

// syncRoles recomputes the role of active SCIM users among uids after a group change. Owners
// keep their role; nothing changes while no group mapping is configured.
func (a *API) syncRoles(ctx context.Context, id *store.Identity, uids []string) error {
	if len(id.GroupMappings) == 0 {
		return nil
	}
	slices.Sort(uids)
	for _, uid := range slices.Compact(uids) {
		su, err := a.S.GetSCIMUser(ctx, id.OrgID, uid)
		if err != nil || !su.Active {
			continue
		}
		m, err := a.S.GetMember(ctx, id.OrgID, uid)
		if err != nil || m.Role == store.RoleOwner {
			continue
		}
		rl, err := a.scimRole(ctx, id, uid)
		if err != nil {
			return err
		}
		if rl != m.Role {
			if err := a.S.SetMember(ctx, store.Member{OrgID: id.OrgID, UserID: uid, Role: rl}); err != nil {
				return err
			}
			a.scimAudit(ctx, id.OrgID, "scim.member.role", uid, string(m.Role)+"→"+string(rl))
		}
	}
	return nil
}

// ---- Groups ----

type scimGroups struct{ a *API }

func groupResource(g *store.SCIMGroup) scim.Resource {
	members := make([]interface{}, 0, len(g.Members))
	for _, m := range g.Members {
		members = append(members, map[string]interface{}{"value": m})
	}
	var ext optional.String
	if g.ExternalID != "" {
		ext = optional.NewString(g.ExternalID)
	}
	created, updated := g.Created, g.Updated
	return scim.Resource{ID: g.ID, ExternalID: ext, Meta: scim.Meta{Created: &created, LastModified: &updated},
		Attributes: scim.ResourceAttributes{"displayName": g.DisplayName, "members": members}}
}

// memberIDs reads member ids from a SCIM members value, keeping the org's live SCIM users only.
func (h scimGroups) memberIDs(ctx context.Context, org string, v interface{}) []string {
	var out []string
	for _, m := range asList(v) {
		uid := attrStr(asMap(m), "value")
		if su, err := h.a.S.GetSCIMUser(ctx, org, uid); err == nil && !su.Deleted && !slices.Contains(out, uid) {
			out = append(out, uid)
		}
	}
	return out
}

func (h scimGroups) save(ctx context.Context, id *store.Identity, g *store.SCIMGroup, before []string, action string) (scim.Resource, error) {
	g.Updated = time.Now().UTC()
	if err := h.a.S.PutSCIMGroup(ctx, g); err != nil {
		return scim.Resource{}, scimFail(err)
	}
	if err := h.a.syncRoles(ctx, id, append(slices.Clone(before), g.Members...)); err != nil {
		return scim.Resource{}, scimFail(err)
	}
	h.a.scimAudit(ctx, id.OrgID, action, g.ID, fmt.Sprintf("%s members=%d", g.DisplayName, len(g.Members)))
	return groupResource(g), nil
}

func (h scimGroups) Create(r *http.Request, attrs scim.ResourceAttributes) (scim.Resource, error) {
	ctx, id := r.Context(), scimIdentity(r)
	scimMu.Lock()
	defer scimMu.Unlock()
	name := attrStr(attrs, "displayName")
	gs, err := h.a.S.ListSCIMGroups(ctx, id.OrgID)
	if err != nil {
		return scim.Resource{}, scimFail(err)
	}
	if slices.ContainsFunc(gs, func(g store.SCIMGroup) bool { return strings.EqualFold(g.DisplayName, name) }) {
		return scim.Resource{}, scimerr.ScimErrorUniqueness
	}
	if len(gs) >= 1000 {
		return scim.Resource{}, scimerr.ScimErrorBadRequest("at most 1000 groups per organization")
	}
	m, _ := attr(attrs, "members")
	now := time.Now().UTC()
	g := &store.SCIMGroup{OrgID: id.OrgID, DisplayName: name, ExternalID: attrStr(attrs, "externalId"),
		Members: h.memberIDs(ctx, id.OrgID, m), Created: now}
	return h.save(ctx, id, g, nil, "scim.group.create")
}

func (h scimGroups) load(ctx context.Context, org, id string) (*store.SCIMGroup, error) {
	g, err := h.a.S.GetSCIMGroup(ctx, org, id)
	if err != nil {
		return nil, scimerr.ScimErrorResourceNotFound(id)
	}
	return g, nil
}

func (h scimGroups) Get(r *http.Request, id string) (scim.Resource, error) {
	g, err := h.load(r.Context(), scimIdentity(r).OrgID, id)
	if err != nil {
		return scim.Resource{}, err
	}
	return groupResource(g), nil
}

func (h scimGroups) GetAll(r *http.Request, p scim.ListRequestParams) (scim.Page, error) {
	gs, err := h.a.S.ListSCIMGroups(r.Context(), scimIdentity(r).OrgID)
	if err != nil {
		return scim.Page{}, scimFail(err)
	}
	var all []scim.Resource
	for i := range gs {
		res := groupResource(&gs[i])
		if p.FilterValidator != nil && p.FilterValidator.PassesFilter(filterView(res)) != nil {
			continue
		}
		all = append(all, res)
	}
	return page(all, p), nil
}

func (h scimGroups) Replace(r *http.Request, id string, attrs scim.ResourceAttributes) (scim.Resource, error) {
	ctx, ident := r.Context(), scimIdentity(r)
	scimMu.Lock()
	defer scimMu.Unlock()
	g, err := h.load(ctx, ident.OrgID, id)
	if err != nil {
		return scim.Resource{}, err
	}
	before := g.Members
	m, _ := attr(attrs, "members")
	g.DisplayName, g.ExternalID, g.Members = attrStr(attrs, "displayName"), attrStr(attrs, "externalId"), h.memberIDs(ctx, ident.OrgID, m)
	return h.save(ctx, ident, g, before, "scim.group.replace")
}

func (h scimGroups) Patch(r *http.Request, id string, ops []scim.PatchOperation) (scim.Resource, error) {
	ctx, ident := r.Context(), scimIdentity(r)
	scimMu.Lock()
	defer scimMu.Unlock()
	g, err := h.load(ctx, ident.OrgID, id)
	if err != nil {
		return scim.Resource{}, err
	}
	before := slices.Clone(g.Members)
	without := func(drop []string) {
		g.Members = slices.DeleteFunc(g.Members, func(m string) bool { return slices.Contains(drop, m) })
	}
	for _, op := range ops {
		path := opPath(op)
		if path == "" { // no path: a map of attributes (Entra sends displayName this way)
			vals := asMap(op.Value)
			if s := attrStr(vals, "displayName"); s != "" {
				g.DisplayName = s
			}
			if s, ok := attr(vals, "externalId"); ok {
				g.ExternalID, _ = s.(string)
			}
			if m, ok := attr(vals, "members"); ok {
				ids := h.memberIDs(ctx, ident.OrgID, m)
				if op.Op == scim.PatchOperationReplace {
					g.Members = ids
				} else {
					without(ids)
					g.Members = append(g.Members, ids...)
				}
			}
			continue
		}
		switch {
		case path == "displayname" && op.Op != scim.PatchOperationRemove:
			if s, _ := op.Value.(string); strings.TrimSpace(s) != "" {
				g.DisplayName = strings.TrimSpace(s)
			}
		case path == "externalid":
			s, _ := op.Value.(string)
			g.ExternalID = s
		case strings.HasPrefix(path, "members"):
			switch op.Op {
			case scim.PatchOperationAdd:
				ids := h.memberIDs(ctx, ident.OrgID, op.Value)
				without(ids)
				g.Members = append(g.Members, ids...)
			case scim.PatchOperationReplace:
				g.Members = h.memberIDs(ctx, ident.OrgID, op.Value)
			case scim.PatchOperationRemove:
				switch {
				case op.Path.ValueExpression != nil: // members[value eq "id"] (Okta)
					without(filterValues(op.Path.ValueExpression))
				case op.Value != nil: // path members with a list of values (Entra)
					var ids []string
					for _, m := range asList(op.Value) {
						ids = append(ids, attrStr(asMap(m), "value"))
					}
					without(ids)
				default:
					g.Members = []string{}
				}
			}
		}
	}
	return h.save(ctx, ident, g, before, "scim.group.patch")
}

// filterValues returns the ids a members[value eq "…"] filter names (or-ed eq expressions).
func filterValues(e filter.Expression) []string {
	var out []string
	var walk func(x filter.Expression)
	walk = func(x filter.Expression) {
		switch v := x.(type) {
		case *filter.AttributeExpression:
			if strings.EqualFold(v.AttributePath.AttributeName, "value") && v.Operator == filter.EQ {
				if s, ok := v.CompareValue.(string); ok {
					out = append(out, s)
				}
			}
		case *filter.LogicalExpression:
			if v.Operator == filter.OR {
				walk(v.Left)
				walk(v.Right)
			}
		}
	}
	walk(e)
	return out
}

func (h scimGroups) Delete(r *http.Request, id string) error {
	ctx, ident := r.Context(), scimIdentity(r)
	scimMu.Lock()
	defer scimMu.Unlock()
	g, err := h.load(ctx, ident.OrgID, id)
	if err != nil {
		return err
	}
	if err := h.a.S.DeleteSCIMGroup(ctx, ident.OrgID, id); err != nil {
		return scimFail(err)
	}
	if err := h.a.syncRoles(ctx, ident, g.Members); err != nil {
		return scimFail(err)
	}
	h.a.scimAudit(ctx, ident.OrgID, "scim.group.delete", g.ID, g.DisplayName)
	return nil
}
