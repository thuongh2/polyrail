package admin

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/crewjam/saml"
	"github.com/crewjam/saml/samlsp"
	"github.com/polyrail/polyrail/internal/gateway"
	"github.com/polyrail/polyrail/internal/store"
)

// Enterprise identity (docs/API.md "Enterprise identity", D29): verified email domains, per-org
// SAML SSO with optional enforcement, SCIM provisioning (scim.go) and SSO discovery for the
// login page. Every setting is owner-only; management tokens (capped at admin) cannot reach it.

const (
	domainTXTPrefix   = "_polyrail-verify."
	domainTXTValuePre = "polyrail-verification="
	ScimPrefix        = "pr-scim-"
)

func (a *API) registerIdentity(mux *http.ServeMux) {
	owner := func(h http.HandlerFunc) http.HandlerFunc { return a.org(store.RoleOwner, h) }
	mux.HandleFunc("GET /api/orgs/{org}/domains", owner(a.listDomains))
	mux.HandleFunc("POST /api/orgs/{org}/domains", owner(a.createDomain))
	mux.HandleFunc("POST /api/orgs/{org}/domains/{id}/verify", owner(a.verifyDomain))
	mux.HandleFunc("DELETE /api/orgs/{org}/domains/{id}", owner(a.deleteDomain))
	mux.HandleFunc("GET /api/orgs/{org}/sso", owner(a.getSSO))
	mux.HandleFunc("PUT /api/orgs/{org}/sso/saml", owner(a.putSAML))
	mux.HandleFunc("PUT /api/orgs/{org}/sso/scim", owner(a.putSCIM))
	mux.HandleFunc("POST /api/orgs/{org}/sso/scim/token", owner(a.rotateSCIMToken))
	mux.HandleFunc("PUT /api/orgs/{org}/sso/group-mappings", owner(a.putGroupMappings))
	mux.HandleFunc("GET /api/auth/sso", a.ssoDiscovery)
	mux.HandleFunc("GET /api/sso/saml/{org}/metadata", a.samlMetadata)
	mux.HandleFunc("GET /api/sso/saml/{org}/login", a.samlLogin)
	mux.HandleFunc("POST /api/sso/saml/{org}/acs", a.samlACS)
	mux.Handle("/scim/v2/{org}/", http.HandlerFunc(a.scimEntry))
}

// ---- domains ----

type domainView struct {
	store.Domain
	Record map[string]string `json:"record"` // the DNS TXT record to publish
}

func viewDomain(d store.Domain) domainView {
	return domainView{d, map[string]string{"type": "TXT", "name": domainTXTPrefix + d.Name, "value": domainTXTValuePre + d.Token}}
}

func (a *API) listDomains(w http.ResponseWriter, r *http.Request) {
	ds, err := a.S.ListDomains(r.Context(), r.PathValue("org"))
	if err != nil {
		fail(w, err)
		return
	}
	out := make([]domainView, 0, len(ds))
	for _, d := range ds {
		out = append(out, viewDomain(d))
	}
	writeJSON(w, 200, out)
}

func (a *API) createDomain(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name string `json:"name"`
	}
	if !decode(w, r, &in) {
		return
	}
	d := &store.Domain{OrgID: r.PathValue("org"), Name: in.Name}
	if err := a.S.CreateDomain(r.Context(), d); err != nil {
		fail(w, err)
		return
	}
	a.audit(r, d.OrgID, "domain.create", d.ID, d.Name)
	writeJSON(w, http.StatusCreated, viewDomain(*d))
}

func (a *API) verifyDomain(w http.ResponseWriter, r *http.Request) {
	org, id := r.PathValue("org"), r.PathValue("id")
	d, err := a.S.GetDomain(r.Context(), org, id)
	if err != nil {
		fail(w, err)
		return
	}
	if n, _ := a.Lim.Incr(r.Context(), "domain-verify:"+org, 1); n > 20 {
		writeErr(w, http.StatusTooManyRequests, "too many verification attempts, wait a minute")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	txts, err := a.lookupTXT(ctx, domainTXTPrefix+d.Name)
	want := domainTXTValuePre + d.Token
	if err != nil || !slices.ContainsFunc(txts, func(t string) bool { return strings.TrimSpace(t) == want }) {
		writeErr(w, http.StatusUnprocessableEntity, "TXT record "+domainTXTPrefix+d.Name+" with value "+want+" not found (DNS changes can take a while to propagate)")
		return
	}
	d, err = a.S.VerifyDomain(r.Context(), org, id)
	if errors.Is(err, store.ErrExists) {
		writeErr(w, http.StatusConflict, "this domain is verified by another organization")
		return
	}
	if err != nil {
		fail(w, err)
		return
	}
	a.audit(r, org, "domain.verify", d.ID, d.Name)
	writeJSON(w, 200, viewDomain(*d))
}

func (a *API) lookupTXT(ctx context.Context, name string) ([]string, error) {
	if a.LookupTXT != nil {
		return a.LookupTXT(ctx, name)
	}
	return net.DefaultResolver.LookupTXT(ctx, name)
}

func (a *API) deleteDomain(w http.ResponseWriter, r *http.Request) {
	org, id := r.PathValue("org"), r.PathValue("id")
	d, err := a.S.GetDomain(r.Context(), org, id)
	if err != nil {
		fail(w, err)
		return
	}
	if err := a.S.DeleteDomain(r.Context(), org, id); err != nil {
		fail(w, err)
		return
	}
	a.audit(r, org, "domain.delete", id, d.Name)
	w.WriteHeader(http.StatusNoContent)
}

// verifiedDomains returns the names of the org's verified domains.
func (a *API) verifiedDomains(ctx context.Context, org string) ([]string, error) {
	ds, err := a.S.ListDomains(ctx, org)
	var out []string
	for _, d := range ds {
		if d.Verified {
			out = append(out, d.Name)
		}
	}
	return out, err
}

// ---- SSO settings ----

func (a *API) samlBase(org string) string { return a.cfg.PublicURL + "/api/sso/saml/" + org }

func (a *API) getSSO(w http.ResponseWriter, r *http.Request) {
	org := r.PathValue("org")
	// The SP key exists before SAML is configured: the IdP needs the SP metadata first.
	if _, err := a.ensureSPKey(r.Context(), org); err != nil {
		fail(w, err)
		return
	}
	id, err := a.S.GetIdentity(r.Context(), org)
	if err != nil {
		fail(w, err)
		return
	}
	a.writeSSO(w, id)
}

func (a *API) writeSSO(w http.ResponseWriter, id *store.Identity) {
	base := a.samlBase(id.OrgID)
	writeJSON(w, 200, map[string]any{
		"saml": id.SAML, "scim": id.SCIM, "group_mappings": id.GroupMappings, "updated_at": id.UpdatedAt,
		"sp": map[string]string{"entity_id": base + "/metadata", "metadata_url": base + "/metadata", "acs_url": base + "/acs",
			"login_url": base + "/login", "certificate": id.SAML.SPCertificate},
		"scim_base_url": a.cfg.PublicURL + "/scim/v2/" + id.OrgID,
	})
}

// ensureSPKey creates the org's SP signing key and certificate once and returns the certificate PEM.
func (a *API) ensureSPKey(ctx context.Context, org string) (string, error) {
	id, err := a.S.GetIdentity(ctx, org)
	if err != nil {
		return "", err
	}
	if id.SAML.SPCertificate != "" {
		return id.SAML.SPCertificate, nil
	}
	bundle, err := a.S.SAMLKey(ctx, org)
	if errors.Is(err, store.ErrNotFound) {
		keyPEM, certPEM, gerr := newSPKeyPair(a.samlBase(org) + "/metadata")
		if gerr != nil {
			return "", gerr
		}
		bundle, err = a.S.EnsureSAMLKey(ctx, org, keyPEM+certPEM) // first writer wins
	}
	if err != nil {
		return "", err
	}
	_, cert, err := parseSPBundle(bundle)
	if err != nil {
		return "", err
	}
	certPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw}))
	_, err = a.S.UpdateIdentity(ctx, org, func(x *store.Identity) error { x.SAML.SPCertificate = certPEM; return nil })
	return certPEM, err
}

// parseSPBundle reads the SP private key and certificate from their stored PEM bundle.
func parseSPBundle(bundle string) (*rsa.PrivateKey, *x509.Certificate, error) {
	var key *rsa.PrivateKey
	var cert *x509.Certificate
	rest := []byte(bundle)
	for {
		var b *pem.Block
		if b, rest = pem.Decode(rest); b == nil {
			break
		}
		switch b.Type {
		case "PRIVATE KEY":
			k, err := x509.ParsePKCS8PrivateKey(b.Bytes)
			if err != nil {
				return nil, nil, err
			}
			rk, ok := k.(*rsa.PrivateKey)
			if !ok {
				return nil, nil, errors.New("SP key is not RSA")
			}
			key = rk
		case "CERTIFICATE":
			c, err := x509.ParseCertificate(b.Bytes)
			if err != nil {
				return nil, nil, err
			}
			cert = c
		}
	}
	if key == nil || cert == nil {
		return nil, nil, errors.New("stored SP key bundle is incomplete")
	}
	return key, cert, nil
}

// newSPKeyPair makes an RSA-2048 key and a 10-year self-signed certificate for SAML signing.
// SAML pins certificates through metadata, so a self-signed one is the norm.
func newSPKeyPair(cn string) (keyPEM, certPEM string, err error) {
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return "", "", err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if err != nil {
		return "", "", err
	}
	if len(cn) > 64 {
		cn = cn[:64] // X.509 CommonName limit; the subject is informational
	}
	tmpl := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: cn},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().AddDate(10, 0, 0),
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &k.PublicKey, k)
	if err != nil {
		return "", "", err
	}
	kder, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		return "", "", err
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: kder})),
		string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), nil
}

func validRole(r store.Role, def store.Role) (store.Role, error) {
	if r == "" {
		return def, nil
	}
	if r.Rank() == 0 || r == store.RoleOwner {
		return "", fmt.Errorf("%w: role must be viewer, member or admin", store.ErrInvalid)
	}
	return r, nil
}

func (a *API) putSAML(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Enabled           bool       `json:"enabled"`
		IdPMetadataURL    string     `json:"idp_metadata_url"`
		IdPMetadataXML    string     `json:"idp_metadata_xml"`
		DefaultRole       store.Role `json:"default_role"`
		AttrEmail         string     `json:"attribute_email"`
		AttrName          string     `json:"attribute_name"`
		AttrGroups        string     `json:"attribute_groups"`
		AllowIdPInitiated bool       `json:"allow_idp_initiated"`
		Enforce           bool       `json:"enforce"`
	}
	if !decode(w, r, &in) {
		return
	}
	ctx, org := r.Context(), r.PathValue("org")
	rl, err := validRole(in.DefaultRole, store.RoleMember)
	if err != nil {
		fail(w, err)
		return
	}
	for _, s := range []string{in.AttrEmail, in.AttrName, in.AttrGroups} {
		if len(s) > 256 {
			fail(w, fmt.Errorf("%w: attribute names are at most 256 characters", store.ErrInvalid))
			return
		}
	}
	cur, err := a.S.GetIdentity(ctx, org)
	if err != nil {
		fail(w, err)
		return
	}
	xmlDoc, metaURL, fetched := cur.SAML.IdPMetadataXML, cur.SAML.IdPMetadataURL, cur.SAML.MetadataFetchedAt
	switch {
	case in.IdPMetadataURL != "":
		xmlDoc, err = a.fetchIdPMetadata(ctx, in.IdPMetadataURL)
		if err != nil {
			fail(w, fmt.Errorf("%w: idp_metadata_url: %s", store.ErrInvalid, err.Error()))
			return
		}
		now := time.Now().UTC()
		metaURL, fetched = in.IdPMetadataURL, &now
	case in.IdPMetadataXML != "":
		xmlDoc, metaURL, fetched = in.IdPMetadataXML, "", nil
	}
	var entityID string
	if xmlDoc != "" {
		md, err := parseIdPMetadata(xmlDoc)
		if err != nil {
			fail(w, fmt.Errorf("%w: IdP metadata: %s", store.ErrInvalid, err.Error()))
			return
		}
		entityID = md.EntityID
	} else if in.Enabled {
		fail(w, fmt.Errorf("%w: idp_metadata_url or idp_metadata_xml is required to enable SAML", store.ErrInvalid))
		return
	}
	if in.Enforce {
		if !in.Enabled {
			fail(w, fmt.Errorf("%w: enforce needs SAML enabled", store.ErrInvalid))
			return
		}
		if ds, err := a.verifiedDomains(ctx, org); err != nil || len(ds) == 0 {
			fail(w, orInvalid(err, "enforce needs at least one verified domain"))
			return
		}
	}
	if _, err := a.ensureSPKey(ctx, org); err != nil {
		fail(w, err)
		return
	}
	id, err := a.S.UpdateIdentity(ctx, org, func(x *store.Identity) error {
		cert := x.SAML.SPCertificate
		x.SAML = store.SAMLConfig{Enabled: in.Enabled, IdPMetadataURL: metaURL, IdPMetadataXML: xmlDoc, IdPEntityID: entityID,
			MetadataFetchedAt: fetched, DefaultRole: rl, AttrEmail: strings.TrimSpace(in.AttrEmail), AttrName: strings.TrimSpace(in.AttrName),
			AttrGroups: strings.TrimSpace(in.AttrGroups), AllowIdPInitiated: in.AllowIdPInitiated, Enforce: in.Enforce, SPCertificate: cert}
		return nil
	})
	if err != nil {
		fail(w, err)
		return
	}
	a.audit(r, org, "sso.saml.update", org, fmt.Sprintf("enabled=%v enforce=%v idp=%s idp_initiated=%v", in.Enabled, in.Enforce, entityID, in.AllowIdPInitiated))
	a.writeSSO(w, id)
}

// parseIdPMetadata parses IdP metadata and checks it can sign us in: a redirect-binding SSO
// endpoint and a signing certificate.
func parseIdPMetadata(doc string) (*saml.EntityDescriptor, error) {
	md, err := samlsp.ParseMetadata([]byte(doc))
	if err != nil {
		return nil, errors.New("not valid SAML metadata")
	}
	sp := saml.ServiceProvider{IDPMetadata: md}
	if sp.GetSSOBindingLocation(saml.HTTPRedirectBinding) == "" {
		return nil, errors.New("no SingleSignOnService with the HTTP-Redirect binding")
	}
	hasCert := false
	for _, d := range md.IDPSSODescriptors {
		for _, k := range d.KeyDescriptors {
			if (k.Use == "" || k.Use == "signing") && len(k.KeyInfo.X509Data.X509Certificates) > 0 {
				hasCert = true
			}
		}
	}
	if !hasCert {
		return nil, errors.New("no signing certificate")
	}
	return md, nil
}

// fetchIdPMetadata downloads tenant-supplied metadata through the SSRF guard: public https
// only (any http(s) when private URLs are allowed), no redirects, 1 MiB, 10 s.
func (a *API) fetchIdPMetadata(ctx context.Context, raw string) (string, error) {
	if !a.cfg.AllowPrivateURLs {
		if !strings.HasPrefix(raw, "https://") {
			return "", errors.New("must be an https URL")
		}
		if err := gateway.CheckPublicURL(raw); err != nil {
			return "", err
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return "", errors.New("invalid URL")
	}
	res, err := a.metaClient().Do(req)
	if err != nil {
		return "", errors.New("could not fetch the metadata")
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("metadata URL answered HTTP %d", res.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, 1<<20+1))
	if err != nil || len(b) > 1<<20 {
		return "", errors.New("metadata is larger than 1 MiB or unreadable")
	}
	return string(b), nil
}

func (a *API) metaClient() *http.Client {
	if a.MetadataClient != nil {
		return a.MetadataClient
	}
	dialer := gateway.PublicDialer(10 * time.Second)
	if a.cfg.AllowPrivateURLs {
		dialer = &net.Dialer{Timeout: 10 * time.Second}
	}
	return &http.Client{Timeout: 10 * time.Second,
		Transport:     &http.Transport{Proxy: nil, DialContext: dialer.DialContext, TLSHandshakeTimeout: 10 * time.Second},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func (a *API) putGroupMappings(w http.ResponseWriter, r *http.Request) {
	var in []store.GroupMapping
	if !decode(w, r, &in) {
		return
	}
	if len(in) > 200 {
		fail(w, fmt.Errorf("%w: at most 200 group mappings", store.ErrInvalid))
		return
	}
	seen := map[string]bool{}
	for i := range in {
		m := &in[i]
		m.Group, m.WorkspaceID = strings.TrimSpace(m.Group), strings.TrimSpace(m.WorkspaceID)
		if m.Group == "" || len(m.Group) > 256 || seen[strings.ToLower(m.Group)] {
			fail(w, fmt.Errorf("%w: each mapping needs a unique group name (at most 256 characters)", store.ErrInvalid))
			return
		}
		seen[strings.ToLower(m.Group)] = true
		if m.Role == "" && m.WorkspaceID == "" {
			fail(w, fmt.Errorf("%w: mapping %q needs a role", store.ErrInvalid, m.Group))
			return
		}
		if m.Role != "" {
			if _, err := validRole(m.Role, ""); err != nil {
				fail(w, err)
				return
			}
		}
	}
	org := r.PathValue("org")
	id, err := a.S.UpdateIdentity(r.Context(), org, func(x *store.Identity) error { x.GroupMappings = in; return nil })
	if err != nil {
		fail(w, err)
		return
	}
	a.audit(r, org, "sso.group_mappings.update", org, fmt.Sprintf("%d mappings", len(in)))
	a.writeSSO(w, id)
}

func (a *API) putSCIM(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Enabled     bool       `json:"enabled"`
		DefaultRole store.Role `json:"default_role"`
	}
	if !decode(w, r, &in) {
		return
	}
	rl, err := validRole(in.DefaultRole, store.RoleMember)
	if err != nil {
		fail(w, err)
		return
	}
	org := r.PathValue("org")
	id, err := a.S.UpdateIdentity(r.Context(), org, func(x *store.Identity) error {
		if in.Enabled && x.SCIM.TokenPrefix == "" {
			return fmt.Errorf("%w: create a SCIM token first (POST /sso/scim/token)", store.ErrInvalid)
		}
		x.SCIM.Enabled, x.SCIM.DefaultRole = in.Enabled, rl
		return nil
	})
	if err != nil {
		fail(w, err)
		return
	}
	a.audit(r, org, "sso.scim.update", org, fmt.Sprintf("enabled=%v default_role=%s", in.Enabled, rl))
	a.writeSSO(w, id)
}

// rotateSCIMToken issues a new SCIM bearer token (shown once) and enables SCIM; the previous
// token stops working at once.
func (a *API) rotateSCIMToken(w http.ResponseWriter, r *http.Request) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		fail(w, err)
		return
	}
	org, secret := r.PathValue("org"), ScimPrefix+base64.RawURLEncoding.EncodeToString(b)
	if _, err := a.S.GetOrg(r.Context(), org); err != nil {
		fail(w, err)
		return
	}
	if err := a.S.SetSCIMTokenHash(r.Context(), org, gateway.HashKey(secret)); err != nil {
		fail(w, err)
		return
	}
	now := time.Now().UTC()
	_, err := a.S.UpdateIdentity(r.Context(), org, func(x *store.Identity) error {
		x.SCIM.Enabled, x.SCIM.TokenPrefix, x.SCIM.TokenCreatedAt = true, secret[:12], &now
		if x.SCIM.DefaultRole == "" {
			x.SCIM.DefaultRole = store.RoleMember
		}
		return nil
	})
	if err != nil {
		fail(w, err)
		return
	}
	a.audit(r, org, "sso.scim.token", org, "")
	writeJSON(w, http.StatusCreated, map[string]any{"token": secret, "scim_base_url": a.cfg.PublicURL + "/scim/v2/" + org})
}

// ---- discovery and enforcement ----

// samlOrgFor returns the org whose verified domain covers email and its identity settings,
// when that org has SAML enabled.
func (a *API) samlOrgFor(ctx context.Context, email string) (*store.Identity, bool) {
	d := store.EmailDomain(email)
	if d == "" {
		return nil, false
	}
	org, err := a.S.DomainOrg(ctx, d)
	if err != nil {
		return nil, false
	}
	id, err := a.S.GetIdentity(ctx, org)
	if err != nil || !id.SAML.Enabled {
		return nil, false
	}
	return id, true
}

// ssoRequired returns the SAML login URL when password sign-in is refused for email.
func (a *API) ssoRequired(ctx context.Context, email string) (string, bool) {
	id, ok := a.samlOrgFor(ctx, email)
	if !ok || !id.SAML.Enforce {
		return "", false
	}
	return "/api/sso/saml/" + id.OrgID + "/login", true
}

func writeSSORequired(w http.ResponseWriter, loginURL string) {
	writeJSON(w, http.StatusForbidden, map[string]any{"error": map[string]string{
		"message": "your organization requires single sign-on: use SSO", "code": "sso_required", "login_url": loginURL}})
}

// ssoDiscovery tells the login page how an email signs in: saml (its domain is verified by an
// org with SAML on), oidc (the platform IdP accepts its domain) or password.
func (a *API) ssoDiscovery(w http.ResponseWriter, r *http.Request) {
	if n, _ := a.Lim.Incr(r.Context(), "sso-discovery:"+clientIP(r), 1); n > 60 {
		writeErr(w, http.StatusTooManyRequests, "too many requests, wait a minute")
		return
	}
	email := r.URL.Query().Get("email")
	if id, ok := a.samlOrgFor(r.Context(), email); ok {
		writeJSON(w, 200, map[string]any{"method": "saml", "login_url": "/api/sso/saml/" + id.OrgID + "/login", "password_allowed": !id.SAML.Enforce})
		return
	}
	if a.oidc != nil {
		d := store.EmailDomain(email)
		if len(a.oidc.cfg.AllowedDomains) == 0 || slices.Contains(a.oidc.cfg.AllowedDomains, d) {
			writeJSON(w, 200, map[string]any{"method": "oidc", "login_url": "/api/auth/oidc/login", "password_allowed": true})
			return
		}
	}
	writeJSON(w, 200, map[string]any{"method": "password", "login_url": "", "password_allowed": true})
}
