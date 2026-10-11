package handlers

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"garagems/internal/config"
	"garagems/internal/database"
	"garagems/internal/models"
	"garagems/internal/repository"
)

// TestSchedulingStaffAndExpenses covers appointments, service reminders, timesheets,
// expenses and the reports built on them.
func TestSchedulingStaffAndExpenses(t *testing.T) {
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
	if err := database.SeedDefaults(ctx, db, cfg); err != nil {
		t.Fatal(err)
	}
	hash, _ := HashPassword("correct-horse-battery")
	repository.CreateUser(ctx, db, models.User{Username: "admin", FullName: "Admin", PasswordHash: hash, RoleID: 1, Active: true})
	var mechRole int64
	db.QueryRow("SELECT id FROM roles WHERE name = 'Mechanic'").Scan(&mechRole)
	mechID, _ := repository.CreateUser(ctx, db, models.User{Username: "mech", FullName: "Sunil Mechanic", PasswordHash: hash, RoleID: mechRole, Active: true})
	app, err := New(db, cfg)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(app.Routes())
	defer srv.Close()
	login := func(user string) *tclient {
		jar, _ := cookiejar.New(nil)
		c := &tclient{t: t, base: srv.URL, c: &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
		c.get("/login")
		if loc, _ := c.post("/login", url.Values{"username": {user}, "password": {"correct-horse-battery"}}); loc != "/" {
			t.Fatalf("login %s: %q", user, loc)
		}
		c.get("/")
		return c
	}
	c := login("admin")
	today := models.Today()
	mech := strconv.FormatInt(mechID, 10)

	// Permission upgrade grants new permissions to default roles of older databases once.
	db.Exec("DELETE FROM role_permissions WHERE permission = 'appointments.view' AND role_id = (SELECT id FROM roles WHERE name = 'Manager')")
	db.Exec("DELETE FROM settings WHERE key = 'perm_upgrade_2'")
	if err := database.SeedDefaults(ctx, db, cfg); err != nil {
		t.Fatal(err)
	}
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM role_permissions rp JOIN roles r ON r.id = rp.role_id WHERE r.name = 'Manager' AND rp.permission = 'appointments.view'`).Scan(&n)
	if n != 1 {
		t.Fatal("permission upgrade not applied to Manager")
	}

	// Customer and vehicle with a service reminder in 3 days.
	loc, _ := c.post("/customers", url.Values{"name": {"Nimal Silva"}, "phone": {"071 555 1234"}})
	cid := idFrom(t, loc, "/customers/")
	c.post("/customers/"+cid+"/vehicles", url.Values{"registration": {"WP KA-4455"}, "make": {"Honda"}, "model": {"Fit"},
		"next_service_date": {addDays(today, 3)}, "next_service_km": {"90000"}, "service_note": {"Oil change"}})
	var vid string
	db.QueryRow("SELECT id FROM vehicles WHERE registration_norm = 'WPKA4455'").Scan(&vid)
	if _, body := c.get("/vehicles/service-due"); !strings.Contains(body, "WP KA-4455") || !strings.Contains(body, "Oil change") {
		t.Fatal("service-due list missing vehicle")
	}
	if _, body := c.get("/vehicles/service-due?within=0"); strings.Contains(body, "WP KA-4455") {
		t.Fatal("vehicle due in 3 days listed as overdue")
	}

	// Book an appointment; a clashing booking for the same staff member is refused.
	loc, _ = c.post("/appointments", url.Values{"customer_id": {cid}, "vehicle_id": {vid}, "appt_date": {today}, "start_time": {"09:00"},
		"duration_min": {"60"}, "service": {"Full service"}, "assigned_user_id": {mech}})
	a1 := idFrom(t, loc, "/appointments/")
	if _, code := c.post("/appointments", url.Values{"customer_id": {cid}, "appt_date": {today}, "start_time": {"09:30"},
		"duration_min": {"30"}, "service": {"Wash"}, "assigned_user_id": {mech}}); code != http.StatusUnprocessableEntity {
		t.Fatalf("double booking accepted: %d", code)
	}
	// Back-to-back is fine; booking in the past is not.
	loc, _ = c.post("/appointments", url.Values{"customer_id": {cid}, "appt_date": {today}, "start_time": {"10:00"},
		"duration_min": {"30"}, "service": {"Wheel alignment"}, "assigned_user_id": {mech}})
	a2 := idFrom(t, loc, "/appointments/")
	if _, code := c.post("/appointments", url.Values{"customer_id": {cid}, "appt_date": {addDays(today, -1)}, "start_time": {"10:00"},
		"duration_min": {"30"}, "service": {"Late"}}); code != http.StatusUnprocessableEntity {
		t.Fatalf("past booking accepted: %d", code)
	}

	// Cancel needs a reason; cancelled bookings can be reinstated.
	c.post("/appointments/"+a2+"/close", url.Values{"status": {"Cancelled"}})
	if st := apptStatus(db, a2); st != "Scheduled" {
		t.Fatalf("cancelled without reason: %s", st)
	}
	c.post("/appointments/"+a2+"/close", url.Values{"status": {"Cancelled"}, "reason": {"Customer called"}})
	if st := apptStatus(db, a2); st != "Cancelled" {
		t.Fatalf("not cancelled: %s", st)
	}
	c.post("/appointments/"+a2+"/reinstate", url.Values{})
	if st := apptStatus(db, a2); st != "Scheduled" {
		t.Fatalf("not reinstated: %s", st)
	}

	// Check-in opens a job with the booked work and staff member.
	loc, _ = c.post("/appointments/"+a1+"/check-in", url.Values{})
	jid := idFrom(t, loc, "/jobs/")
	var issue string
	var assigned sql.NullInt64
	db.QueryRow("SELECT reported_issue, assigned_user_id FROM jobs WHERE id = ?", jid).Scan(&issue, &assigned)
	if issue != "Full service" || assigned.Int64 != mechID {
		t.Fatalf("job from appointment: issue %q assigned %v", issue, assigned)
	}
	if st := apptStatus(db, a1); st != "Checked In" {
		t.Fatalf("appointment status %s", st)
	}
	if _, code := c.post("/appointments/"+a1+"/check-in", url.Values{}); code != http.StatusSeeOther || jobCount(db) != 1 {
		t.Fatal("second check-in created another job")
	}

	// A new caller is matched/created as a customer with their vehicle at check-in.
	loc, _ = c.post("/appointments", url.Values{"contact_name": {"Ruwan Fernando"}, "contact_phone": {"0779876543"}, "vehicle_reg": {"CP ABC-9999"},
		"appt_date": {today}, "start_time": {"13:00"}, "duration_min": {"45"}, "service": {"Brake noise"}})
	a3 := idFrom(t, loc, "/appointments/")
	loc, _ = c.post("/appointments/"+a3+"/check-in", url.Values{})
	j2 := idFrom(t, loc, "/jobs/")
	var custName, reg string
	db.QueryRow("SELECT c.name, v.registration FROM jobs j JOIN customers c ON c.id = j.customer_id JOIN vehicles v ON v.id = j.vehicle_id WHERE j.id = ?", j2).Scan(&custName, &reg)
	if custName != "Ruwan Fernando" || reg != "CP ABC-9999" {
		t.Fatalf("walk-in check-in: customer %q vehicle %q", custName, reg)
	}

	// Labour on the first job, complete, invoice: feeds services, staff and profit reports.
	var catID string
	db.QueryRow("SELECT id FROM charge_categories WHERE name = 'Garage labour'").Scan(&catID)
	c.post("/jobs/"+jid+"/charges", url.Values{"category_id": {catID}, "quantity": {"2"}, "unit_price": {"1500"}})
	c.post("/jobs/"+jid+"/status", url.Values{"status": {"Completed"}})
	c.post("/jobs/"+jid+"/invoice", url.Values{"invoice_date": {today}})
	// Next-service reminder from the job page.
	c.post("/vehicles/"+vid+"/service", url.Values{"next": {"/jobs/" + jid}, "next_service_date": {addDays(today, 180)}, "service_note": {"Next oil change"}})
	var nsd string
	db.QueryRow("SELECT next_service_date FROM vehicles WHERE id = ?", vid).Scan(&nsd)
	if nsd != addDays(today, 180) {
		t.Fatalf("service reminder not saved: %q", nsd)
	}

	// Expenses: validation, create, void.
	if _, code := c.post("/expenses", url.Values{"expense_date": {today}, "category": {"Rent"}, "description": {"Rent"}, "amount": {"0"}, "method": {"Cash"}}); code != http.StatusUnprocessableEntity {
		t.Fatalf("zero expense accepted: %d", code)
	}
	loc, _ = c.post("/expenses", url.Values{"expense_date": {today}, "category": {"Electricity & water"}, "description": {"Electricity bill"}, "amount": {"1,000.00"}, "method": {"Cash"}})
	idFrom(t, loc, "/expenses/")
	loc, _ = c.post("/expenses", url.Values{"expense_date": {today}, "category": {"Other"}, "description": {"Mistake"}, "amount": {"500"}, "method": {"Cash"}})
	e2 := idFrom(t, loc, "/expenses/")
	c.post("/expenses/"+e2+"/void", url.Values{"reason": {"Entered twice"}})
	p, err := repository.Profit(ctx, db, today, today)
	if err != nil {
		t.Fatal(err)
	}
	if p.NetSales != 300000 || p.Expenses != 100000 || p.NetProfit != 200000 {
		t.Fatalf("profit summary %+v", p)
	}

	// Timesheets: clock in once, clock out; manager entry; duplicate refused.
	m := login("mech")
	m.post("/timesheet/clock-in", url.Values{"next": {"/"}})
	if !strings.Contains(flashFor(db, mechID), "Clocked in") {
		t.Fatalf("clock in: %q", flashFor(db, mechID))
	}
	m.post("/timesheet/clock-in", url.Values{})
	if !strings.Contains(flashFor(db, mechID), "already clocked in") {
		t.Fatalf("second clock in: %q", flashFor(db, mechID))
	}
	m.post("/timesheet/clock-out", url.Values{"break_min": {"0"}})
	var out sql.NullString
	db.QueryRow("SELECT time_out FROM timesheets WHERE user_id = ? AND work_date = ?", mechID, today).Scan(&out)
	if !out.Valid {
		t.Fatal("clock out not recorded")
	}
	yday := addDays(today, -1)
	c.post("/staff/timesheets", url.Values{"user_id": {mech}, "work_date": {yday}, "time_in": {"08:00"}, "time_out": {"17:00"}, "break_min": {"60"}})
	if _, code := c.post("/staff/timesheets", url.Values{"user_id": {mech}, "work_date": {yday}, "time_in": {"09:00"}, "time_out": {"12:00"}}); code != http.StatusUnprocessableEntity {
		t.Fatalf("duplicate timesheet accepted: %d", code)
	}
	worked, _ := repository.WorkedMinutes(ctx, db, yday, yday)
	if worked[mechID] != 480 {
		t.Fatalf("worked minutes = %d, want 480", worked[mechID])
	}

	// Every new page renders.
	for _, path := range []string{
		"/", "/appointments", "/appointments?date=" + addDays(today, 7), "/appointments?view=list&status=open", "/appointments/new", "/appointments/new?q=nimal",
		"/appointments/new?new=1", "/appointments/new?customer_id=" + cid + "&vehicle_id=" + vid, "/appointments/" + a1, "/appointments/" + a2 + "/edit",
		"/jobs/" + jid, "/vehicles/" + vid, "/vehicles/" + vid + "/edit", "/customers/" + cid, "/vehicles", "/vehicles/service-due?within=365",
		"/expenses", "/expenses/new", "/expenses/" + e2, "/staff", "/staff/timesheets", "/staff/timesheets/new",
		"/reports", "/reports/services", "/reports/staff", "/reports/expenses", "/reports/profit",
	} {
		if code, body := c.get(path); code != 200 {
			t.Errorf("GET %s = %d: %.300s", path, code, body)
		}
	}
	if _, body := c.get("/reports/services?from=" + today + "&to=" + today); !strings.Contains(body, "Garage labour") {
		t.Error("services report missing labour")
	}
	if _, body := c.get("/reports/staff?from=" + yday + "&to=" + today); !strings.Contains(body, "Sunil Mechanic") || !strings.Contains(body, "8h") {
		t.Error("staff report missing mechanic hours")
	}

	// Mechanic: own timesheet only, no appointments, expenses or staff pages.
	if code, _ := m.get("/staff/timesheets"); code != 200 {
		t.Errorf("mechanic timesheet = %d", code)
	}
	for _, path := range []string{"/appointments", "/expenses", "/staff", "/reports/staff", "/staff/timesheets/new"} {
		if code, _ := m.get(path); code != http.StatusForbidden {
			t.Errorf("mechanic GET %s = %d, want 403", path, code)
		}
	}
}

func apptStatus(db *sql.DB, id string) string {
	var s string
	db.QueryRow("SELECT status FROM appointments WHERE id = ?", id).Scan(&s)
	return s
}

func flashFor(db *sql.DB, userID int64) string {
	var f string
	db.QueryRow("SELECT flash FROM sessions WHERE user_id = ? ORDER BY created_at DESC LIMIT 1", userID).Scan(&f)
	return f
}

func jobCount(db *sql.DB) int {
	var n int
	db.QueryRow("SELECT COUNT(*) FROM jobs").Scan(&n)
	return n
}
