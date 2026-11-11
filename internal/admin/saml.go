package admin

import (
	"context"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/crewjam/saml"
	"github.com/polyrail/polyrail/internal/store"
)

// SAML 2.0 service provider per org (crewjam/saml does the protocol: signatures, audience,
// NotOnOrAfter, InResponseTo, Destination, issuer). Polyrail adds: the email must be on a
// domain the org verified, operators never sign in through a tenant IdP, deprovisioned SCIM
// users stay out, and each assertion is accepted once.

const samlCookie = "gw_saml"

// replayCache records accepted assertion ids in the KV, so every node refuses a replay.
type replayCache struct {
	s   *store.Store
	org string
}

func (c replayCache) Consume(id string, expires time.Time) error {
	err := c.s.ConsumeOnce(context.Background(), "saml/"+c.org+"/"+id, expires)
	if errors.Is(err, store.ErrExists) {
		return saml.ErrAssertionReplayed
	}
	return err
}

// samlSP builds the org's service provider. needIdP requires SAML to be enabled with metadata.
func (a *API) samlSP(ctx context.Context, org string, needIdP bool) (*saml.ServiceProvider, *store.Identity, error) {
	id, err := a.S.GetIdentity(ctx, org)
	if err != nil {
		return nil, nil, err
	}
	if needIdP && (!id.SAML.Enabled || id.SAML.IdPMetadataXML == "") {
		return nil, nil, store.ErrNotFound
	}
	bundle, err := a.S.SAMLKey(ctx, org)
	if err != nil {
		return nil, nil, err
	}
	key, cert, err := parseSPBundle(bundle)
	if err != nil {
		return nil, nil, err
	}
	base, err := url.Parse(a.samlBase(org))
	if err != nil {
		return nil, nil, err
	}
	meta, acs := *base, *base
	meta.Path += "/metadata"
	acs.Path += "/acs"
	sp := &saml.ServiceProvider{EntityID: meta.String(), Key: key, Certificate: cert, MetadataURL: meta, AcsURL: acs,
		AllowIDPInitiated: id.SAML.AllowIdPInitiated, RejectSHA1Signatures: true, ReplayCache: replayCache{a.S, org},
		AuthnNameIDFormat: saml.UnspecifiedNameIDFormat}
	if id.SAML.IdPMetadataXML != "" {
		md, err := parseIdPMetadata(id.SAML.IdPMetadataXML)
		if err != nil {
			return nil, nil, err
		}
		sp.IDPMetadata = md
	}
	return sp, id, nil
}

func (a *API) samlMetadata(w http.ResponseWriter, r *http.Request) {
	sp, _, err := a.samlSP(r.Context(), r.PathValue("org"), false)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			slog.Error("saml metadata", "err", err)
		}
		writeErr(w, http.StatusNotFound, "SAML is not set up for this organization")
		return
	}
	buf, err := xml.MarshalIndent(sp.Metadata(), "", "  ")
	if err != nil {
		fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/samlmetadata+xml")
	_, _ = w.Write(buf)
}

func (a *API) samlCookieOpts(org string, value string, maxAge int) *http.Cookie {
	c := &http.Cookie{Name: samlCookie, Value: value, Path: "/api/sso/saml/" + org, HttpOnly: true,
		Secure: a.cfg.SecureCookies, SameSite: http.SameSiteLaxMode, MaxAge: maxAge}
	if a.cfg.SecureCookies {
		// The IdP posts the response cross-site; a Lax cookie would not come back with it.
		c.SameSite = http.SameSiteNoneMode
	}
	return c
}

func (a *API) samlLogin(w http.ResponseWriter, r *http.Request) {
	org := r.PathValue("org")
	sp, _, err := a.samlSP(r.Context(), org, true)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			slog.Error("saml login", "err", err)
		}
		writeErr(w, http.StatusNotFound, "SAML is not set up for this organization")
		return
	}
	req, err := sp.MakeAuthenticationRequest(sp.GetSSOBindingLocation(saml.HTTPRedirectBinding), saml.HTTPRedirectBinding, saml.HTTPPostBinding)
	if err != nil {
		fail(w, err)
		return
	}
	u, err := req.Redirect("", sp)
	if err != nil {
		fail(w, err)
		return
	}
	http.SetCookie(w, a.samlCookieOpts(org, a.sign.sign("saml", 10*time.Minute, org, req.ID), 600))
	http.Redirect(w, r, u.String(), http.StatusFound)
}

func (a *API) samlACS(w http.ResponseWriter, r *http.Request) {
	ctx, org := r.Context(), r.PathValue("org")
	if n, _ := a.Lim.Incr(ctx, "saml-acs:"+clientIP(r), 1); n > 30 {
		writeErr(w, http.StatusTooManyRequests, "too many sign-in attempts, wait a minute")
		return
	}
	sp, id, err := a.samlSP(ctx, org, true)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			slog.Error("saml acs", "err", err)
		}
		writeErr(w, http.StatusNotFound, "SAML is not set up for this organization")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := r.ParseForm(); err != nil || r.PostForm.Get("SAMLResponse") == "" {
		writeErr(w, http.StatusBadRequest, "missing SAMLResponse (only the HTTP-POST binding is supported)")
		return
	}
	var requestIDs []string
	if c, err := r.Cookie(samlCookie); err == nil {
		if parts, ok := a.sign.verify("saml", c.Value); ok && len(parts) == 2 && parts[0] == org {
			requestIDs = []string{parts[1]}
		}
	}
	http.SetCookie(w, a.samlCookieOpts(org, "", -1))
	raw, err := base64.StdEncoding.DecodeString(r.PostForm.Get("SAMLResponse"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid SAMLResponse encoding")
		return
	}
	as, err := sp.ParseXMLResponse(raw, requestIDs, sp.AcsURL)
	if err != nil {
		var ire *saml.InvalidResponseError
		if errors.As(err, &ire) {
			err = ire.PrivateErr
		}
		slog.Warn("saml assertion rejected", "org", org, "err", err) // details stay in the log
		writeErr(w, http.StatusUnauthorized, "the SAML response is not valid for this organization")
		return
	}
	email, name, groups := samlIdentity(as, id.SAML)
	u, code, msg := a.samlUser(ctx, id, email, name, groups)
	if u == nil {
		writeErr(w, code, msg)
		return
	}
	if err := a.S.Audit(ctx, store.AuditEntry{OrgID: org, ActorID: u.ID, Actor: u.Email, Action: "sso.saml.login", Target: u.ID,
		Detail: "idp=" + id.SAML.IdPEntityID}); err != nil {
		slog.Error("audit write failed", "err", err, "action", "sso.saml.login")
	}
	a.setSession(w, u)
	http.Redirect(w, r, "/app/", http.StatusFound)
}

// samlUser maps a verified assertion to a user: the email must be on a verified domain of the
// org; unknown users are created (just in time) and non-members join with the default role or
// their group mapping. It returns nil and an HTTP status and message when sign-in is refused.
func (a *API) samlUser(ctx context.Context, id *store.Identity, email, name string, groups []string) (*store.User, int, string) {
	org := id.OrgID
	if email == "" || !strings.Contains(email, "@") {
		return nil, http.StatusForbidden, "the SAML assertion has no email address"
	}
	if owner, err := a.S.DomainOrg(ctx, store.EmailDomain(email)); err != nil || owner != org {
		return nil, http.StatusForbidden, "your email domain is not verified for this organization"
	}
	u, err := a.S.GetUserByEmail(ctx, email)
	switch {
	case errors.Is(err, store.ErrNotFound):
		u = &store.User{Email: email, Name: name}
		err = a.S.CreateUser(ctx, u)
		if errors.Is(err, store.ErrExists) { // a concurrent first sign-in created it
			u, err = a.S.GetUserByEmail(ctx, email)
		}
	case err == nil && u.Operator:
		// an org's IdP must never be able to sign in as a platform operator
		return nil, http.StatusForbidden, "operator accounts sign in with their password"
	}
	if err != nil {
		slog.Error("saml user", "err", err)
		return nil, http.StatusInternalServerError, "internal error"
	}
	if u.Disabled {
		return nil, http.StatusForbidden, "account disabled"
	}
	if su, err := a.S.GetSCIMUser(ctx, org, u.ID); err == nil && !su.Active {
		return nil, http.StatusForbidden, "your account was deprovisioned by your identity provider"
	}
	mapped, hasMapped := mappedRole(id.GroupMappings, groups)
	m, err := a.S.GetMember(ctx, org, u.ID)
	switch {
	case errors.Is(err, store.ErrNotFound):
		rl := id.SAML.DefaultRole
		if hasMapped {
			rl = mapped
		}
		err = a.S.SetMember(ctx, store.Member{OrgID: org, UserID: u.ID, Role: rl})
	case err == nil && hasMapped && m.Role != store.RoleOwner && m.Role != mapped:
		err = a.S.SetMember(ctx, store.Member{OrgID: org, UserID: u.ID, Role: mapped})
	}
	if err != nil {
		slog.Error("saml membership", "err", err)
		return nil, http.StatusInternalServerError, "internal error"
	}
	return u, 0, ""
}

// mappedRole is the highest role among the group mappings that match groups (case-insensitive).
func mappedRole(ms []store.GroupMapping, groups []string) (store.Role, bool) {
	var best store.Role
	for _, m := range ms {
		if m.Role == "" {
			continue
		}
		if slices.ContainsFunc(groups, func(g string) bool { return strings.EqualFold(g, m.Group) }) && m.Role.Rank() > best.Rank() {
			best = m.Role
		}
	}
	return best, best != ""
}

var (
	defaultEmailAttrs = []string{"email", "mail", "emailaddress", "Email", "EmailAddress", "User.Email",
		"http://schemas.xmlsoap.org/ws/2005/05/identity/claims/emailaddress", "urn:oid:0.9.2342.19200300.100.1.3"}
	defaultNameAttrs = []string{"name", "displayName", "displayname", "cn",
		"http://schemas.microsoft.com/identity/claims/displayname", "urn:oid:2.16.840.1.113730.3.1.241", "urn:oid:2.5.4.3"}
	defaultGroupAttrs = []string{"groups", "Groups", "memberOf", "http://schemas.microsoft.com/ws/2008/06/identity/claims/groups"}
)

// samlIdentity reads email, display name and groups from the assertion, by the configured
// attribute names or common Okta / Entra ID / Google defaults. The NameID is the email
// fallback when it looks like one.
func samlIdentity(as *saml.Assertion, cfg store.SAMLConfig) (email, name string, groups []string) {
	values := func(names ...string) []string {
		for _, n := range names {
			for _, st := range as.AttributeStatements {
				for _, at := range st.Attributes {
					if at.Name == n { // never FriendlyName (saml-core §2.7.3.1)
						var out []string
						for _, v := range at.Values {
							if s := strings.TrimSpace(v.Value); s != "" {
								out = append(out, s)
							}
						}
						if len(out) > 0 {
							return out
						}
					}
				}
			}
		}
		return nil
	}
	pick := func(cfgName string, defaults []string) []string {
		if cfgName != "" {
			return values(cfgName)
		}
		return values(defaults...)
	}
	if v := pick(cfg.AttrEmail, defaultEmailAttrs); len(v) > 0 {
		email = v[0]
	} else if as.Subject != nil && as.Subject.NameID != nil && strings.Contains(as.Subject.NameID.Value, "@") {
		email = as.Subject.NameID.Value
	}
	if v := pick(cfg.AttrName, defaultNameAttrs); len(v) > 0 {
		name = v[0]
	}
	return strings.ToLower(strings.TrimSpace(email)), name, pick(cfg.AttrGroups, defaultGroupAttrs)
}
