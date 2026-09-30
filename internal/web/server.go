// Package web serves the admin API and the embedded admin UI.
package web

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"path"
	"runtime"
	"strings"
	"time"

	"github.com/juancer/milo/internal/app"
	"github.com/juancer/milo/internal/auth"
	"github.com/juancer/milo/internal/features"
	"github.com/juancer/milo/internal/i18n"
	"github.com/juancer/milo/internal/store"
)

const (
	cookieName        = "milo_session"
	clientHeader      = "X-Requested-With"
	clientHeaderValue = "milo-admin"
	sessionTTL        = 12 * time.Hour
	maxBody           = 4 << 10
)

// featureTimeout bounds a feature switch, as for restarts after a credential change.
var featureTimeout = 20 * time.Second

type server struct {
	app      *app.App
	version  string
	accounts *auth.Accounts
	sessions *auth.Sessions
	limiter  *auth.Limiter // sign-in attempts per client address
	// Separate buckets, so a mistyped password change or restore passphrase does not count
	// against sign-in from the same address.
	pwLimiter      *auth.Limiter // password-change attempts
	restoreLimiter *auth.Limiter // restore passphrase attempts (read by backup.go)
	started        time.Time
}

type ctxKey struct{}

// adminFrom returns the signed-in administrator of an authenticated request.
func adminFrom(r *http.Request) store.AdminUser {
	u, _ := r.Context().Value(ctxKey{}).(store.AdminUser)
	return u
}

// New returns the HTTP handler. assets is the built Astro UI (rooted at index.html).
func New(a *app.App, version string, assets fs.FS) http.Handler {
	return newServer(a, version).handler(assets)
}

func newServer(a *app.App, version string) *server {
	return &server{
		app:      a,
		version:  version,
		accounts: auth.NewAccounts(a.Store),
		sessions: auth.NewSessions(sessionTTL),
		limiter:  auth.NewLimiter(5, 15*time.Minute),

		pwLimiter:      auth.NewLimiter(5, 15*time.Minute),
		restoreLimiter: auth.NewLimiter(5, 15*time.Minute),
		started:        time.Now(),
	}
}

func (s *server) handler(assets fs.FS) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", s.health)
	mux.HandleFunc("GET /api/session", s.sessionInfo)
	mux.HandleFunc("POST /api/login", s.login)
	mux.HandleFunc("POST /api/setup", s.setup)
	mux.HandleFunc("POST /api/logout", s.logout)
	s.accountRoutes(mux)
	mux.Handle("GET /api/status", s.requireAuth(s.status))
	mux.Handle("GET /api/features", s.requireAuth(s.listFeatures))
	mux.Handle("PUT /api/features/{name}", s.requireAuth(s.setFeature))
	mux.Handle("GET /api/audit", s.requireAuth(s.listAudit))
	s.buttonRoutes(mux)
	s.telegramRoutes(mux)
	s.settingsRoutes(mux)
	s.languageRoutes(mux)
	s.scheduleRoutes(mux)
	s.assistantRoutes(mux)
	s.wifiRoutes(mux)
	s.systemRoutes(mux)
	s.wakeRoutes(mux)
	s.skillRoutes(mux)
	s.backupRoutes(mux)
	s.simRoutes(mux)
	mux.Handle("/", noListing(assets, http.FileServerFS(assets)))
	return securityHeaders(s.withLanguage(csrfGuard(mux)))
}

// noListing answers 404 for a directory that has no index.html, instead of listing it.
func noListing(assets fs.FS, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p := r.URL.Path; strings.HasSuffix(p, "/") {
			if _, err := fs.Stat(assets, path.Join(strings.TrimPrefix(p, "/"), "index.html")); err != nil {
				http.NotFound(w, r)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// securityHeaders sets a strict policy: the UI is self-contained, so nothing
// external is ever loaded or embedded.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

// csrfGuard blocks cross-site state changes. Browsers cannot attach a custom
// header cross-origin without a CORS preflight, which this server never
// grants; the Origin check is defence in depth.
func csrfGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
		default:
			if r.Header.Get(clientHeader) != clientHeaderValue {
				writeError(w, http.StatusForbidden, "web.err.missing_client_header")
				return
			}
			if origin := r.Header.Get("Origin"); origin != "" {
				if u, err := url.Parse(origin); err != nil || u.Host != r.Host {
					writeError(w, http.StatusForbidden, "web.err.cross_origin_request_refused")
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

// passwordChangeOK lists what a freshly generated password may do before it is changed.
var passwordChangeOK = map[string]bool{"/api/account/password": true, "/api/logout": true}

func (s *server) requireAuth(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, ok := s.authenticated(r)
		if !ok {
			writeError(w, http.StatusUnauthorized, "web.err.authentication_required")
			return
		}
		if user.MustChange && !passwordChangeOK[r.URL.Path] {
			writeError(w, http.StatusForbidden, "password_change_required")
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, user)))
	})
}

// authenticated resolves the session cookie to an administrator. A session
// stops working the moment its user's password changes or the user is removed.
func (s *server) authenticated(r *http.Request) (store.AdminUser, bool) {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return store.AdminUser{}, false
	}
	id, fp, ok := s.sessions.Lookup(c.Value)
	if !ok {
		return store.AdminUser{}, false
	}
	user, err := s.app.Store.GetAdmin(r.Context(), id)
	if err != nil || !auth.Matches(fp, user.PasswordHash) {
		s.sessions.Delete(c.Value)
		return store.AdminUser{}, false
	}
	return user, true
}

func (s *server) health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.app.Store.Ping(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *server) sessionInfo(w http.ResponseWriter, r *http.Request) {
	user, ok := s.authenticated(r)
	info := map[string]any{"authenticated": ok}
	if !ok {
		// A fresh device has no administrator yet: the UI then offers the first-run setup.
		if n, err := s.app.Store.AdminCount(r.Context()); err == nil && n == 0 {
			info["setup_required"] = true
		}
	}
	if ok {
		info["username"], info["is_owner"], info["must_change_password"] = user.Username, user.IsOwner, user.MustChange
	}
	writeJSON(w, http.StatusOK, info)
}

func (s *server) startSession(w http.ResponseWriter, user store.AdminUser) error {
	token, err := s.sessions.Create(user.ID, auth.Fingerprint(user.PasswordHash))
	if err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: token, Path: "/",
		MaxAge:   int(s.sessions.TTL().Seconds()),
		HttpOnly: true, SameSite: http.SameSiteStrictMode,
		// Secure is off because the admin UI is plain HTTP on the local network.
	})
	return nil
}

func (s *server) login(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	name := strings.ToLower(strings.TrimSpace(body.Username))
	if len(name) > 40 {
		name = name[:40]
	}
	// Attempts are throttled per address only. A limit per username would let anyone on the
	// network lock the owner out just by naming "admin". Each attempt is counted before it
	// runs, so parallel requests cannot get past the limit together.
	if !s.limiter.Try(ip) {
		writeError(w, http.StatusTooManyRequests, "web.err.too_many_failed_attempts_try_again_later")
		return
	}
	user, err := s.accounts.Authenticate(r.Context(), body.Username, body.Password)
	if errors.Is(err, auth.ErrBusy) {
		s.limiter.Refund(ip) // the server was busy: not a wrong-password guess
		writeError(w, http.StatusServiceUnavailable, "web.err.the_server_is_busy_try_again_shortly")
		return
	}
	if errors.Is(err, auth.ErrBadCredentials) {
		// Only an existing account name is recorded: anything else may be a password typed
		// into the username field.
		target := ""
		if _, err := s.app.Store.GetAdminByName(r.Context(), name); err == nil {
			target = name
		}
		s.app.Audit.Record(r.Context(), "anonymous", "admin.login_failed", target, map[string]string{"ip": ip})
		writeError(w, http.StatusUnauthorized, "web.err.incorrect_username_or_password")
		return
	}
	if err != nil {
		s.app.Log.Error("login", "error", err)
		writeError(w, http.StatusInternalServerError, "web.err.internal_error")
		return
	}
	s.limiter.Refund(ip) // give back only this attempt; earlier failures stay counted
	if err := s.startSession(w, user); err != nil {
		writeError(w, http.StatusInternalServerError, "web.err.internal_error")
		return
	}
	s.app.Audit.Record(r.Context(), user.Username, "admin.login", "", map[string]string{"ip": ip})
	writeJSON(w, http.StatusOK, map[string]any{"authenticated": true, "username": user.Username,
		"is_owner": user.IsOwner, "must_change_password": user.MustChange})
}

// setup is the first-run onboarding: it creates the owner chosen in the browser, but only
// while no administrator exists, then signs that person in.
func (s *server) setup(w http.ResponseWriter, r *http.Request) {
	// Whoever completes setup becomes the owner, so it is limited to the home network. The decision
	// uses the TCP peer only: a forwarding header is client-supplied and proves nothing.
	if !fromHomeNetwork(r) {
		writeError(w, http.StatusForbidden, "web.err.setup_from_the_home_network_only")
		return
	}
	ip := clientIP(r)
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if !s.limiter.Try(ip) {
		writeError(w, http.StatusTooManyRequests, "web.err.too_many_failed_attempts_try_again_later")
		return
	}
	user, err := s.accounts.Setup(r.Context(), body.Username, body.Password)
	switch {
	case errors.Is(err, auth.ErrSetupDone):
		writeError(w, http.StatusConflict, "web.err.setup_already_done")
		return
	case errors.Is(err, auth.ErrBusy):
		s.limiter.Refund(ip)
		writeError(w, http.StatusServiceUnavailable, "web.err.the_server_is_busy_try_again_shortly")
		return
	case errors.Is(err, auth.ErrUsername):
		s.limiter.Refund(ip)
		writeError(w, http.StatusBadRequest, "web.err.username_rules")
		return
	case err != nil && auth.ValidatePassword(body.Password) != nil:
		s.limiter.Refund(ip)
		writeError(w, http.StatusBadRequest, "web.err.password_rules", "min", auth.MinPasswordLength)
		return
	case err != nil:
		s.app.Log.Error("setup", "error", err)
		writeError(w, http.StatusInternalServerError, "web.err.internal_error")
		return
	}
	s.limiter.Refund(ip)
	if err := s.startSession(w, user); err != nil {
		writeError(w, http.StatusInternalServerError, "web.err.internal_error")
		return
	}
	s.app.Audit.Record(r.Context(), user.Username, "admin.setup", "", map[string]string{"ip": ip})
	writeJSON(w, http.StatusOK, map[string]any{"authenticated": true, "username": user.Username,
		"is_owner": user.IsOwner, "must_change_password": user.MustChange})
}

func (s *server) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(cookieName); err == nil {
		s.sessions.Delete(c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	writeJSON(w, http.StatusOK, map[string]bool{"authenticated": false})
}

func (s *server) status(w http.ResponseWriter, r *http.Request) {
	version, err := s.app.Store.SchemaVersion(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "web.err.internal_error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"audio":           s.app.Audio.Status(r.Context()),
		"hardware_errors": s.app.HardwareErrors,
		"version":         s.version,
		"uptime_seconds":  int64(time.Since(s.started).Seconds()),
		"hardware":        s.app.Cfg.Hardware,
		"schema_version":  version,
		"go_version":      runtime.Version(),
		"arch":            runtime.GOOS + "/" + runtime.GOARCH,
	})
}

func (s *server) listFeatures(w http.ResponseWriter, r *http.Request) {
	list, err := s.app.Features.List(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "web.err.internal_error")
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *server) setFeature(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.Enabled == nil {
		writeError(w, http.StatusBadRequest, "web.err.enabled_is_required")
		return
	}
	// Start may call a network service while the manager holds its lock; bound it.
	ctx, cancel := context.WithTimeout(r.Context(), featureTimeout)
	defer cancel()
	err := s.app.Features.SetEnabled(ctx, name, *body.Enabled)
	if errors.Is(err, features.ErrUnknownFeature) {
		writeError(w, http.StatusNotFound, "web.err.unknown_feature")
		return
	}
	if err != nil {
		s.app.Log.Error("set feature", "feature", name, "error", err)
		writeError(w, http.StatusInternalServerError, "web.err.internal_error")
		return
	}
	action := "feature.disabled"
	if *body.Enabled {
		action = "feature.enabled"
	}
	s.audit(r, action, name, nil)
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) listAudit(w http.ResponseWriter, r *http.Request) {
	entries, err := s.app.Store.ListAudit(r.Context(), 50)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "web.err.internal_error")
		return
	}
	type row struct {
		At      time.Time `json:"at"`
		Actor   string    `json:"actor"`
		Action  string    `json:"action"`
		Target  string    `json:"target"`
		Details string    `json:"details"`
	}
	out := make([]row, 0, len(entries))
	for _, e := range entries {
		out = append(out, row{e.At, e.Actor, e.Action, e.Target, e.Details})
	}
	writeJSON(w, http.StatusOK, out)
}

// fromHomeNetwork reports whether the request's TCP peer is on the local network: loopback,
// private (RFC 1918 or IPv6 ULA) or link-local. It reads RemoteAddr only, never X-Forwarded-For.
func fromHomeNetwork(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	addr = addr.Unmap()
	return addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast()
}

// clientIP is the limiter key for a request: the IPv4 address, or the /64 prefix for IPv6
// (one host can use billions of addresses inside its /64).
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if addr, err := netip.ParseAddr(host); err == nil {
		addr = addr.Unmap()
		if addr.Is6() {
			if p, err := addr.Prefix(64); err == nil {
				return p.String()
			}
		}
		return addr.String()
	}
	return host
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	return decodeJSONLimit(w, r, dst, maxBody)
}

func decodeJSONLimit(w http.ResponseWriter, r *http.Request, dst any, limit int64) bool {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, "web.err.invalid_json_body")
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// writeError answers with an error. msg is either an i18n key (web.err.*), translated into the
// interface language with the alternating name, value arguments, or a ready-made message
// from a lower layer, which is sent as is.
func writeError(w http.ResponseWriter, status int, msg string, kv ...any) {
	if strings.HasPrefix(msg, "web.err.") {
		lang := i18n.Default
		if lw, ok := w.(interface{ language() string }); ok {
			lang = lw.language()
		}
		msg = i18n.Tf(lang, msg, kv...)
	}
	writeJSON(w, status, map[string]string{"error": msg})
}

// langWriter lets writeError find the interface language without every handler passing it along.
// The lookup happens only when an error is actually written.
type langWriter struct {
	http.ResponseWriter
	ctx      context.Context
	settings i18n.Settings
}

func (l *langWriter) language() string { return i18n.Interface(l.ctx, l.settings) }

// Unwrap lets http.ResponseController reach the real writer.
func (l *langWriter) Unwrap() http.ResponseWriter { return l.ResponseWriter }

func (s *server) withLanguage(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(&langWriter{ResponseWriter: w, ctx: r.Context(), settings: s.app.Store}, r)
	})
}

// audit records an admin action, ignoring write failures (they are logged).
func (s *server) audit(r *http.Request, action, target string, details map[string]string) {
	actor := "admin"
	if u := adminFrom(r); u.Username != "" {
		actor = u.Username
	}
	s.app.Audit.Record(r.Context(), actor, action, target, details)
}
