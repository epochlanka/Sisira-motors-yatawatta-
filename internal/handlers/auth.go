package handlers

import (
	"net/http"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/bcrypt"

	mw "garagems/internal/middleware"
	"garagems/internal/models"
	"garagems/internal/repository"
)

const BcryptCost = 12

// dummyHash is compared when a username does not exist, to keep timing similar.
var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("not-a-real-password"), BcryptCost)

func HashPassword(pw string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(pw), BcryptCost)
	return string(h), err
}

// ValidatePassword enforces a minimum policy for staff passwords.
func ValidatePassword(pw, username string) string {
	n := utf8.RuneCountInString(pw)
	switch {
	case n < 8:
		return "Password must be at least 8 characters."
	case len(pw) > 72:
		return "Password must be at most 72 bytes."
	case strings.EqualFold(pw, username):
		return "Password must not be the same as the username."
	}
	return ""
}

func safeNext(next string) string {
	if next == "" || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.HasPrefix(next, "/\\") {
		return "/"
	}
	return next
}

func (a *App) loginForm(w http.ResponseWriter, r *http.Request) {
	if mw.FromContext(r.Context()) != nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	p := a.page(r, "Sign in", "")
	p.CSRF = a.Auth.PreSessionCSRF(w, r)
	p.D["next"] = safeNext(r.URL.Query().Get("next"))
	a.render(w, r, "login", p)
}

func (a *App) loginSubmit(w http.ResponseWriter, r *http.Request) {
	username := val(r, "username")
	password := r.PostFormValue("password")
	ip := mw.ClientIP(r)
	fail := func(msg string) {
		p := a.page(r, "Sign in", "")
		p.CSRF = a.Auth.PreSessionCSRF(w, r)
		p.D["next"] = safeNext(r.PostFormValue("next"))
		p.D["username"] = username
		p.FlashKind, p.FlashMsg = "error", msg
		a.render(w, r, "login", p, http.StatusUnauthorized)
	}
	if a.Auth.Limiter.Blocked(ip, username) {
		fail("Too many failed sign-in attempts. Wait 15 minutes and try again.")
		return
	}
	u, err := repository.GetUserByUsername(r.Context(), a.DB, username)
	if err != nil {
		bcrypt.CompareHashAndPassword(dummyHash, []byte(password))
		a.Auth.Limiter.Fail(ip, username)
		fail("Incorrect username or password.")
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)) != nil || !u.Active {
		a.Auth.Limiter.Fail(ip, username)
		repository.Audit(r.Context(), a.DB, models.Actor{IP: ip, Username: username}, "auth.login_failed", "user", u.ID, "")
		fail("Incorrect username or password.")
		return
	}
	a.Auth.Limiter.Success(ip, username)
	if err := a.Auth.Login(w, r, u); err != nil {
		a.serverError(w, r, err)
		return
	}
	repository.Audit(r.Context(), a.DB, models.Actor{ID: u.ID, Username: u.Username, IP: ip}, "auth.login", "user", u.ID, "")
	http.Redirect(w, r, safeNext(r.PostFormValue("next")), http.StatusSeeOther)
}

func (a *App) logout(w http.ResponseWriter, r *http.Request) {
	a.audit(r, "auth.logout", "user", mw.FromContext(r.Context()).User.ID, "")
	a.Auth.Logout(w, r)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (a *App) passwordForm(w http.ResponseWriter, r *http.Request) {
	p := a.page(r, "Change password", "")
	a.render(w, r, "password", p)
}

func (a *App) passwordSubmit(w http.ResponseWriter, r *http.Request) {
	s := mw.FromContext(r.Context())
	v := V{}
	current := r.PostFormValue("current_password")
	pw := r.PostFormValue("new_password")
	if bcrypt.CompareHashAndPassword([]byte(s.User.PasswordHash), []byte(current)) != nil {
		v["current_password"] = "Current password is incorrect."
	}
	if msg := ValidatePassword(pw, s.User.Username); msg != "" {
		v["new_password"] = msg
	} else if pw != r.PostFormValue("confirm_password") {
		v["confirm_password"] = "Passwords do not match."
	} else if pw == current {
		v["new_password"] = "Choose a password different from the current one."
	}
	if len(v) > 0 {
		p := a.page(r, "Change password", "")
		p.Errors = v
		a.render(w, r, "password", p, http.StatusUnprocessableEntity)
		return
	}
	hash, err := HashPassword(pw)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	if err := repository.SetPassword(r.Context(), a.DB, s.User.ID, hash, false); err != nil {
		a.serverError(w, r, err)
		return
	}
	repository.DeleteUserSessions(r.Context(), a.DB, s.User.ID, s.TokenHash)
	a.audit(r, "user.password_change", "user", s.User.ID, "own password changed")
	a.redirectFlash(w, r, "/", "success", "Your password has been changed.")
}

func (a *App) dashboard(w http.ResponseWriter, r *http.Request) {
	s := mw.FromContext(r.Context())
	if !s.Can("dashboard.view") {
		// Users without the dashboard go to the first section they can use.
		for _, x := range []struct{ perm, url string }{
			{"jobs.view_all", "/jobs"}, {"jobs.view_assigned", "/jobs"}, {"customers.view", "/customers"},
			{"appointments.view", "/appointments"}, {"invoices.view", "/invoices"}, {"parts.view", "/parts"}, {"purchases.view", "/purchases"},
			{"expenses.view", "/expenses"}, {"timesheet.self", "/staff/timesheets"},
		} {
			if s.Can(x.perm) {
				http.Redirect(w, r, x.url, http.StatusSeeOther)
				return
			}
		}
		a.forbidden(w, r)
		return
	}
	ctx := r.Context()
	var only int64
	if !s.Can("jobs.view_all") {
		only = s.User.ID
	}
	p := a.page(r, "Dashboard", "dashboard")
	stats, err := repository.Dashboard(ctx, a.DB, models.Today(), only)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	p.D["stats"] = stats
	if s.Can("jobs.view_all") || s.Can("jobs.view_assigned") {
		jobs, _, err := repository.ListJobs(ctx, a.DB, repository.JobFilter{Status: "active", OnlyUserID: only},
			repository.ListOpts{OrderBy: "j.job_date DESC, j.id DESC", Limit: 10})
		if err != nil {
			a.serverError(w, r, err)
			return
		}
		p.D["jobs"] = jobs
	}
	if s.Can("invoices.view") {
		invs, _, err := repository.ListInvoices(ctx, a.DB, repository.InvoiceFilter{PaymentStatus: "outstanding"},
			repository.ListOpts{OrderBy: "i.invoice_date, i.id", Limit: 8})
		if err != nil {
			a.serverError(w, r, err)
			return
		}
		p.D["unpaid"] = invs
	}
	if s.Can("parts.view") {
		low, _, err := repository.ListParts(ctx, a.DB, repository.PartFilter{LowOnly: true}, repository.ListOpts{OrderBy: "qty_on_hand - reorder_level, code", Limit: 8})
		if err != nil {
			a.serverError(w, r, err)
			return
		}
		p.D["low"] = low
	}
	if s.Can("appointments.view") {
		appts, _, err := repository.ListAppointments(ctx, a.DB, repository.ApptFilter{From: models.Today(), To: models.Today()},
			repository.ListOpts{OrderBy: "a.start_time, a.id"})
		if err != nil {
			a.serverError(w, r, err)
			return
		}
		p.D["appts"] = appts
	}
	if s.Can("timesheet.self") {
		p.D["canClock"] = true
		if t, err := repository.TimesheetFor(ctx, a.DB, s.User.ID, models.Today()); err == nil {
			p.D["mine"] = t
		}
	}
	if s.Can("audit.view") {
		act, _, err := repository.ListAudit(ctx, a.DB, repository.AuditFilter{}, repository.ListOpts{OrderBy: "id DESC", Limit: 10})
		if err != nil {
			a.serverError(w, r, err)
			return
		}
		p.D["activity"] = act
	}
	a.render(w, r, "dashboard", p)
}
