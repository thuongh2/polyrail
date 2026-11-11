package admin

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/polyrail/polyrail/internal/store"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/oauth2"
)

const (
	sessionCookie = "gw_session"
	sessionTTL    = 3 * 24 * time.Hour
)

// signer issues stateless, HMAC-signed tokens: base64(payload) + "." + hex(mac).
type signer struct{ key []byte }

func newSigner(masterKey string) signer {
	// The label keeps the pre-rename name on purpose: changing it would sign out every session (D24).
	k := sha256.Sum256([]byte("agent-gateway/session/v1:" + masterKey))
	return signer{key: k[:]}
}

func (s signer) mac(payload string) string {
	m := hmac.New(sha256.New, s.key)
	m.Write([]byte(payload))
	return hex.EncodeToString(m.Sum(nil))
}

// sign issues a token for one purpose ("sess", "oidc") that expires after ttl.
func (s signer) sign(purpose string, ttl time.Duration, parts ...string) string {
	all := append([]string{purpose, strconv.FormatInt(time.Now().Add(ttl).Unix(), 10)}, parts...)
	p := base64.RawURLEncoding.EncodeToString([]byte(strings.Join(all, "|")))
	return p + "." + s.mac(p)
}

// verify returns the parts of a valid, unexpired token issued for purpose.
func (s signer) verify(purpose, tok string) ([]string, bool) {
	p, mac, ok := strings.Cut(tok, ".")
	if !ok || !hmac.Equal([]byte(mac), []byte(s.mac(p))) {
		return nil, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(p)
	if err != nil {
		return nil, false
	}
	parts := strings.Split(string(raw), "|")
	if len(parts) < 2 || parts[0] != purpose {
		return nil, false
	}
	exp, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return nil, false
	}
	return parts[2:], true
}

// sessionEpoch changes when the password changes or the user signs out, ending every session.
func sessionEpoch(u *store.User) string {
	h := sha256.Sum256([]byte(u.PasswordHash + "|" + strconv.Itoa(u.SessionGen)))
	return hex.EncodeToString(h[:8])
}

func (a *API) setSession(w http.ResponseWriter, u *store.User) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: a.sign.sign("sess", sessionTTL, u.ID, sessionEpoch(u)),
		Path: "/", HttpOnly: true, Secure: a.cfg.SecureCookies, SameSite: http.SameSiteLaxMode, MaxAge: int(sessionTTL.Seconds())})
}

// currentUser resolves the session cookie.
func (a *API) currentUser(r *http.Request) (*store.User, error) {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return nil, errUnauthorized
	}
	parts, ok := a.sign.verify("sess", c.Value)
	if !ok || len(parts) != 2 {
		return nil, errUnauthorized
	}
	u, err := a.S.GetUser(r.Context(), parts[0])
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, errUnauthorized
		}
		return nil, err
	}
	if u.Disabled || sessionEpoch(u) != parts[1] {
		return nil, errUnauthorized
	}
	return u, nil
}

func HashPassword(pw string) (string, error) {
	if len(pw) < 10 {
		return "", fmt.Errorf("%w: password must be at least 10 characters", store.ErrInvalid)
	}
	if len(pw) > 72 {
		return "", fmt.Errorf("%w: password must be at most 72 bytes", store.ErrInvalid)
	}
	h, err := bcrypt.GenerateFromPassword([]byte(pw), 12)
	return string(h), err
}

// dummyHash keeps login timing similar for unknown emails.
var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("dummy-password-x"), 12)

func (a *API) login(w http.ResponseWriter, r *http.Request) {
	var in struct{ Email, Password string }
	if !decode(w, r, &in) {
		return
	}
	n, _ := a.Lim.Incr(r.Context(), "login:"+clientIP(r), 1)
	m, _ := a.Lim.Incr(r.Context(), "login-email:"+strings.ToLower(strings.TrimSpace(in.Email)), 1)
	if n > 20 || m > 10 {
		writeErr(w, http.StatusTooManyRequests, "too many login attempts, wait a minute")
		return
	}
	u, err := a.S.GetUserByEmail(r.Context(), in.Email)
	if loginURL, enforced := a.ssoRequired(r.Context(), in.Email); enforced {
		// The org requires SSO for its verified domains. Break-glass: a platform operator with
		// the right password still signs in. Everyone else gets the same answer whatever the
		// password, so the response does not reveal operators or valid passwords.
		ok := err == nil && u.Operator && !u.Disabled && u.PasswordHash != "" &&
			bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(in.Password)) == nil
		if !ok {
			if err != nil || u.PasswordHash == "" {
				_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(in.Password))
			}
			writeSSORequired(w, loginURL)
			return
		}
		slog.Warn("password sign-in past enforced SSO (operator break-glass)", "user", u.ID)
		a.setSession(w, u)
		a.me(w, r.WithContext(context.WithValue(r.Context(), userCtx{}, u)))
		return
	}
	if err != nil || u.PasswordHash == "" || u.Disabled {
		_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(in.Password))
		writeErr(w, http.StatusUnauthorized, "invalid email or password")
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(in.Password)) != nil {
		writeErr(w, http.StatusUnauthorized, "invalid email or password")
		return
	}
	a.setSession(w, u)
	a.me(w, r.WithContext(context.WithValue(r.Context(), userCtx{}, u)))
}

// logout ends every session of the user (a stolen cookie stops working too).
func (a *API) logout(w http.ResponseWriter, r *http.Request) {
	if u, err := a.currentUser(r); err == nil {
		if _, err := a.S.UpdateUser(r.Context(), u.ID, func(x *store.User) error { x.SessionGen++; return nil }); err != nil {
			fail(w, err) // the other sessions are still valid: say so instead of a silent 204
			return
		}
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true})
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) signup(w http.ResponseWriter, r *http.Request) {
	if !a.cfg.AllowSignup {
		writeErr(w, http.StatusForbidden, "sign-up is disabled on this gateway")
		return
	}
	var in struct{ Email, Password, Name string }
	if !decode(w, r, &in) {
		return
	}
	if n, _ := a.Lim.Incr(r.Context(), "signup:"+clientIP(r), 1); n > 5 {
		writeErr(w, http.StatusTooManyRequests, "too many sign-ups, wait a minute")
		return
	}
	if loginURL, enforced := a.ssoRequired(r.Context(), in.Email); enforced {
		writeSSORequired(w, loginURL)
		return
	}
	hash, err := HashPassword(in.Password)
	if err != nil {
		fail(w, err)
		return
	}
	u := &store.User{Email: in.Email, Name: in.Name, PasswordHash: hash}
	if err := a.S.CreateUser(r.Context(), u); err != nil {
		fail(w, err)
		return
	}
	org := &store.Org{Name: orDefault(in.Name, u.Email) + "'s org", Prepaid: a.cfg.SignupPrepaid}
	if err := a.S.CreateOrg(r.Context(), org, u.ID); err != nil {
		fail(w, err)
		return
	}
	a.setSession(w, u)
	writeJSON(w, http.StatusCreated, map[string]any{"user": u, "org": org})
}

func (a *API) changePassword(w http.ResponseWriter, r *http.Request) {
	u := user(r)
	var in struct{ Current, New string }
	if !decode(w, r, &in) {
		return
	}
	if u.PasswordHash != "" && bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(in.Current)) != nil {
		writeErr(w, http.StatusUnauthorized, "current password is wrong")
		return
	}
	hash, err := HashPassword(in.New)
	if err != nil {
		fail(w, err)
		return
	}
	u, err = a.S.UpdateUser(r.Context(), u.ID, func(x *store.User) error { x.PasswordHash = hash; return nil })
	if err != nil {
		fail(w, err)
		return
	}
	a.setSession(w, u) // other sessions are invalidated by the epoch change
	w.WriteHeader(http.StatusNoContent)
}

// ---- OIDC SSO ----

type OIDCConfig struct {
	Issuer, ClientID, ClientSecret, RedirectURL string
	AllowedDomains                              []string
	DefaultOrg                                  string
	DefaultRole                                 store.Role
	TrustUnverifiedEmail                        bool // some IdPs (e.g. Entra ID) omit email_verified
}

type oidcClient struct {
	cfg      OIDCConfig
	oauth    oauth2.Config
	verifier *oidc.IDTokenVerifier
}

func newOIDC(ctx context.Context, cfg OIDCConfig) (*oidcClient, error) {
	if cfg.DefaultOrg != "" && len(cfg.AllowedDomains) == 0 {
		return nil, errors.New("OIDC_DEFAULT_ORG requires OIDC_ALLOWED_DOMAINS, or any IdP account would join that org")
	}
	if cfg.DefaultRole.Rank() == 0 || cfg.DefaultRole == store.RoleOwner {
		return nil, errors.New("OIDC_DEFAULT_ROLE must be viewer, member or admin")
	}
	p, err := oidc.NewProvider(ctx, cfg.Issuer)
	if err != nil {
		return nil, fmt.Errorf("oidc discovery: %w", err)
	}
	return &oidcClient{cfg: cfg,
		oauth: oauth2.Config{ClientID: cfg.ClientID, ClientSecret: cfg.ClientSecret, Endpoint: p.Endpoint(),
			RedirectURL: cfg.RedirectURL, Scopes: []string{oidc.ScopeOpenID, "email", "profile"}},
		verifier: p.Verifier(&oidc.Config{ClientID: cfg.ClientID}),
	}, nil
}

func (a *API) oidcLogin(w http.ResponseWriter, r *http.Request) {
	if a.oidc == nil {
		writeErr(w, http.StatusNotFound, "SSO is not configured")
		return
	}
	state, nonce := store.NewID()+store.NewID(), store.NewID()+store.NewID()
	http.SetCookie(w, &http.Cookie{Name: "gw_oidc", Value: a.sign.sign("oidc", 10*time.Minute, state, nonce),
		Path: "/api/auth/oidc", HttpOnly: true, Secure: a.cfg.SecureCookies, SameSite: http.SameSiteLaxMode, MaxAge: 600})
	http.Redirect(w, r, a.oidc.oauth.AuthCodeURL(state, oidc.Nonce(nonce)), http.StatusFound)
}

func (a *API) oidcCallback(w http.ResponseWriter, r *http.Request) {
	if a.oidc == nil {
		writeErr(w, http.StatusNotFound, "SSO is not configured")
		return
	}
	c, err := r.Cookie("gw_oidc")
	parts, ok := []string(nil), false
	if err == nil {
		parts, ok = a.sign.verify("oidc", c.Value)
	}
	if !ok || len(parts) != 2 || r.URL.Query().Get("state") != parts[0] {
		writeErr(w, http.StatusBadRequest, "invalid SSO state, start the login again")
		return
	}
	ctx := r.Context()
	tok, err := a.oidc.oauth.Exchange(ctx, r.URL.Query().Get("code"))
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "SSO code exchange failed")
		return
	}
	raw, _ := tok.Extra("id_token").(string)
	idt, err := a.oidc.verifier.Verify(ctx, raw)
	if err != nil || idt.Nonce != parts[1] {
		writeErr(w, http.StatusUnauthorized, "invalid SSO token")
		return
	}
	var claims struct {
		Email    string `json:"email"`
		Verified *bool  `json:"email_verified"`
		Name     string `json:"name"`
	}
	err = idt.Claims(&claims)
	verified := claims.Verified != nil && *claims.Verified || (claims.Verified == nil && a.oidc.cfg.TrustUnverifiedEmail)
	if err != nil || claims.Email == "" || !verified {
		writeErr(w, http.StatusForbidden, "SSO account has no verified email")
		return
	}
	_, domain, _ := strings.Cut(strings.ToLower(claims.Email), "@")
	if len(a.oidc.cfg.AllowedDomains) > 0 && !slices.Contains(a.oidc.cfg.AllowedDomains, domain) {
		writeErr(w, http.StatusForbidden, "your email domain is not allowed on this gateway")
		return
	}
	subject := idt.Issuer + "|" + idt.Subject
	u, err := a.S.GetUserByEmail(ctx, claims.Email)
	switch {
	case errors.Is(err, store.ErrNotFound):
		u = &store.User{Email: claims.Email, Name: claims.Name, SSOSubject: subject}
		err = a.S.CreateUser(ctx, u)
	case err != nil:
	case u.SSOSubject == subject:
	case u.SSOSubject == "" && u.PasswordHash == "":
		// an invited account nobody has claimed with a password: link it to this identity
		u, err = a.S.UpdateUser(ctx, u.ID, func(x *store.User) error { x.SSOSubject = subject; return nil })
	default:
		// never silently link SSO to an account someone else may have created with this email
		writeErr(w, http.StatusConflict, "an account with this email already exists; sign in with its password or ask an operator")
		return
	}
	if err != nil {
		fail(w, err)
		return
	}
	if u.Disabled {
		writeErr(w, http.StatusForbidden, "account disabled")
		return
	}
	if org := a.oidc.cfg.DefaultOrg; org != "" {
		if _, err := a.S.GetMember(ctx, org, u.ID); errors.Is(err, store.ErrNotFound) {
			if err := a.S.SetMember(ctx, store.Member{OrgID: org, UserID: u.ID, Role: a.oidc.cfg.DefaultRole}); err != nil {
				slog.Error("sso auto-join failed", "org", org, "err", err)
			}
		}
	}
	a.setSession(w, u)
	http.Redirect(w, r, "/app/", http.StatusFound)
}
