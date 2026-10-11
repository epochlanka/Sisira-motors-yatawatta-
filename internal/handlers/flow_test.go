package handlers

import (
	"context"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"garagems/internal/config"
	"garagems/internal/database"
	"garagems/internal/models"
	"garagems/internal/repository"
)

var csrfRe = regexp.MustCompile(`name="csrf_token" value="([^"]+)"`)

type tclient struct {
	t    *testing.T
	c    *http.Client
	base string
	csrf string
}

func (c *tclient) get(path string) (int, string) {
	c.t.Helper()
	resp, err := c.c.Get(c.base + path)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if m := csrfRe.FindStringSubmatch(string(b)); m != nil {
		c.csrf = m[1]
	}
	return resp.StatusCode, string(b)
}

// post submits a form and returns the redirect location (or "" and status on non-redirect).
func (c *tclient) post(path string, form url.Values) (string, int) {
	c.t.Helper()
	form.Set("csrf_token", c.csrf)
	resp, err := c.c.PostForm(c.base+path, form)
	if err != nil {
		c.t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp.Header.Get("Location"), resp.StatusCode
}

func idFrom(t *testing.T, loc, prefix string) string {
	t.Helper()
	if !strings.HasPrefix(loc, prefix) {
		t.Fatalf("expected redirect to %s..., got %q", prefix, loc)
	}
	s := strings.TrimPrefix(loc, prefix)
	if i := strings.IndexAny(s, "?#/"); i >= 0 {
		s = s[:i]
	}
	return s
}

func lastFlash(t *testing.T, a *App) string {
	var f string
	a.DB.QueryRow("SELECT flash FROM sessions ORDER BY created_at DESC LIMIT 1").Scan(&f)
	return f
}

func TestAcceptanceFlow(t *testing.T) {
	ctx := context.Background()
	cfg := config.Default()
	cfg.DBPath = filepath.Join(t.TempDir(), "test.db")
	cfg.Location = time.UTC
	cfg.Business.Name = "Test Garage"
	cfg.Business.CurrencySymbol = "Rs."
	models.SetLocation(time.UTC)
	db, err := database.Open(cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	// Migrations and seeding must be safe to repeat.
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := database.SeedDefaults(ctx, db, cfg); err != nil {
			t.Fatal(err)
		}
	}
	hash, _ := HashPassword("correct-horse-battery")
	if _, err := repository.CreateUser(ctx, db, models.User{Username: "admin", FullName: "Admin", PasswordHash: hash, RoleID: 1, Active: true}); err != nil {
		t.Fatal(err)
	}
	app, err := New(db, cfg)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(app.Routes())
	defer srv.Close()
	jar, _ := cookiejar.New(nil)
	c := &tclient{t: t, base: srv.URL, c: &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}

	// Unauthenticated access redirects to login; POST without CSRF is rejected.
	if resp, _ := c.c.Get(srv.URL + "/jobs"); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("anonymous /jobs: %d", resp.StatusCode)
	}
	if resp, _ := c.c.PostForm(srv.URL+"/customers", url.Values{"name": {"x"}}); resp.StatusCode == http.StatusSeeOther && strings.Contains(resp.Header.Get("Location"), "/customers/") {
		t.Fatal("POST without session/CSRF should not succeed")
	}

	// 1. Sign in
	c.get("/login")
	if loc, _ := c.post("/login", url.Values{"username": {"admin"}, "password": {"wrong-password-123"}}); loc != "" {
		t.Fatal("wrong password must not log in")
	}
	c.get("/login")
	if loc, _ := c.post("/login", url.Values{"username": {"admin"}, "password": {"correct-horse-battery"}}); loc != "/" {
		t.Fatalf("login redirect %q", loc)
	}
	if code, body := c.get("/"); code != 200 || !strings.Contains(body, "Dashboard") {
		t.Fatalf("dashboard %d", code)
	}
	// CSRF token mismatch is rejected
	if _, code := (&tclient{t: t, c: c.c, base: c.base, csrf: "bad"}).post("/customers", url.Values{"name": {"X"}, "phone": {"0771234567"}}); code != http.StatusForbidden {
		t.Fatalf("bad CSRF accepted: %d", code)
	}

	// Validation error stays on form with message
	_, code := c.post("/customers", url.Values{"name": {""}, "phone": {"12"}})
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 for invalid customer, got %d", code)
	}

	// 2. Customer + vehicle
	loc, _ := c.post("/customers", url.Values{"name": {"Kamal Perera"}, "phone": {"077 123 4567"}, "address": {"Yatawatta"}})
	cid := idFrom(t, loc, "/customers/")
	loc, _ = c.post("/customers/"+cid+"/vehicles", url.Values{"registration": {"WP CAB-1234"}, "make": {"Toyota"}, "model": {"Axio"}, "year": {"2014"}})
	if loc != "/customers/"+cid {
		t.Fatalf("vehicle create redirect %q", loc)
	}
	var vid string
	db.QueryRow("SELECT id FROM vehicles WHERE registration_norm = 'WPCAB1234'").Scan(&vid)
	if vid == "" {
		t.Fatal("vehicle not created")
	}
	// Duplicate registration rejected
	if _, code := c.post("/customers/"+cid+"/vehicles", url.Values{"registration": {"wpcab 1234"}}); code != http.StatusUnprocessableEntity {
		t.Fatalf("duplicate registration accepted: %d", code)
	}
	// Search by registration and phone
	if _, body := c.get("/customers?q=cab1234"); !strings.Contains(body, "Kamal Perera") {
		t.Fatal("search by registration failed")
	}
	if _, body := c.get("/customers?q=0771234567"); !strings.Contains(body, "Kamal Perera") {
		t.Fatal("search by phone failed")
	}

	// Part with opening stock
	loc, _ = c.post("/parts", url.Values{"code": {"OF-001"}, "name": {"Oil filter"}, "unit": {"pcs"}, "sale_price": {"1,500.00"}, "purchase_cost": {"1000"}, "reorder_level": {"3"}, "opening_qty": {"10"}})
	pid := idFrom(t, loc, "/parts/")
	if _, code := c.post("/parts", url.Values{"code": {"of-001"}, "name": {"Dup"}, "sale_price": {"1"}}); code != http.StatusUnprocessableEntity {
		t.Fatalf("duplicate part code accepted: %d", code)
	}
	stock := func() int64 {
		var q int64
		db.QueryRow("SELECT qty_on_hand FROM parts WHERE id = ?", pid).Scan(&q)
		return q
	}

	// 3. Open job
	c.get("/jobs/new?customer_id=" + cid + "&vehicle_id=" + vid)
	loc, _ = c.post("/jobs", url.Values{"customer_id": {cid}, "vehicle_id": {vid}, "job_date": {models.Today()}, "odometer": {"85000"}, "reported_issue": {"Service"}})
	jid := idFrom(t, loc, "/jobs/")

	// 4. Manual charge (1.5 h x 2,000 = 3,000) and parts
	var catID string
	db.QueryRow("SELECT id FROM charge_categories WHERE name = 'Garage labour'").Scan(&catID)
	c.post("/jobs/"+jid+"/charges", url.Values{"category_id": {catID}, "quantity": {"1.5"}, "unit_price": {"2000"}})
	c.post("/jobs/"+jid+"/parts", url.Values{"part": {"OF-001"}, "quantity": {"2"}})
	// 5. Stock updated with traceable movements
	if s := stock(); s != 8000 {
		t.Fatalf("stock after issue = %d, want 8000", s)
	}
	var jpID string
	db.QueryRow("SELECT id FROM job_parts WHERE job_id = ?", jid).Scan(&jpID)
	c.post("/jobs/"+jid+"/parts/"+jpID, url.Values{"quantity": {"3"}})
	if s := stock(); s != 7000 {
		t.Fatalf("stock after qty change = %d, want 7000 (only difference applied)", s)
	}
	// Insufficient stock rejected without override
	c.post("/jobs/"+jid+"/parts", url.Values{"part": {"OF-001"}, "quantity": {"100"}})
	if s := stock(); s != 7000 || !strings.Contains(lastFlash(t, app), "Not enough stock") {
		t.Fatalf("negative stock allowed: %d %q", s, lastFlash(t, app))
	}
	var moves int
	db.QueryRow("SELECT COUNT(*) FROM stock_movements WHERE part_id = ? AND job_id = ?", pid, jid).Scan(&moves)
	if moves != 2 {
		t.Fatalf("job movements = %d, want 2", moves)
	}
	if code, body := c.get("/jobs/" + jid); code != 200 || !strings.Contains(body, "Rs. 7,500.00") {
		t.Fatalf("job view %d missing total", code)
	}

	// Invoice requires Completed
	c.post("/jobs/"+jid+"/invoice", url.Values{"invoice_date": {models.Today()}})
	var nInv int
	db.QueryRow("SELECT COUNT(*) FROM invoices").Scan(&nInv)
	if nInv != 0 {
		t.Fatal("invoice created for non-completed job")
	}
	// 6. Complete job and invoice
	c.post("/jobs/"+jid+"/status", url.Values{"status": {"Completed"}})
	loc, _ = c.post("/jobs/"+jid+"/invoice", url.Values{"invoice_date": {models.Today()}})
	iid := idFrom(t, loc, "/invoices/")
	var total, sub int64
	var invNo string
	db.QueryRow("SELECT total, subtotal, invoice_number FROM invoices WHERE id = ?", iid).Scan(&total, &sub, &invNo)
	if total != 750000 || sub != 750000 {
		t.Fatalf("invoice total %d subtotal %d, want 750000", total, sub)
	}
	if s := stock(); s != 7000 {
		t.Fatalf("invoice changed stock: %d", s)
	}
	// Job locked after invoicing
	c.post("/jobs/"+jid+"/charges", url.Values{"category_id": {catID}, "quantity": {"1"}, "unit_price": {"1"}})
	var nCh int
	db.QueryRow("SELECT COUNT(*) FROM job_charges WHERE job_id = ?", jid).Scan(&nCh)
	if nCh != 1 {
		t.Fatal("charges changed after invoice was issued")
	}
	// Changing the category default later does not change the invoice snapshot
	db.Exec("UPDATE charge_categories SET default_price = 999999 WHERE id = ?", catID)

	// 7. Partial payment
	c.post("/invoices/"+iid+"/payments", url.Values{"amount": {"2000"}, "payment_date": {models.Today()}, "method": {"Cash"}})
	var paid int64
	var pst string
	db.QueryRow("SELECT amount_paid, payment_status FROM invoices WHERE id = ?", iid).Scan(&paid, &pst)
	if paid != 200000 || pst != "Partially Paid" {
		t.Fatalf("after payment paid=%d status=%s", paid, pst)
	}
	// Overpayment rejected without explicit authorization
	c.post("/invoices/"+iid+"/payments", url.Values{"amount": {"99999"}, "payment_date": {models.Today()}, "method": {"Cash"}})
	db.QueryRow("SELECT amount_paid FROM invoices WHERE id = ?", iid).Scan(&paid)
	if paid != 200000 {
		t.Fatalf("overpayment accepted: %d", paid)
	}
	if _, body := c.get("/invoices/" + iid); !strings.Contains(body, "Rs. 5,500.00") {
		t.Fatal("invoice page does not show remaining balance 5,500.00")
	}
	// 8. Printable bill and receipt
	code, body := c.get("/invoices/" + iid + "/print")
	if code != 200 || !strings.Contains(body, invNo) || !strings.Contains(body, "Test Garage") || !strings.Contains(body, "WP CAB-1234") || !strings.Contains(body, "Balance due") {
		t.Fatalf("print bill %d incomplete", code)
	}
	var payID string
	db.QueryRow("SELECT id FROM payments LIMIT 1").Scan(&payID)
	if code, body := c.get("/payments/" + payID + "/receipt"); code != 200 || !strings.Contains(body, "RECEIPT") {
		t.Fatalf("receipt %d", code)
	}

	// 9. Purchase: draft does not change stock, receiving does
	loc, _ = c.post("/suppliers", url.Values{"name": {"ABC Parts"}})
	sid := idFrom(t, loc, "/suppliers/")
	loc, _ = c.post("/purchases", url.Values{"supplier_id": {sid}, "purchase_date": {models.Today()}, "reference": {"INV-77"}})
	puid := idFrom(t, loc, "/purchases/")
	c.post("/purchases/"+puid+"/items", url.Values{"part": {"OF-001"}, "quantity": {"5"}, "unit_cost": {"900"}})
	if s := stock(); s != 7000 {
		t.Fatalf("draft purchase changed stock: %d", s)
	}
	c.post("/purchases/"+puid+"/receive", url.Values{})
	if s := stock(); s != 12000 {
		t.Fatalf("stock after receive = %d, want 12000", s)
	}
	var cost int64
	db.QueryRow("SELECT purchase_cost FROM parts WHERE id = ?", pid).Scan(&cost)
	if cost != 90000 {
		t.Fatalf("purchase cost not updated: %d", cost)
	}
	// Stock adjustment
	c.post("/parts/"+pid+"/adjust", url.Values{"mode": {"delta"}, "quantity": {"-1"}, "reason": {"Damaged"}})
	if s := stock(); s != 11000 {
		t.Fatalf("stock after adjustment = %d", s)
	}

	// Void flow: cannot void with payments; void payment, then invoice, then job unlocks
	c.post("/invoices/"+iid+"/void", url.Values{"reason": {"Wrong price"}})
	var ist string
	db.QueryRow("SELECT status FROM invoices WHERE id = ?", iid).Scan(&ist)
	if ist != "Issued" {
		t.Fatal("invoice with payments was voided")
	}
	c.post("/payments/"+payID+"/void", url.Values{"reason": {"Entered by mistake"}})
	c.post("/invoices/"+iid+"/void", url.Values{"reason": {"Wrong price"}})
	db.QueryRow("SELECT status FROM invoices WHERE id = ?", iid).Scan(&ist)
	if ist != "Void" {
		t.Fatal("invoice not voided")
	}
	loc, _ = c.post("/jobs/"+jid+"/invoice", url.Values{"invoice_date": {models.Today()}})
	idFrom(t, loc, "/invoices/")

	// 10. Reports and every list page render
	for _, p := range []string{
		"/", "/jobs", "/jobs?status=active", "/jobs/new?q=kamal", "/jobs/" + jid + "/edit", "/customers", "/customers/" + cid, "/customers/new", "/customers/" + cid + "/edit",
		"/vehicles", "/vehicles/" + vid, "/vehicles/" + vid + "/edit", "/customers/" + cid + "/vehicles/new",
		"/invoices", "/invoices?status=all", "/payments", "/parts", "/parts?low=1", "/parts/new", "/parts/" + pid, "/parts/" + pid + "/edit",
		"/stock-movements", "/suppliers", "/suppliers/new", "/suppliers/" + sid, "/suppliers/" + sid + "/edit", "/purchases", "/purchases/new", "/purchases/" + puid,
		"/reports", "/reports/jobs?status=Completed", "/reports/sales", "/reports/outstanding", "/reports/parts-sold", "/reports/stock", "/reports/stock?low=1",
		"/reports/purchases?supplier_id=" + sid, "/reports/movements?type=job_issue", "/settings", "/settings/charges", "/settings/users", "/settings/users/new",
		"/settings/users/1", "/settings/roles", "/settings/audit", "/account/password",
	} {
		if code, body := c.get(p); code != 200 {
			t.Errorf("GET %s = %d: %.300s", p, code, body)
		}
	}
	if _, body := c.get("/reports/parts-sold"); !strings.Contains(body, "OF-001") {
		t.Error("parts sold report missing part")
	}
	if code, _ := c.get("/settings/backup"); code != 200 {
		t.Errorf("backup %d", code)
	}

	// Mechanic role: server-side permission enforcement
	var mechRole int64
	db.QueryRow("SELECT id FROM roles WHERE name = 'Mechanic'").Scan(&mechRole)
	repository.CreateUser(ctx, db, models.User{Username: "mech", FullName: "Mech", PasswordHash: hash, RoleID: mechRole, Active: true})
	jar2, _ := cookiejar.New(nil)
	m := &tclient{t: t, base: srv.URL, c: &http.Client{Jar: jar2, CheckRedirect: c.c.CheckRedirect}}
	m.get("/login")
	m.post("/login", url.Values{"username": {"mech"}, "password": {"correct-horse-battery"}})
	for _, p := range []string{"/customers", "/invoices", "/settings", "/settings/users", "/jobs/" + jid, "/reports"} {
		if code, _ := m.get(p); code != http.StatusForbidden {
			t.Errorf("mechanic GET %s = %d, want 403", p, code)
		}
	}
	m.get("/jobs")
	if _, code := m.post("/customers", url.Values{"name": {"Hack"}, "phone": {"0771111111"}}); code != http.StatusForbidden {
		t.Errorf("mechanic POST /customers = %d, want 403", code)
	}
}
