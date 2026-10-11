// Package handlers contains HTTP handlers, routing and template rendering.
package handlers

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"path"
	"strings"
	"sync"
	"time"

	"garagems/internal/config"
	"garagems/internal/middleware"
	"garagems/internal/models"
	"garagems/internal/repository"
	"garagems/internal/service"
	"garagems/web"
)

type App struct {
	DB   *sql.DB
	Cfg  config.Config
	Svc  *service.Service
	Auth *middleware.Auth

	tmpl map[string]*template.Template

	mu  sync.RWMutex
	biz models.Settings
}

func New(db *sql.DB, cfg config.Config) (*App, error) {
	a := &App{
		DB:  db,
		Cfg: cfg,
		Svc: service.New(db),
		Auth: &middleware.Auth{DB: db, TTL: time.Duration(cfg.SessionHours) * time.Hour,
			Secure: cfg.CookieSecure, Limiter: middleware.NewLoginLimiter()},
	}
	if err := a.ReloadSettings(context.Background()); err != nil {
		return nil, err
	}
	if err := a.loadTemplates(); err != nil {
		return nil, err
	}
	middleware.Forbidden = a.forbidden
	return a, nil
}

// ReloadSettings refreshes the cached business settings and currency format.
func (a *App) ReloadSettings(ctx context.Context) error {
	s, err := repository.GetSettings(ctx, a.DB)
	if err != nil {
		return err
	}
	models.SetCurrency(models.CurrencyFormat{Code: s.CurrencyCode, Symbol: s.CurrencySymbol, Decimals: s.CurrencyDecimals, SymbolAfter: s.SymbolAfter})
	a.mu.Lock()
	a.biz = s
	a.mu.Unlock()
	return nil
}

func (a *App) Settings() models.Settings {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.biz
}

// ---------- templates ----------

func (a *App) funcs() template.FuncMap {
	return template.FuncMap{
		"money":   func(m models.Money) string { return m.String() },
		"moneyIn": func(m models.Money) string { return m.Input() },
		"qty":     func(q models.Qty) string { return q.String() },
		"date": func(s string) string {
			t, err := time.Parse(models.DateLayout, s)
			if err != nil {
				return s
			}
			return t.Format(a.Settings().DateFormat)
		},
		"datetime": func(s string) string {
			t, err := time.Parse(models.TimeLayout, s)
			if err != nil {
				return s
			}
			return t.In(models.Location()).Format(a.Settings().DateFormat + " 15:04")
		},
		"nullInt": func(n sql.NullInt64) string {
			if !n.Valid {
				return ""
			}
			return fmt.Sprint(n.Int64)
		},
		"dict": func(kv ...any) map[string]any {
			m := map[string]any{}
			for i := 0; i+1 < len(kv); i += 2 {
				m[fmt.Sprint(kv[i])] = kv[i+1]
			}
			return m
		},
		"statusClass": func(s string) string {
			switch s {
			case models.JobOpen, models.PurchaseDraft:
				return "badge-info"
			case models.JobInProgress:
				return "badge-primary"
			case models.JobWaiting, models.PayPartial:
				return "badge-warn"
			case models.JobCompleted, models.PayPaid, models.PurchaseReceived, "Valid", "Active":
				return "badge-ok"
			case models.JobCancelled, models.InvoiceVoid, "Inactive", "Archived":
				return "badge-muted"
			case models.PayUnpaid, "Low":
				return "badge-danger"
			}
			return "badge-muted"
		},
		"movementLabel": models.MovementLabel,
		"add":           func(a, b int) int { return a + b },
		"lower":         strings.ToLower,
		"neg":           func(m models.Money) bool { return m < 0 },
		"pos":           func(m models.Money) bool { return m > 0 },
		"qpos":          func(q models.Qty) bool { return q > 0 },
		"daysSince": func(s string) int {
			t, err := time.ParseInLocation(models.DateLayout, s, models.Location())
			if err != nil {
				return 0
			}
			return int(time.Since(t).Hours() / 24)
		},
	}
}

func (a *App) loadTemplates() error {
	a.tmpl = map[string]*template.Template{}
	files, err := fs.Glob(web.FS, "templates/*.html")
	if err != nil {
		return err
	}
	for _, f := range files {
		name := strings.TrimSuffix(path.Base(f), ".html")
		if name == "layout" || name == "partials" {
			continue
		}
		t, err := template.New(name).Funcs(a.funcs()).ParseFS(web.FS, "templates/layout.html", "templates/partials.html", f)
		if err != nil {
			return fmt.Errorf("template %s: %w", f, err)
		}
		a.tmpl[name] = t
	}
	return nil
}

// Page is the data passed to every template.
type Page struct {
	Title     string
	Active    string
	Session   *middleware.Session
	CSRF      string
	FlashKind string
	FlashMsg  string
	Biz       models.Settings
	Today     string
	D         map[string]any
	Errors    map[string]string
}

func (p *Page) Can(perm string) bool { return p.Session.Can(perm) }

func (p *Page) CanAny(perms ...string) bool {
	for _, x := range perms {
		if p.Session.Can(x) {
			return true
		}
	}
	return false
}

func (p *Page) User() models.User {
	if p.Session == nil {
		return models.User{}
	}
	return p.Session.User
}

func (a *App) page(r *http.Request, title, active string) *Page {
	s := middleware.FromContext(r.Context())
	p := &Page{Title: title, Active: active, Session: s, Biz: a.Settings(), Today: models.Today(), D: map[string]any{}, Errors: map[string]string{}}
	if s != nil {
		p.CSRF = s.CSRF
	}
	return p
}

func (a *App) render(w http.ResponseWriter, r *http.Request, name string, p *Page, status ...int) {
	t, ok := a.tmpl[name]
	if !ok {
		a.serverError(w, r, fmt.Errorf("template %q not found", name))
		return
	}
	if p.FlashMsg == "" {
		p.FlashKind, p.FlashMsg = a.Auth.PopFlash(r)
	}
	root := "layout"
	if t.Lookup("standalone") != nil {
		root = "standalone"
	}
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, root, p); err != nil {
		log.Printf("render %s: %v", name, err)
		http.Error(w, "Template error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if len(status) > 0 {
		w.WriteHeader(status[0])
	}
	buf.WriteTo(w)
}

// ---------- responses ----------

func (a *App) serverError(w http.ResponseWriter, r *http.Request, err error) {
	log.Printf("ERROR %s %s: %v", r.Method, r.URL.Path, err)
	p := a.page(r, "Error", "")
	p.D["code"] = 500
	p.D["message"] = "Something went wrong. The error has been logged; please try again."
	a.renderError(w, r, p, http.StatusInternalServerError)
}

func (a *App) notFound(w http.ResponseWriter, r *http.Request) {
	p := a.page(r, "Not found", "")
	p.D["code"] = 404
	p.D["message"] = "The page or record you requested does not exist."
	a.renderError(w, r, p, http.StatusNotFound)
}

func (a *App) forbidden(w http.ResponseWriter, r *http.Request) {
	p := a.page(r, "Access denied", "")
	p.D["code"] = 403
	p.D["message"] = "You do not have permission to access this page or perform this action."
	a.renderError(w, r, p, http.StatusForbidden)
}

func (a *App) renderError(w http.ResponseWriter, r *http.Request, p *Page, code int) {
	if t, ok := a.tmpl["error"]; ok {
		var buf bytes.Buffer
		if err := t.ExecuteTemplate(&buf, "layout", p); err == nil {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(code)
			buf.WriteTo(w)
			return
		}
	}
	http.Error(w, fmt.Sprint(p.D["message"]), code)
}

// handleErr renders not-found / user errors / server errors appropriately for GET pages.
func (a *App) handleErr(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, repository.ErrNotFound) {
		a.notFound(w, r)
		return
	}
	if msg, ok := service.IsUserError(err); ok {
		p := a.page(r, "Not allowed", "")
		p.D["code"] = 400
		p.D["message"] = msg
		a.renderError(w, r, p, http.StatusBadRequest)
		return
	}
	a.serverError(w, r, err)
}

// done finishes a POST action: flash a success or user-facing error and redirect.
func (a *App) done(w http.ResponseWriter, r *http.Request, to string, err error, success string) {
	if err != nil {
		if msg, ok := service.IsUserError(err); ok {
			a.Auth.SetFlash(r, "error", msg)
			http.Redirect(w, r, to, http.StatusSeeOther)
			return
		}
		a.serverError(w, r, err)
		return
	}
	if success != "" {
		a.Auth.SetFlash(r, "success", success)
	}
	http.Redirect(w, r, to, http.StatusSeeOther)
}

func (a *App) redirectFlash(w http.ResponseWriter, r *http.Request, to, kind, msg string) {
	a.Auth.SetFlash(r, kind, msg)
	http.Redirect(w, r, to, http.StatusSeeOther)
}

func (a *App) audit(r *http.Request, action, entity string, id int64, details string) {
	if err := repository.Audit(r.Context(), a.DB, middleware.ActorFrom(r), action, entity, id, details); err != nil {
		log.Printf("audit: %v", err)
	}
}
