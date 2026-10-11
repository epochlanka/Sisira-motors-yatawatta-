package repository

import (
	"context"

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
	d.LowStock, err = count(ctx, q, "SELECT COUNT(*) FROM parts WHERE active = 1 AND qty_on_hand <= reorder_level")
	return d, err
}
