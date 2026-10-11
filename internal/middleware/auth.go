// Package middleware provides sessions, authentication, permission checks, CSRF
// protection, security headers, request logging and panic recovery.
package middleware

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"garagems/internal/models"
	"garagems/internal/repository"
)

const (
	SessionCookie = "garage_session"
	PreCSRFCookie = "garage_csrf"
	CSRFField     = "csrf_token"
)

type ctxKey int

const sessionKey ctxKey = 1

// Session is the authenticated state attached to a request.
type Session struct {
	TokenHash string
	User      models.User
	Perms     map[string]bool
	CSRF      string
	Flash     string
}

func (s *Session) Can(p string) bool { return s != nil && s.Perms[p] }

func FromContext(ctx context.Context) *Session {
	s, _ := ctx.Value(sessionKey).(*Session)
	return s
}

// ActorFrom builds the audit/permission actor for the request.
func ActorFrom(r *http.Request) models.Actor {
	s := FromContext(r.Context())
	a := models.Actor{IP: ClientIP(r)}
	if s != nil {
		a.ID, a.Username, a.FullName, a.Perms = s.User.ID, s.User.Username, s.User.FullName, s.Perms
	}
	return a
}

func ClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func RandomToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func hashToken(t string) string {
	h := sha256.Sum256([]byte(t))
	return hex.EncodeToString(h[:])
}

type Auth struct {
	DB      *sql.DB
	TTL     time.Duration
	Secure  bool
	Limiter *LoginLimiter
}

func (m *Auth) cookie(name, value string, maxAge int) *http.Cookie {
	return &http.Cookie{Name: name, Value: value, Path: "/", HttpOnly: true, Secure: m.Secure,
		SameSite: http.SameSiteLaxMode, MaxAge: maxAge}
}

// Load attaches the session (if valid) to the request context.
func (m *Auth) Load(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(SessionCookie)
		if err != nil || c.Value == "" {
			next.ServeHTTP(w, r)
			return
		}
		ctx := r.Context()
		th := hashToken(c.Value)
		row, err := repository.GetSession(ctx, m.DB, th)
		if err != nil {
			if !errors.Is(err, repository.ErrNotFound) {
				log.Printf("session lookup: %v", err)
			}
			http.SetCookie(w, m.cookie(SessionCookie, "", -1))
			next.ServeHTTP(w, r)
			return
		}
		u, err := repository.GetUser(ctx, m.DB, row.UserID)
		if err != nil || !u.Active {
			repository.DeleteSession(ctx, m.DB, th)
			http.SetCookie(w, m.cookie(SessionCookie, "", -1))
			next.ServeHTTP(w, r)
			return
		}
		perms, err := repository.RolePermissions(ctx, m.DB, u.RoleID)
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		// Sliding expiry: extend when less than half the lifetime remains.
		if exp, err := time.Parse(models.TimeLayout, row.ExpiresAt); err == nil && time.Until(exp) < m.TTL/2 {
			repository.ExtendSession(ctx, m.DB, th, time.Now().UTC().Add(m.TTL).Format(models.TimeLayout))
		}
		s := &Session{TokenHash: th, User: u, Perms: perms, CSRF: row.CSRF, Flash: row.Flash}
		next.ServeHTTP(w, r.WithContext(context.WithValue(ctx, sessionKey, s)))
	})
}

// RequireLogin redirects anonymous users to the login page and forces a
// password change when required.
func (m *Auth) RequireLogin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s := FromContext(r.Context())
		if s == nil {
			target := "/login"
			if r.Method == http.MethodGet && r.URL.Path != "/" {
				target += "?next=" + url.QueryEscape(r.URL.RequestURI())
			}
			http.Redirect(w, r, target, http.StatusSeeOther)
			return
		}
		if s.User.MustChangePassword && r.URL.Path != "/account/password" && r.URL.Path != "/logout" {
			http.Redirect(w, r, "/account/password", http.StatusSeeOther)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Require wraps a handler so it only runs when the user holds any of the permissions.
func Require(next http.HandlerFunc, perms ...string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s := FromContext(r.Context())
		for _, p := range perms {
			if s.Can(p) {
				next(w, r)
				return
			}
		}
		Forbidden(w, r)
	}
}

// Forbidden is replaced by the handlers package with a rendered page.
var Forbidden = func(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "You do not have permission to access this page.", http.StatusForbidden)
}

// Login creates a new session for the user and sets the cookie.
func (m *Auth) Login(w http.ResponseWriter, r *http.Request, u models.User) error {
	token := RandomToken(32)
	csrf := RandomToken(32)
	exp := time.Now().UTC().Add(m.TTL).Format(models.TimeLayout)
	if err := repository.CreateSession(r.Context(), m.DB, hashToken(token), u.ID, csrf, ClientIP(r), exp); err != nil {
		return err
	}
	repository.PurgeExpiredSessions(r.Context(), m.DB)
	http.SetCookie(w, m.cookie(SessionCookie, token, int(m.TTL.Seconds())))
	http.SetCookie(w, m.cookie(PreCSRFCookie, "", -1))
	return nil
}

func (m *Auth) Logout(w http.ResponseWriter, r *http.Request) {
	if s := FromContext(r.Context()); s != nil {
		repository.DeleteSession(r.Context(), m.DB, s.TokenHash)
	}
	http.SetCookie(w, m.cookie(SessionCookie, "", -1))
}

// SetFlash stores a one-time message shown on the next page.
func (m *Auth) SetFlash(r *http.Request, kind, msg string) {
	if s := FromContext(r.Context()); s != nil {
		repository.SetFlash(r.Context(), m.DB, s.TokenHash, kind+"|"+msg)
	}
}

// PopFlash returns and clears the pending flash message.
func (m *Auth) PopFlash(r *http.Request) (kind, msg string) {
	s := FromContext(r.Context())
	if s == nil || s.Flash == "" {
		return "", ""
	}
	kind, msg, _ = strings.Cut(s.Flash, "|")
	s.Flash = ""
	repository.SetFlash(r.Context(), m.DB, s.TokenHash, "")
	return kind, msg
}

// PreSessionCSRF returns (and sets if needed) the CSRF token used by the login form.
func (m *Auth) PreSessionCSRF(w http.ResponseWriter, r *http.Request) string {
	if c, err := r.Cookie(PreCSRFCookie); err == nil && len(c.Value) >= 32 {
		return c.Value
	}
	t := RandomToken(32)
	http.SetCookie(w, m.cookie(PreCSRFCookie, t, 3600))
	return t
}

// CSRF rejects state-changing requests without a valid token. Authenticated requests
// use the per-session token; anonymous ones (login) use a double-submit cookie.
func (m *Auth) CSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" && origin != "null" {
			if u, err := url.Parse(origin); err != nil || u.Host != r.Host {
				http.Error(w, "Cross-origin request rejected.", http.StatusForbidden)
				return
			}
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		if err := r.ParseForm(); err != nil {
			http.Error(w, "Invalid form submission.", http.StatusBadRequest)
			return
		}
		sent := r.PostFormValue(CSRFField)
		if sent == "" {
			sent = r.Header.Get("X-CSRF-Token")
		}
		var want string
		if s := FromContext(r.Context()); s != nil {
			want = s.CSRF
		} else if c, err := r.Cookie(PreCSRFCookie); err == nil {
			want = c.Value
		}
		if want == "" || subtle.ConstantTimeCompare([]byte(sent), []byte(want)) != 1 {
			http.Error(w, "Your form has expired or is invalid. Go back, refresh the page and try again.", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ---------- login throttling ----------

type LoginLimiter struct {
	mu      sync.Mutex
	entries map[string]*limitEntry
}

type limitEntry struct {
	fails       int
	first       time.Time
	lockedUntil time.Time
}

func NewLoginLimiter() *LoginLimiter { return &LoginLimiter{entries: map[string]*limitEntry{}} }

const (
	maxFails   = 5
	failWindow = 15 * time.Minute
	lockFor    = 15 * time.Minute
)

func (l *LoginLimiter) key(ip, user string) string { return ip + "|" + strings.ToLower(user) }

// Blocked reports whether this ip/username is temporarily locked out.
func (l *LoginLimiter) Blocked(ip, user string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	for _, k := range []string{l.key(ip, user), l.key(ip, "*")} {
		if e, ok := l.entries[k]; ok && now.Before(e.lockedUntil) {
			return true
		}
	}
	return false
}

func (l *LoginLimiter) Fail(ip, user string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	for k, limit := range map[string]int{l.key(ip, user): maxFails, l.key(ip, "*"): maxFails * 4} {
		e, ok := l.entries[k]
		if !ok || now.Sub(e.first) > failWindow {
			e = &limitEntry{first: now}
			l.entries[k] = e
		}
		e.fails++
		if e.fails >= limit {
			e.lockedUntil = now.Add(lockFor)
		}
	}
	if len(l.entries) > 10000 {
		for k, e := range l.entries {
			if now.Sub(e.first) > failWindow && now.After(e.lockedUntil) {
				delete(l.entries, k)
			}
		}
	}
}

func (l *LoginLimiter) Success(ip, user string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.entries, l.key(ip, user))
}
