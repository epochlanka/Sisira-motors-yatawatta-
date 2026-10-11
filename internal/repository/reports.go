package repository

import (
	"context"
	"database/sql"
	"time"

	"garagems/internal/models"
)

type StatusCount struct {
	Status string
	Count  int
	Total  models.Money
}

// JobsByStatus counts jobs opened (job_date) in range grouped by current status, with job value.
func JobsByStatus(ctx context.Context, q DBTX, f JobFilter) ([]StatusCount, error) {
	w := jobWhere(f)
	rows, err := q.QueryContext(ctx, `SELECT j.status, COUNT(*),
		COALESCE(SUM((SELECT COALESCE(SUM(line_total),0) FROM job_charges WHERE job_id = j.id) + (SELECT COALESCE(SUM(line_total),0) FROM job_parts WHERE job_id = j.id)), 0)`+
		jobFrom+w.SQL()+" GROUP BY j.status", w.Args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byStatus := map[string]StatusCount{}
	for rows.Next() {
		var s StatusCount
		if err := rows.Scan(&s.Status, &s.Count, &s.Total); err != nil {
			return nil, err
		}
		byStatus[s.Status] = s
	}
	var out []StatusCount
	for _, st := range models.JobStatuses {
		s := byStatus[st]
		s.Status = st
		out = append(out, s)
	}
	return out, rows.Err()
}

// JobsCompletedBetween counts jobs whose completed_date falls in range.
func JobsCompletedBetween(ctx context.Context, q DBTX, from, to string) (int, error) {
	var w Where
	w.Add("status = 'Completed'")
	if from != "" {
		w.Add("completed_date >= ?", from)
	}
	if to != "" {
		w.Add("completed_date <= ?", to)
	}
	return count(ctx, q, "SELECT COUNT(*) FROM jobs"+w.SQL(), w.Args...)
}

type PartSales struct {
	PartID      int64
	Code        string
	Description string
	Unit        string
	Qty         models.Qty
	Revenue     models.Money
	Cost        models.Money
	InStock     models.Qty
}

func (p PartSales) Margin() models.Money { return p.Revenue - p.Cost }

// PartsSold aggregates part lines on issued invoices in the invoice date range.
// Cost uses the purchase cost snapshot recorded when the part was added to the job.
func PartsSold(ctx context.Context, q DBTX, from, to string, partID int64, category string) ([]PartSales, error) {
	var w Where
	w.Add("i.status = 'Issued'")
	w.Add("l.line_type = 'part'")
	if from != "" {
		w.Add("i.invoice_date >= ?", from)
	}
	if to != "" {
		w.Add("i.invoice_date <= ?", to)
	}
	if partID != 0 {
		w.Add("l.part_id = ?", partID)
	}
	if category != "" {
		w.Add("p.category = ?", category)
	}
	rows, err := q.QueryContext(ctx, `SELECT l.part_id, MAX(l.part_code), MAX(COALESCE(p.name, l.description)), MAX(l.unit), SUM(l.quantity), SUM(l.line_total),
		(SUM(l.quantity * l.unit_cost) + 500) / 1000, COALESCE(MAX(p.qty_on_hand), 0)
		FROM invoice_lines l JOIN invoices i ON i.id = l.invoice_id LEFT JOIN parts p ON p.id = l.part_id`+w.SQL()+
		" GROUP BY l.part_id ORDER BY SUM(l.line_total) DESC", w.Args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PartSales
	for rows.Next() {
		var s PartSales
		var pid *int64
		if err := rows.Scan(&pid, &s.Code, &s.Description, &s.Unit, &s.Qty, &s.Revenue, &s.Cost, &s.InStock); err != nil {
			return nil, err
		}
		if pid != nil {
			s.PartID = *pid
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// ChargeSales aggregates labour/extra charge lines on issued invoices by description.
func ChargeSales(ctx context.Context, q DBTX, from, to string) ([]StatusCount, error) {
	var w Where
	w.Add("i.status = 'Issued'")
	w.Add("l.line_type = 'charge'")
	if from != "" {
		w.Add("i.invoice_date >= ?", from)
	}
	if to != "" {
		w.Add("i.invoice_date <= ?", to)
	}
	rows, err := q.QueryContext(ctx, `SELECT l.description, COUNT(*), SUM(l.line_total) FROM invoice_lines l JOIN invoices i ON i.id = l.invoice_id`+
		w.SQL()+" GROUP BY l.description ORDER BY SUM(l.line_total) DESC LIMIT 50", w.Args...)
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

type SupplierTotal struct {
	SupplierID int64
	Name       string
	Count      int
	Total      models.Money
}

func PurchasesBySupplier(ctx context.Context, q DBTX, f PurchaseFilter) ([]SupplierTotal, error) {
	w := purchaseWhere(f)
	rows, err := q.QueryContext(ctx, `SELECT s.id, s.name, COUNT(*), COALESCE(SUM(pu.total), 0) FROM purchases pu JOIN suppliers s ON s.id = pu.supplier_id`+
		w.SQL()+" GROUP BY s.id ORDER BY SUM(pu.total) DESC", w.Args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SupplierTotal
	for rows.Next() {
		var s SupplierTotal
		if err := rows.Scan(&s.SupplierID, &s.Name, &s.Count, &s.Total); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

type MovementSummary struct {
	Type  string
	Count int
	In    models.Qty
	Out   models.Qty
}

func (m MovementSummary) Label() string { return models.MovementLabel(m.Type) }

func MovementsByType(ctx context.Context, q DBTX, f MovementFilter) ([]MovementSummary, error) {
	var w Where
	if f.PartID != 0 {
		w.Add("part_id = ?", f.PartID)
	}
	if f.Type != "" {
		w.Add("movement_type = ?", f.Type)
	}
	if f.From != "" {
		w.Add("created_at >= ?", f.From)
	}
	if f.To != "" {
		w.Add("created_at < ?", f.To)
	}
	rows, err := q.QueryContext(ctx, `SELECT movement_type, COUNT(*), COALESCE(SUM(CASE WHEN quantity > 0 THEN quantity END), 0),
		COALESCE(SUM(CASE WHEN quantity < 0 THEN -quantity END), 0) FROM stock_movements`+w.SQL()+" GROUP BY movement_type ORDER BY movement_type", w.Args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MovementSummary
	for rows.Next() {
		var m MovementSummary
		if err := rows.Scan(&m.Type, &m.Count, &m.In, &m.Out); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

type DashboardStats struct {
	ActiveJobs    int
	WaitingParts  int
	ToInvoice     int
	UnpaidCount   int
	Outstanding   models.Money
	TodayPayments models.Money
	TodayInvoiced models.Money
	LowStock      int
	ApptsToday    int // still to arrive today
	ServiceDue    int // vehicles due for service within a week (or overdue)
}

func Dashboard(ctx context.Context, q DBTX, today string, onlyUser int64) (DashboardStats, error) {
	var d DashboardStats
	var err error
	if d.ActiveJobs, err = CountJobs(ctx, q, JobFilter{Status: "active", OnlyUserID: onlyUser}); err != nil {
		return d, err
	}
	if d.WaitingParts, err = CountJobs(ctx, q, JobFilter{Status: models.JobWaiting, OnlyUserID: onlyUser}); err != nil {
		return d, err
	}
	if d.ToInvoice, err = CountJobs(ctx, q, JobFilter{Uninvoiced: true, OnlyUserID: onlyUser}); err != nil {
		return d, err
	}
	if err = q.QueryRowContext(ctx, "SELECT COUNT(*), COALESCE(SUM(total - amount_paid), 0) FROM invoices WHERE status = 'Issued' AND total > amount_paid").Scan(&d.UnpaidCount, &d.Outstanding); err != nil {
		return d, err
	}
	if err = q.QueryRowContext(ctx, "SELECT COALESCE(SUM(amount), 0) FROM payments WHERE status = 'Valid' AND payment_date = ?", today).Scan(&d.TodayPayments); err != nil {
		return d, err
	}
	if err = q.QueryRowContext(ctx, "SELECT COALESCE(SUM(total), 0) FROM invoices WHERE status = 'Issued' AND invoice_date = ?", today).Scan(&d.TodayInvoiced); err != nil {
		return d, err
	}
	if d.LowStock, err = count(ctx, q, "SELECT COUNT(*) FROM parts WHERE active = 1 AND qty_on_hand <= reorder_level"); err != nil {
		return d, err
	}
	if d.ApptsToday, err = CountAppointments(ctx, q, ApptFilter{From: today, To: today, Status: "open"}); err != nil {
		return d, err
	}
	week, _ := time.Parse(models.DateLayout, today)
	d.ServiceDue, err = count(ctx, q, `SELECT COUNT(*) FROM vehicles v JOIN customers c ON c.id = v.customer_id
		WHERE v.archived = 0 AND c.archived = 0 AND v.next_service_date IS NOT NULL AND v.next_service_date <= ?`,
		week.AddDate(0, 0, 7).Format(models.DateLayout))
	return d, err
}

type ServiceStat struct {
	Name    string
	Jobs    int
	Lines   int
	Qty     models.Qty
	Unit    string
	Revenue models.Money
}

// Average is the value per job, rounded half up.
func (s ServiceStat) Average() models.Money {
	if s.Jobs == 0 {
		return 0
	}
	n := models.Money(s.Jobs)
	return (s.Revenue + n/2) / n
}

// ServicesSold ranks labour/extra charge categories on jobs completed in the date range.
func ServicesSold(ctx context.Context, q DBTX, from, to string) ([]ServiceStat, error) {
	rows, err := q.QueryContext(ctx, `SELECT COALESCE(cc.name, 'Uncategorised'), COUNT(DISTINCT jc.job_id), COUNT(*), SUM(jc.quantity),
		COALESCE(MAX(cc.unit), MAX(jc.unit)), SUM(jc.line_total)
		FROM job_charges jc JOIN jobs j ON j.id = jc.job_id LEFT JOIN charge_categories cc ON cc.id = jc.category_id
		WHERE j.status = 'Completed' AND j.completed_date >= ? AND j.completed_date <= ?
		GROUP BY COALESCE(cc.name, 'Uncategorised') ORDER BY COUNT(DISTINCT jc.job_id) DESC, SUM(jc.line_total) DESC`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ServiceStat
	for rows.Next() {
		var s ServiceStat
		if err := rows.Scan(&s.Name, &s.Jobs, &s.Lines, &s.Qty, &s.Unit, &s.Revenue); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

type StaffStat struct {
	UserID        int64
	Name          string
	RoleName      string
	Active        bool
	OpenJobs      int          // currently open / in progress / waiting
	Completed     int          // jobs completed in range
	BilledHours   models.Qty   // charge quantity in hours on completed jobs
	LabourRevenue models.Money // charges on completed jobs
	PartsRevenue  models.Money // parts on completed jobs
	WorkedMin     int          // from timesheets
	ApptsToday    int
}

func (s StaffStat) Revenue() models.Money { return s.LabourRevenue + s.PartsRevenue }

// Utilisation is billed hours as a percentage of hours worked (0 when no time recorded).
func (s StaffStat) Utilisation() int {
	if s.WorkedMin <= 0 {
		return 0
	}
	return int((int64(s.BilledHours)*60/1000*100 + int64(s.WorkedMin)/2) / int64(s.WorkedMin))
}

// StaffStats builds workload and productivity figures per user for a completed-date range.
func StaffStats(ctx context.Context, q DBTX, from, to, today string) ([]StaffStat, error) {
	users, err := ListUsers(ctx, q, false)
	if err != nil {
		return nil, err
	}
	idx := map[int64]*StaffStat{}
	out := make([]StaffStat, len(users))
	for i, u := range users {
		out[i] = StaffStat{UserID: u.ID, Name: u.FullName, RoleName: u.RoleName, Active: u.Active}
		idx[u.ID] = &out[i]
	}
	scan := func(query string, args []any, fn func(*StaffStat, *sql.Rows) error) error {
		rows, err := q.QueryContext(ctx, query, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			// fn scans one row into a partial StaffStat that is added to the user's totals.
			s := &StaffStat{}
			if err := fn(s, rows); err != nil {
				return err
			}
			if t, ok := idx[s.UserID]; ok {
				t.OpenJobs += s.OpenJobs
				t.Completed += s.Completed
				t.BilledHours += s.BilledHours
				t.LabourRevenue += s.LabourRevenue
				t.PartsRevenue += s.PartsRevenue
				t.ApptsToday += s.ApptsToday
			}
		}
		return rows.Err()
	}
	if err := scan(`SELECT assigned_user_id, COUNT(*) FROM jobs WHERE assigned_user_id IS NOT NULL
		AND status IN ('Open','In Progress','Waiting for Parts') GROUP BY assigned_user_id`, nil,
		func(s *StaffStat, r *sql.Rows) error { return r.Scan(&s.UserID, &s.OpenJobs) }); err != nil {
		return nil, err
	}
	if err := scan(`SELECT j.assigned_user_id, COUNT(*),
		COALESCE(SUM((SELECT SUM(line_total) FROM job_charges WHERE job_id = j.id)), 0),
		COALESCE(SUM((SELECT SUM(line_total) FROM job_parts WHERE job_id = j.id)), 0),
		COALESCE(SUM((SELECT SUM(quantity) FROM job_charges WHERE job_id = j.id AND unit IN ('hour','hours','hr','hrs'))), 0)
		FROM jobs j WHERE j.assigned_user_id IS NOT NULL AND j.status = 'Completed' AND j.completed_date >= ? AND j.completed_date <= ?
		GROUP BY j.assigned_user_id`, []any{from, to},
		func(s *StaffStat, r *sql.Rows) error {
			return r.Scan(&s.UserID, &s.Completed, &s.LabourRevenue, &s.PartsRevenue, &s.BilledHours)
		}); err != nil {
		return nil, err
	}
	if err := scan(`SELECT assigned_user_id, COUNT(*) FROM appointments WHERE assigned_user_id IS NOT NULL
		AND appt_date = ? AND status IN ('Scheduled','Checked In') GROUP BY assigned_user_id`, []any{today},
		func(s *StaffStat, r *sql.Rows) error { return r.Scan(&s.UserID, &s.ApptsToday) }); err != nil {
		return nil, err
	}
	worked, err := WorkedMinutes(ctx, q, from, to)
	if err != nil {
		return nil, err
	}
	for id, m := range worked {
		if t, ok := idx[id]; ok {
			t.WorkedMin = m
		}
	}
	return out, nil
}

type ProfitSummary struct {
	Invoices    int
	Gross       models.Money // invoice totals incl. tax
	Tax         models.Money
	Discounts   models.Money
	NetSales    models.Money // excl. tax
	PartsCost   models.Money // cost of parts on those invoices
	GrossProfit models.Money
	Expenses    models.Money
	NetProfit   models.Money
	Received    models.Money // payments received in range
}

// Profit summarises issued invoices, parts cost and expenses in a date range.
func Profit(ctx context.Context, q DBTX, from, to string) (ProfitSummary, error) {
	var p ProfitSummary
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(total), 0), COALESCE(SUM(tax_amount), 0), COALESCE(SUM(discount), 0)
		FROM invoices WHERE status = 'Issued' AND invoice_date >= ? AND invoice_date <= ?`, from, to).
		Scan(&p.Invoices, &p.Gross, &p.Tax, &p.Discounts); err != nil {
		return p, err
	}
	if err := q.QueryRowContext(ctx, `SELECT COALESCE((SUM(l.quantity * l.unit_cost) + 500) / 1000, 0)
		FROM invoice_lines l JOIN invoices i ON i.id = l.invoice_id
		WHERE i.status = 'Issued' AND l.line_type = 'part' AND i.invoice_date >= ? AND i.invoice_date <= ?`, from, to).
		Scan(&p.PartsCost); err != nil {
		return p, err
	}
	if err := q.QueryRowContext(ctx, `SELECT COALESCE(SUM(amount), 0) FROM expenses WHERE status = 'Valid' AND expense_date >= ? AND expense_date <= ?`, from, to).
		Scan(&p.Expenses); err != nil {
		return p, err
	}
	if err := q.QueryRowContext(ctx, `SELECT COALESCE(SUM(amount), 0) FROM payments WHERE status = 'Valid' AND payment_date >= ? AND payment_date <= ?`, from, to).
		Scan(&p.Received); err != nil {
		return p, err
	}
	p.NetSales = p.Gross - p.Tax
	p.GrossProfit = p.NetSales - p.PartsCost
	p.NetProfit = p.GrossProfit - p.Expenses
	return p, nil
}
