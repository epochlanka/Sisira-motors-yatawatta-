package handlers

import (
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	mw "garagems/internal/middleware"
	"garagems/internal/models"
	"garagems/internal/repository"
	"garagems/internal/service"
)

var DateFormats = []struct{ Layout, Example string }{
	{"02 Jan 2006", "10 Oct 2026"},
	{"2006-01-02", "2026-10-10"},
	{"02/01/2006", "10/10/2026 (day/month/year)"},
	{"01/02/2006", "10/10/2026 (month/day/year)"},
	{"Jan 02, 2006", "Oct 10, 2026"},
}

func validDateFormat(s string) bool {
	for _, f := range DateFormats {
		if f.Layout == s {
			return true
		}
	}
	return false
}

var currencyCodeRe = regexp.MustCompile(`^[A-Z]{3}$`)

func (a *App) settingsForm(w http.ResponseWriter, r *http.Request) {
	p := a.page(r, "Business settings", "settings")
	p.D["s"], p.D["formats"] = a.Settings(), DateFormats
	a.render(w, r, "settings_business", p)
}

func parsePercentBP(s string) (int64, error) {
	s = strings.TrimSuffix(strings.TrimSpace(s), "%")
	if s == "" {
		return 0, nil
	}
	q, err := models.ParseQty(s) // thousandths of a percent
	if err != nil || q < 0 || q > 100*models.QtyScale || q%10 != 0 {
		return 0, fmt.Errorf("enter a percentage between 0 and 100 with up to 2 decimals")
	}
	return int64(q) / 10, nil
}

func (a *App) settingsSave(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	old := a.Settings()
	v := V{}
	s := old
	s.BusinessName, s.Address, s.Phone, s.Email = val(r, "business_name"), val(r, "address"), val(r, "phone"), val(r, "email")
	s.RegistrationNo, s.TaxNumber = val(r, "registration_no"), val(r, "tax_number")
	s.CurrencyCode, s.CurrencySymbol = strings.ToUpper(val(r, "currency_code")), val(r, "currency_symbol")
	s.SymbolAfter, s.TaxEnabled, s.DiscountEnabled = checked(r, "symbol_after"), checked(r, "tax_enabled"), checked(r, "discount_enabled")
	s.TaxName = val(r, "tax_name")
	s.InvoicePrefix, s.ReceiptPrefix, s.JobPrefix, s.PurchasePrefix = val(r, "invoice_prefix"), val(r, "receipt_prefix"), val(r, "job_prefix"), val(r, "purchase_prefix")
	s.InvoiceFooter, s.DateFormat = val(r, "invoice_footer"), val(r, "date_format")

	v.Required("business_name", s.BusinessName, "Business name")
	v.MaxLen("business_name", s.BusinessName, 120, "Business name")
	v.MaxLen("address", s.Address, 300, "Address")
	v.MaxLen("phone", s.Phone, 80, "Phone")
	v.Email("email", s.Email)
	if !currencyCodeRe.MatchString(s.CurrencyCode) {
		v["currency_code"] = "Enter a 3-letter ISO currency code (e.g. LKR, USD)."
	}
	v.MaxLen("currency_symbol", s.CurrencySymbol, 8, "Currency symbol")
	dec, err := strconv.Atoi(val(r, "currency_decimals"))
	if err != nil || dec < 0 || dec > 3 {
		v["currency_decimals"] = "Decimal places must be 0 to 3."
	} else if dec != old.CurrencyDecimals {
		var n int
		a.DB.QueryRowContext(ctx, "SELECT (SELECT COUNT(*) FROM parts) + (SELECT COUNT(*) FROM job_charges) + (SELECT COUNT(*) FROM invoices) + (SELECT COUNT(*) FROM charge_categories WHERE default_price > 0)").Scan(&n)
		if n > 0 {
			v["currency_decimals"] = "Decimal places cannot change after prices have been recorded (stored amounts would change meaning)."
		} else {
			s.CurrencyDecimals = dec
		}
	}
	if bp, err := parsePercentBP(val(r, "tax_rate")); err != nil {
		v["tax_rate"] = "Tax rate: " + err.Error() + "."
	} else {
		s.TaxRateBP = bp
	}
	if s.TaxEnabled {
		v.Required("tax_name", s.TaxName, "Tax name")
	}
	v.MaxLen("tax_name", s.TaxName, 20, "Tax name")
	for f, x := range map[string]string{"invoice_prefix": s.InvoicePrefix, "receipt_prefix": s.ReceiptPrefix, "job_prefix": s.JobPrefix, "purchase_prefix": s.PurchasePrefix} {
		v.MaxLen(f, x, 12, "Prefix")
	}
	pad, err := strconv.Atoi(val(r, "invoice_pad"))
	if err != nil || pad < 1 || pad > 12 {
		v["invoice_pad"] = "Number of digits must be 1 to 12."
	} else {
		s.InvoicePad = pad
	}
	next, err := strconv.ParseInt(val(r, "invoice_next"), 10, 64)
	if err != nil || next < 1 {
		v["invoice_next"] = "Next invoice number must be a positive whole number."
	} else {
		s.InvoiceNext = next
		if next != old.InvoiceNext {
			var used int
			a.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM invoices WHERE invoice_number = ?", repository.FormatNumber(s.InvoicePrefix, next, s.InvoicePad)).Scan(&used)
			if used > 0 {
				v["invoice_next"] = "That invoice number has already been used."
			}
		}
	}
	v.MaxLen("invoice_footer", s.InvoiceFooter, 500, "Footer message")
	if !validDateFormat(s.DateFormat) {
		v["date_format"] = "Choose a date format."
	}
	if len(v) > 0 {
		p := a.page(r, "Business settings", "settings")
		p.D["s"], p.D["formats"], p.Errors = s, DateFormats, v
		p.FlashKind, p.FlashMsg = "error", "Please correct the highlighted fields."
		a.render(w, r, "settings_business", p, http.StatusUnprocessableEntity)
		return
	}
	b := func(x bool) string {
		if x {
			return "1"
		}
		return "0"
	}
	err = a.Svc.Tx(ctx, func(tx *sql.Tx) error {
		if err := repository.SaveSettings(ctx, tx, map[string]string{
			"business_name": s.BusinessName, "address": s.Address, "phone": s.Phone, "email": s.Email,
			"registration_no": s.RegistrationNo, "tax_number": s.TaxNumber,
			"currency_code": s.CurrencyCode, "currency_symbol": s.CurrencySymbol, "currency_decimals": strconv.Itoa(s.CurrencyDecimals),
			"symbol_after": b(s.SymbolAfter), "tax_enabled": b(s.TaxEnabled), "tax_name": s.TaxName, "tax_rate_bp": strconv.FormatInt(s.TaxRateBP, 10),
			"discount_enabled": b(s.DiscountEnabled), "invoice_prefix": s.InvoicePrefix, "invoice_pad": strconv.Itoa(s.InvoicePad),
			"receipt_prefix": s.ReceiptPrefix, "job_prefix": s.JobPrefix, "purchase_prefix": s.PurchasePrefix,
			"invoice_footer": s.InvoiceFooter, "date_format": s.DateFormat,
		}); err != nil {
			return err
		}
		if s.InvoiceNext != old.InvoiceNext {
			if err := repository.SetSequence(ctx, tx, "invoice", s.InvoiceNext); err != nil {
				return err
			}
		}
		detail := fmt.Sprintf("tax %v %s; currency %s; invoice prefix %q next %d", s.TaxEnabled, s.TaxRateLabel(), s.CurrencyCode, s.InvoicePrefix, s.InvoiceNext)
		return repository.Audit(ctx, tx, mw.ActorFrom(r), "settings.update", "settings", 0, detail)
	})
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	if err := a.ReloadSettings(ctx); err != nil {
		a.serverError(w, r, err)
		return
	}
	a.redirectFlash(w, r, "/settings", "success", "Settings saved.")
}

// ---------- charge categories ----------

func (a *App) chargesList(w http.ResponseWriter, r *http.Request) {
	cats, err := repository.ListChargeCategories(r.Context(), a.DB, false)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	p := a.page(r, "Charge categories", "settings")
	p.D["cats"] = cats
	a.render(w, r, "settings_charges", p)
}

func chargeFromForm(r *http.Request) (models.ChargeCategory, V) {
	v := V{}
	c := models.ChargeCategory{Name: val(r, "name"), Unit: val(r, "unit"), ManualPrice: checked(r, "manual_price"), IsCustom: checked(r, "is_custom"), Active: true}
	v.Required("name", c.Name, "Name")
	v.MaxLen("name", c.Name, 80, "Name")
	if c.Unit == "" {
		c.Unit = "job"
	}
	v.MaxLen("unit", c.Unit, 20, "Unit")
	c.DefaultPrice = v.Money(r, "default_price", "Default price", false)
	return c, v
}

func (a *App) chargeCreate(w http.ResponseWriter, r *http.Request) {
	c, v := chargeFromForm(r)
	if len(v) > 0 {
		a.redirectFlash(w, r, "/settings/charges", "error", firstErr(v))
		return
	}
	id, err := repository.CreateChargeCategory(r.Context(), a.DB, c)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			a.redirectFlash(w, r, "/settings/charges", "error", "A category with that name already exists.")
			return
		}
		a.serverError(w, r, err)
		return
	}
	a.audit(r, "charge_category.create", "charge_category", id, fmt.Sprintf("%s default %s", c.Name, c.DefaultPrice))
	a.redirectFlash(w, r, "/settings/charges", "success", "Category added.")
}

func (a *App) chargeUpdate(w http.ResponseWriter, r *http.Request) {
	old, err := repository.GetChargeCategory(r.Context(), a.DB, pathID(r, "id"))
	if err != nil {
		a.handleErr(w, r, err)
		return
	}
	c, v := chargeFromForm(r)
	c.ID, c.Active = old.ID, checked(r, "active")
	if len(v) > 0 {
		a.redirectFlash(w, r, "/settings/charges", "error", firstErr(v))
		return
	}
	if err := repository.UpdateChargeCategory(r.Context(), a.DB, c); err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			a.redirectFlash(w, r, "/settings/charges", "error", "A category with that name already exists.")
			return
		}
		a.serverError(w, r, err)
		return
	}
	a.audit(r, "charge_category.update", "charge_category", c.ID,
		fmt.Sprintf("%s: default %s -> %s, active %v", c.Name, old.DefaultPrice, c.DefaultPrice, c.Active))
	a.redirectFlash(w, r, "/settings/charges", "success", "Category saved. Existing job lines keep their recorded prices.")
}

func (a *App) chargeMove(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := pathID(r, "id")
	up := r.PostFormValue("dir") == "up"
	err := a.Svc.Tx(ctx, func(tx *sql.Tx) error {
		cats, err := repository.ListChargeCategories(ctx, tx, false)
		if err != nil {
			return err
		}
		idx := -1
		for i, c := range cats {
			if c.ID == id {
				idx = i
			}
		}
		if idx < 0 {
			return repository.ErrNotFound
		}
		j := idx + 1
		if up {
			j = idx - 1
		}
		if j < 0 || j >= len(cats) {
			return nil
		}
		cats[idx], cats[j] = cats[j], cats[idx]
		for i, c := range cats {
			if err := repository.SetChargeCategoryOrder(ctx, tx, c.ID, (i+1)*10); err != nil {
				return err
			}
		}
		return nil
	})
	a.done(w, r, "/settings/charges", err, "")
}

// ---------- users & roles ----------

func (a *App) usersList(w http.ResponseWriter, r *http.Request) {
	users, err := repository.ListUsers(r.Context(), a.DB, false)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	p := a.page(r, "Users", "settings")
	p.D["users"] = users
	a.render(w, r, "settings_users", p)
}

var usernameRe = regexp.MustCompile(`^[a-zA-Z0-9._-]{3,40}$`)

func (a *App) userFormPage(r *http.Request, title string, u models.User) (*Page, error) {
	roles, err := repository.ListRoles(r.Context(), a.DB)
	if err != nil {
		return nil, err
	}
	p := a.page(r, title, "settings")
	p.D["u"], p.D["roles"] = u, roles
	return p, nil
}

func (a *App) userNew(w http.ResponseWriter, r *http.Request) {
	p, err := a.userFormPage(r, "New user", models.User{Active: true, MustChangePassword: true})
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	a.render(w, r, "user_form", p)
}

func (a *App) userCreate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	v := V{}
	u := models.User{Username: val(r, "username"), FullName: val(r, "full_name"), RoleID: formID(r, "role_id"), Active: true, MustChangePassword: checked(r, "must_change_password")}
	if !usernameRe.MatchString(u.Username) {
		v["username"] = "Username must be 3-40 characters: letters, digits, dot, dash or underscore."
	} else if _, err := repository.GetUserByUsername(ctx, a.DB, u.Username); err == nil {
		v["username"] = "That username is already taken."
	}
	v.Required("full_name", u.FullName, "Full name")
	v.MaxLen("full_name", u.FullName, 100, "Full name")
	if _, err := repository.GetRole(ctx, a.DB, u.RoleID); err != nil {
		v["role_id"] = "Select a role."
	}
	pw := r.PostFormValue("password")
	if msg := ValidatePassword(pw, u.Username); msg != "" {
		v["password"] = msg
	}
	if len(v) > 0 {
		p, err := a.userFormPage(r, "New user", u)
		if err != nil {
			a.serverError(w, r, err)
			return
		}
		p.Errors = v
		a.render(w, r, "user_form", p, http.StatusUnprocessableEntity)
		return
	}
	hash, err := HashPassword(pw)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	u.PasswordHash = hash
	id, err := repository.CreateUser(ctx, a.DB, u)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	role, _ := repository.GetRole(ctx, a.DB, u.RoleID)
	a.audit(r, "user.create", "user", id, u.Username+" role "+role.Name)
	a.redirectFlash(w, r, "/settings/users", "success", "User "+u.Username+" created.")
}

func (a *App) userEdit(w http.ResponseWriter, r *http.Request) {
	u, err := repository.GetUser(r.Context(), a.DB, pathID(r, "id"))
	if err != nil {
		a.handleErr(w, r, err)
		return
	}
	p, err := a.userFormPage(r, "Edit user", u)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	a.render(w, r, "user_form", p)
}

func (a *App) userUpdate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	me := mw.FromContext(ctx).User
	old, err := repository.GetUser(ctx, a.DB, pathID(r, "id"))
	if err != nil {
		a.handleErr(w, r, err)
		return
	}
	u := old
	u.FullName, u.RoleID, u.Active = val(r, "full_name"), formID(r, "role_id"), checked(r, "active")
	v := V{}
	v.Required("full_name", u.FullName, "Full name")
	v.MaxLen("full_name", u.FullName, 100, "Full name")
	role, err := repository.GetRole(ctx, a.DB, u.RoleID)
	if err != nil {
		v["role_id"] = "Select a role."
	}
	if u.ID == me.ID && !u.Active {
		v["active"] = "You cannot deactivate your own account."
	}
	if len(v) > 0 {
		p, err := a.userFormPage(r, "Edit user", u)
		if err != nil {
			a.serverError(w, r, err)
			return
		}
		p.Errors = v
		a.render(w, r, "user_form", p, http.StatusUnprocessableEntity)
		return
	}
	err = a.Svc.Tx(ctx, func(tx *sql.Tx) error {
		if err := repository.UpdateUser(ctx, tx, u); err != nil {
			return err
		}
		if n, err := repository.CountActiveAdmins(ctx, tx, 0); err != nil {
			return err
		} else if n == 0 {
			return service.Fail("At least one active user must keep the 'users.manage' permission.")
		}
		if !u.Active {
			if err := repository.DeleteUserSessions(ctx, tx, u.ID, ""); err != nil {
				return err
			}
		}
		return repository.Audit(ctx, tx, mw.ActorFrom(r), "user.update", "user", u.ID,
			fmt.Sprintf("%s: role %s -> %s, active %v -> %v", u.Username, old.RoleName, role.Name, old.Active, u.Active))
	})
	a.done(w, r, "/settings/users", err, "User saved.")
}

func (a *App) userResetPassword(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u, err := repository.GetUser(ctx, a.DB, pathID(r, "id"))
	if err != nil {
		a.handleErr(w, r, err)
		return
	}
	pw := r.PostFormValue("password")
	to := "/settings/users/" + strconv.FormatInt(u.ID, 10)
	if msg := ValidatePassword(pw, u.Username); msg != "" {
		a.redirectFlash(w, r, to, "error", msg)
		return
	}
	hash, err := HashPassword(pw)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	if err := repository.SetPassword(ctx, a.DB, u.ID, hash, true); err != nil {
		a.serverError(w, r, err)
		return
	}
	repository.DeleteUserSessions(ctx, a.DB, u.ID, mw.FromContext(ctx).TokenHash)
	a.audit(r, "user.password_reset", "user", u.ID, u.Username)
	a.redirectFlash(w, r, to, "success", "Password reset. The user must change it at next sign-in.")
}

func (a *App) rolesForm(w http.ResponseWriter, r *http.Request) {
	roles, err := repository.ListRoles(r.Context(), a.DB)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	p := a.page(r, "Roles & permissions", "settings")
	p.D["roles"], p.D["perms"] = roles, models.AllPermissions
	a.render(w, r, "settings_roles", p)
}

func (a *App) rolesSave(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	err := a.Svc.Tx(ctx, func(tx *sql.Tx) error {
		roles, err := repository.ListRoles(ctx, tx)
		if err != nil {
			return err
		}
		for _, role := range roles {
			if role.IsSystem {
				continue // the Administrator role always has every permission
			}
			var perms []string
			for _, code := range r.PostForm["perm_"+strconv.FormatInt(role.ID, 10)] {
				if models.ValidPermission(code) {
					perms = append(perms, code)
				}
			}
			if err := repository.SetRolePermissions(ctx, tx, role.ID, perms); err != nil {
				return err
			}
			added, removed := diffPerms(role.Permissions, perms)
			if len(added)+len(removed) > 0 {
				if err := repository.Audit(ctx, tx, mw.ActorFrom(r), "role.permissions", "role", role.ID,
					fmt.Sprintf("%s: +[%s] -[%s]", role.Name, strings.Join(added, " "), strings.Join(removed, " "))); err != nil {
					return err
				}
			}
		}
		if n, err := repository.CountActiveAdmins(ctx, tx, 0); err != nil {
			return err
		} else if n == 0 {
			return service.Fail("At least one active user must keep the 'users.manage' permission.")
		}
		return nil
	})
	a.done(w, r, "/settings/roles", err, "Permissions saved. They apply on each user's next request.")
}

func diffPerms(old map[string]bool, now []string) (added, removed []string) {
	nm := map[string]bool{}
	for _, p := range now {
		nm[p] = true
		if !old[p] {
			added = append(added, p)
		}
	}
	for p := range old {
		if !nm[p] {
			removed = append(removed, p)
		}
	}
	return
}

func (a *App) roleCreate(w http.ResponseWriter, r *http.Request) {
	name, desc := val(r, "name"), val(r, "description")
	if name == "" || len(name) > 50 {
		a.redirectFlash(w, r, "/settings/roles", "error", "Role name is required (max 50 characters).")
		return
	}
	id, err := repository.CreateRole(r.Context(), a.DB, name, desc)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			a.redirectFlash(w, r, "/settings/roles", "error", "A role with that name already exists.")
			return
		}
		a.serverError(w, r, err)
		return
	}
	a.audit(r, "role.create", "role", id, name)
	a.redirectFlash(w, r, "/settings/roles", "success", "Role created. Tick its permissions below and save.")
}

func (a *App) auditList(w http.ResponseWriter, r *http.Request) {
	l := newList(r, map[string]string{"date": "id"}, "date", "desc")
	l.PerPage = 50
	from, to := dateParam(r, "from", ""), dateParam(r, "to", "")
	uf, ut := utcBounds(from, to)
	list, total, err := repository.ListAudit(r.Context(), a.DB, repository.AuditFilter{Q: l.Get("q"), EntityType: l.Get("entity"), From: uf, To: ut}, l.Opts())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	l.Total = total
	p := a.page(r, "Audit log", "settings")
	p.D["list"], p.D["entries"] = l, list
	p.D["entities"] = []string{"customer", "vehicle", "job", "invoice", "part", "supplier", "purchase", "user", "role", "settings", "charge_category"}
	a.render(w, r, "settings_audit", p)
}

// backupDownload streams a consistent snapshot of the database made with VACUUM INTO.
func (a *App) backupDownload(w http.ResponseWriter, r *http.Request) {
	dir, err := os.MkdirTemp("", "garage-backup-")
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "backup.db")
	if _, err := a.DB.ExecContext(r.Context(), "VACUUM INTO ?", path); err != nil {
		a.serverError(w, r, err)
		return
	}
	f, err := os.Open(path)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	defer f.Close()
	a.audit(r, "backup.download", "settings", 0, "database backup downloaded")
	name := "garage-backup-" + time.Now().In(models.Location()).Format("20060102-1504") + ".db"
	w.Header().Set("Content-Type", "application/vnd.sqlite3")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	io.Copy(w, f)
}
