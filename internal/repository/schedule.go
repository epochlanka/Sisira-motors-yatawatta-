package repository

import (
	"context"
	"database/sql"

	"garagems/internal/models"
)

// ---------- appointments ----------

type ApptFilter struct {
	Q          string
	From, To   string // appt_date range, inclusive
	Status     string // a status, "open" (scheduled) or ""
	AssignedID int64
	CustomerID int64
	VehicleID  int64
}

const apptFrom = ` FROM appointments a
	LEFT JOIN customers c ON c.id = a.customer_id
	LEFT JOIN vehicles v ON v.id = a.vehicle_id
	LEFT JOIN users u ON u.id = a.assigned_user_id
	LEFT JOIN jobs j ON j.id = a.job_id
	LEFT JOIN users cb ON cb.id = a.created_by`

const apptSelect = `SELECT a.id, a.appt_date, a.start_time, a.duration_min, a.customer_id, COALESCE(c.name, ''), a.vehicle_id,
	a.contact_name, a.contact_phone, COALESCE(v.registration, a.vehicle_reg), COALESCE(TRIM(v.make || ' ' || v.model), ''),
	a.service, a.notes, a.assigned_user_id, COALESCE(u.full_name, ''), a.status, a.job_id, COALESCE(j.job_number, ''),
	a.cancel_reason, COALESCE(cb.full_name, ''), a.created_at, a.updated_at` + apptFrom

func scanAppt(sc interface{ Scan(...any) error }) (models.Appointment, error) {
	var a models.Appointment
	err := sc.Scan(&a.ID, &a.ApptDate, &a.StartTime, &a.DurationMin, &a.CustomerID, &a.CustomerName, &a.VehicleID,
		&a.ContactName, &a.ContactPhone, &a.VehicleReg, &a.VehicleDesc,
		&a.Service, &a.Notes, &a.AssignedUserID, &a.AssignedName, &a.Status, &a.JobID, &a.JobNumber,
		&a.CancelReason, &a.CreatedBy, &a.CreatedAt, &a.UpdatedAt)
	return a, err
}

func apptWhere(f ApptFilter) Where {
	var w Where
	if f.Q != "" {
		cond := `(a.contact_name LIKE ? ESCAPE '\' OR c.name LIKE ? ESCAPE '\' OR a.service LIKE ? ESCAPE '\'`
		args := []any{Like(f.Q), Like(f.Q), Like(f.Q)}
		if reg := models.NormalizeReg(f.Q); reg != "" {
			cond += ` OR v.registration_norm LIKE ? ESCAPE '\' OR UPPER(REPLACE(REPLACE(a.vehicle_reg, ' ', ''), '-', '')) LIKE ? ESCAPE '\'`
			args = append(args, Like(reg), Like(reg))
		}
		if d := models.DigitsOnly(f.Q); d != "" {
			cond += ` OR REPLACE(REPLACE(REPLACE(a.contact_phone, ' ', ''), '-', ''), '+', '') LIKE ? ESCAPE '\'`
			args = append(args, Like(d))
		}
		w.Add(cond+")", args...)
	}
	if f.From != "" {
		w.Add("a.appt_date >= ?", f.From)
	}
	if f.To != "" {
		w.Add("a.appt_date <= ?", f.To)
	}
	switch f.Status {
	case "":
	case "open":
		w.Add("a.status = 'Scheduled'")
	default:
		w.Add("a.status = ?", f.Status)
	}
	if f.AssignedID != 0 {
		w.Add("a.assigned_user_id = ?", f.AssignedID)
	}
	if f.CustomerID != 0 {
		w.Add("a.customer_id = ?", f.CustomerID)
	}
	if f.VehicleID != 0 {
		w.Add("a.vehicle_id = ?", f.VehicleID)
	}
	return w
}

func ListAppointments(ctx context.Context, q DBTX, f ApptFilter, o ListOpts) ([]models.Appointment, int, error) {
	w := apptWhere(f)
	total, err := count(ctx, q, "SELECT COUNT(*)"+apptFrom+w.SQL(), w.Args...)
	if err != nil {
		return nil, 0, err
	}
	rows, err := q.QueryContext(ctx, apptSelect+w.SQL()+o.SQL(), w.Args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []models.Appointment
	for rows.Next() {
		a, err := scanAppt(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, a)
	}
	return out, total, rows.Err()
}

func CountAppointments(ctx context.Context, q DBTX, f ApptFilter) (int, error) {
	w := apptWhere(f)
	return count(ctx, q, "SELECT COUNT(*)"+apptFrom+w.SQL(), w.Args...)
}

func GetAppointment(ctx context.Context, q DBTX, id int64) (models.Appointment, error) {
	a, err := scanAppt(q.QueryRowContext(ctx, apptSelect+" WHERE a.id = ?", id))
	return a, notFound(err)
}

// AppointmentForJob returns the appointment that was checked in as the given job, if any.
func AppointmentForJob(ctx context.Context, q DBTX, jobID int64) (models.Appointment, error) {
	a, err := scanAppt(q.QueryRowContext(ctx, apptSelect+" WHERE a.job_id = ?", jobID))
	return a, notFound(err)
}

func CreateAppointment(ctx context.Context, q DBTX, a models.Appointment, userID int64) (int64, error) {
	now := models.NowUTC()
	res, err := q.ExecContext(ctx, `INSERT INTO appointments(appt_date, start_time, duration_min, customer_id, vehicle_id, contact_name, contact_phone,
		vehicle_reg, service, notes, assigned_user_id, status, created_by, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'Scheduled', ?, ?, ?)`,
		a.ApptDate, a.StartTime, a.DurationMin, a.CustomerID, a.VehicleID, a.ContactName, a.ContactPhone,
		a.VehicleReg, a.Service, a.Notes, a.AssignedUserID, userID, now, now)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func UpdateAppointment(ctx context.Context, q DBTX, a models.Appointment) error {
	_, err := q.ExecContext(ctx, `UPDATE appointments SET appt_date = ?, start_time = ?, duration_min = ?, customer_id = ?, vehicle_id = ?,
		contact_name = ?, contact_phone = ?, vehicle_reg = ?, service = ?, notes = ?, assigned_user_id = ?, updated_at = ? WHERE id = ?`,
		a.ApptDate, a.StartTime, a.DurationMin, a.CustomerID, a.VehicleID, a.ContactName, a.ContactPhone, a.VehicleReg,
		a.Service, a.Notes, a.AssignedUserID, models.NowUTC(), a.ID)
	return err
}

func SetAppointmentStatus(ctx context.Context, q DBTX, id int64, status, reason string, jobID sql.NullInt64) error {
	_, err := q.ExecContext(ctx, "UPDATE appointments SET status = ?, cancel_reason = ?, job_id = ?, updated_at = ? WHERE id = ?",
		status, reason, jobID, models.NowUTC(), id)
	return err
}

// StaffBookings returns the scheduled or checked-in appointments of one staff member on a date.
func StaffBookings(ctx context.Context, q DBTX, userID int64, date string) ([]models.Appointment, error) {
	list, _, err := ListAppointments(ctx, q, ApptFilter{AssignedID: userID, From: date, To: date},
		ListOpts{OrderBy: "a.start_time"})
	if err != nil {
		return nil, err
	}
	out := list[:0]
	for _, a := range list {
		if a.Status == models.ApptScheduled || a.Status == models.ApptCheckedIn {
			out = append(out, a)
		}
	}
	return out, nil
}

// ---------- timesheets ----------

type TimesheetFilter struct {
	UserID   int64
	From, To string
}

const tsSelect = `SELECT t.id, t.user_id, u.full_name, t.work_date, t.time_in, t.time_out, t.break_min, t.notes, t.created_at, t.updated_at
	FROM timesheets t JOIN users u ON u.id = t.user_id`

func scanTimesheet(sc interface{ Scan(...any) error }) (models.Timesheet, error) {
	var t models.Timesheet
	err := sc.Scan(&t.ID, &t.UserID, &t.UserName, &t.WorkDate, &t.TimeIn, &t.TimeOut, &t.BreakMin, &t.Notes, &t.CreatedAt, &t.UpdatedAt)
	return t, err
}

func tsWhere(f TimesheetFilter) Where {
	var w Where
	if f.UserID != 0 {
		w.Add("t.user_id = ?", f.UserID)
	}
	if f.From != "" {
		w.Add("t.work_date >= ?", f.From)
	}
	if f.To != "" {
		w.Add("t.work_date <= ?", f.To)
	}
	return w
}

func ListTimesheets(ctx context.Context, q DBTX, f TimesheetFilter, o ListOpts) ([]models.Timesheet, int, error) {
	w := tsWhere(f)
	total, err := count(ctx, q, "SELECT COUNT(*) FROM timesheets t JOIN users u ON u.id = t.user_id"+w.SQL(), w.Args...)
	if err != nil {
		return nil, 0, err
	}
	rows, err := q.QueryContext(ctx, tsSelect+w.SQL()+o.SQL(), w.Args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []models.Timesheet
	for rows.Next() {
		t, err := scanTimesheet(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, t)
	}
	return out, total, rows.Err()
}

func GetTimesheet(ctx context.Context, q DBTX, id int64) (models.Timesheet, error) {
	t, err := scanTimesheet(q.QueryRowContext(ctx, tsSelect+" WHERE t.id = ?", id))
	return t, notFound(err)
}

func TimesheetFor(ctx context.Context, q DBTX, userID int64, date string) (models.Timesheet, error) {
	t, err := scanTimesheet(q.QueryRowContext(ctx, tsSelect+" WHERE t.user_id = ? AND t.work_date = ?", userID, date))
	return t, notFound(err)
}

func CreateTimesheet(ctx context.Context, q DBTX, t models.Timesheet, by int64) (int64, error) {
	now := models.NowUTC()
	res, err := q.ExecContext(ctx, `INSERT INTO timesheets(user_id, work_date, time_in, time_out, break_min, notes, created_by, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, t.UserID, t.WorkDate, t.TimeIn, t.TimeOut, t.BreakMin, t.Notes, by, now, now)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func UpdateTimesheet(ctx context.Context, q DBTX, t models.Timesheet) error {
	_, err := q.ExecContext(ctx, "UPDATE timesheets SET time_in = ?, time_out = ?, break_min = ?, notes = ?, updated_at = ? WHERE id = ?",
		t.TimeIn, t.TimeOut, t.BreakMin, t.Notes, models.NowUTC(), t.ID)
	return err
}

// WorkedMinutes sums worked minutes per user in a date range.
func WorkedMinutes(ctx context.Context, q DBTX, from, to string) (map[int64]int, error) {
	list, _, err := ListTimesheets(ctx, q, TimesheetFilter{From: from, To: to}, ListOpts{})
	if err != nil {
		return nil, err
	}
	out := map[int64]int{}
	for _, t := range list {
		out[t.UserID] += t.WorkedMin()
	}
	return out, nil
}

// ---------- expenses ----------

type ExpenseFilter struct {
	Q        string
	Category string
	Method   string
	From, To string
	Status   string // "" valid only, "void", "all"
}

const expSelect = `SELECT e.id, e.expense_date, e.category, e.description, e.amount, e.method, e.paid_to, e.reference, e.notes,
	e.status, e.void_reason, COALESCE(e.voided_at, ''), COALESCE(u.full_name, ''), e.created_at
	FROM expenses e LEFT JOIN users u ON u.id = e.created_by`

func scanExpense(sc interface{ Scan(...any) error }) (models.Expense, error) {
	var e models.Expense
	err := sc.Scan(&e.ID, &e.ExpenseDate, &e.Category, &e.Description, &e.Amount, &e.Method, &e.PaidTo, &e.Reference, &e.Notes,
		&e.Status, &e.VoidReason, &e.VoidedAt, &e.CreatedBy, &e.CreatedAt)
	return e, err
}

func expWhere(f ExpenseFilter) Where {
	var w Where
	if f.Q != "" {
		w.Add(`(e.description LIKE ? ESCAPE '\' OR e.paid_to LIKE ? ESCAPE '\' OR e.reference LIKE ? ESCAPE '\' OR e.notes LIKE ? ESCAPE '\')`,
			Like(f.Q), Like(f.Q), Like(f.Q), Like(f.Q))
	}
	if f.Category != "" {
		w.Add("e.category = ?", f.Category)
	}
	if f.Method != "" {
		w.Add("e.method = ?", f.Method)
	}
	if f.From != "" {
		w.Add("e.expense_date >= ?", f.From)
	}
	if f.To != "" {
		w.Add("e.expense_date <= ?", f.To)
	}
	switch f.Status {
	case "all":
	case "void":
		w.Add("e.status = 'Void'")
	default:
		w.Add("e.status = 'Valid'")
	}
	return w
}

// ListExpenses returns a page of expenses, the total row count and the sum of valid amounts matching the filter.
func ListExpenses(ctx context.Context, q DBTX, f ExpenseFilter, o ListOpts) ([]models.Expense, int, models.Money, error) {
	w := expWhere(f)
	var total int
	var sum models.Money
	if err := q.QueryRowContext(ctx, "SELECT COUNT(*), COALESCE(SUM(CASE WHEN e.status = 'Valid' THEN e.amount END), 0) FROM expenses e"+w.SQL(), w.Args...).
		Scan(&total, &sum); err != nil {
		return nil, 0, 0, err
	}
	rows, err := q.QueryContext(ctx, expSelect+w.SQL()+o.SQL(), w.Args...)
	if err != nil {
		return nil, 0, 0, err
	}
	defer rows.Close()
	var out []models.Expense
	for rows.Next() {
		e, err := scanExpense(rows)
		if err != nil {
			return nil, 0, 0, err
		}
		out = append(out, e)
	}
	return out, total, sum, rows.Err()
}

func GetExpense(ctx context.Context, q DBTX, id int64) (models.Expense, error) {
	e, err := scanExpense(q.QueryRowContext(ctx, expSelect+" WHERE e.id = ?", id))
	return e, notFound(err)
}

func CreateExpense(ctx context.Context, q DBTX, e models.Expense, by int64) (int64, error) {
	res, err := q.ExecContext(ctx, `INSERT INTO expenses(expense_date, category, description, amount, method, paid_to, reference, notes, status, created_by, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'Valid', ?, ?)`,
		e.ExpenseDate, e.Category, e.Description, e.Amount, e.Method, e.PaidTo, e.Reference, e.Notes, by, models.NowUTC())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func VoidExpense(ctx context.Context, q DBTX, id int64, reason string, by int64) error {
	_, err := q.ExecContext(ctx, "UPDATE expenses SET status = 'Void', void_reason = ?, voided_at = ?, voided_by = ? WHERE id = ? AND status = 'Valid'",
		reason, models.NowUTC(), by, id)
	return err
}

// ExpensesByCategory sums valid expenses per category in a date range.
func ExpensesByCategory(ctx context.Context, q DBTX, from, to string) ([]StatusCount, error) {
	w := expWhere(ExpenseFilter{From: from, To: to})
	rows, err := q.QueryContext(ctx, "SELECT e.category, COUNT(*), SUM(e.amount) FROM expenses e"+w.SQL()+
		" GROUP BY e.category ORDER BY SUM(e.amount) DESC", w.Args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StatusCount
	for rows.Next() {
		var s StatusCount
		if err := rows.Scan(&s.Status, &s.Count, &s.Total); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
