package repository

import (
	"context"
	"database/sql"

	"garagems/internal/models"
)

type JobFilter struct {
	Q          string
	Status     string // a status, "active" (not completed/cancelled) or ""
	From, To   string // job_date range, inclusive
	AssignedID int64
	OnlyUserID int64 // restricts to jobs assigned to this user (mechanics)
	CustomerID int64
	VehicleID  int64
	Uninvoiced bool // completed with no issued invoice
}

const jobSelect = `SELECT j.id, j.job_number, j.customer_id, c.name, c.phone, j.vehicle_id,
	COALESCE(v.registration, ''), COALESCE(TRIM(v.make || ' ' || v.model), ''),
	j.job_date, j.odometer, j.reported_issue, j.inspection_notes, j.work_performed, j.internal_notes,
	j.assigned_user_id, COALESCE(u.full_name, ''), j.status, COALESCE(j.completed_date, ''), j.created_by, j.created_at, j.updated_at,
	(SELECT COALESCE(SUM(line_total), 0) FROM job_charges WHERE job_id = j.id),
	(SELECT COALESCE(SUM(line_total), 0) FROM job_parts WHERE job_id = j.id),
	(SELECT id FROM invoices WHERE job_id = j.id AND status = 'Issued'),
	COALESCE((SELECT invoice_number FROM invoices WHERE job_id = j.id AND status = 'Issued'), '')
	FROM jobs j
	JOIN customers c ON c.id = j.customer_id
	LEFT JOIN vehicles v ON v.id = j.vehicle_id
	LEFT JOIN users u ON u.id = j.assigned_user_id`

const jobFrom = ` FROM jobs j JOIN customers c ON c.id = j.customer_id LEFT JOIN vehicles v ON v.id = j.vehicle_id LEFT JOIN users u ON u.id = j.assigned_user_id`

func scanJob(sc interface{ Scan(...any) error }) (models.Job, error) {
	var j models.Job
	err := sc.Scan(&j.ID, &j.JobNumber, &j.CustomerID, &j.CustomerName, &j.CustomerPhone, &j.VehicleID, &j.VehicleReg, &j.VehicleDesc,
		&j.JobDate, &j.Odometer, &j.ReportedIssue, &j.InspectionNotes, &j.WorkPerformed, &j.InternalNotes,
		&j.AssignedUserID, &j.AssignedName, &j.Status, &j.CompletedDate, &j.CreatedBy, &j.CreatedAt, &j.UpdatedAt,
		&j.ChargesTotal, &j.PartsTotal, &j.InvoiceID, &j.InvoiceNo)
	return j, err
}

func jobWhere(f JobFilter) Where {
	var w Where
	if f.Q != "" {
		cond := `(j.job_number LIKE ? ESCAPE '\' OR c.name LIKE ? ESCAPE '\' OR j.reported_issue LIKE ? ESCAPE '\' OR v.make LIKE ? ESCAPE '\' OR v.model LIKE ? ESCAPE '\'`
		args := []any{Like(f.Q), Like(f.Q), Like(f.Q), Like(f.Q), Like(f.Q)}
		if reg := models.NormalizeReg(f.Q); reg != "" {
			cond += ` OR v.registration_norm LIKE ? ESCAPE '\'`
			args = append(args, Like(reg))
		}
		if d := models.DigitsOnly(f.Q); d != "" {
			cond += ` OR c.phone_norm LIKE ? ESCAPE '\'`
			args = append(args, Like(d))
		}
		w.Add(cond+")", args...)
	}
	switch f.Status {
	case "":
	case "active":
		w.Add("j.status IN ('Open','In Progress','Waiting for Parts')")
	default:
		w.Add("j.status = ?", f.Status)
	}
	if f.From != "" {
		w.Add("j.job_date >= ?", f.From)
	}
	if f.To != "" {
		w.Add("j.job_date <= ?", f.To)
	}
	if f.AssignedID != 0 {
		w.Add("j.assigned_user_id = ?", f.AssignedID)
	}
	if f.OnlyUserID != 0 {
		w.Add("j.assigned_user_id = ?", f.OnlyUserID)
	}
	if f.CustomerID != 0 {
		w.Add("j.customer_id = ?", f.CustomerID)
	}
	if f.VehicleID != 0 {
		w.Add("j.vehicle_id = ?", f.VehicleID)
	}
	if f.Uninvoiced {
		w.Add("j.status = 'Completed' AND NOT EXISTS (SELECT 1 FROM invoices i WHERE i.job_id = j.id AND i.status = 'Issued')")
	}
	return w
}

func ListJobs(ctx context.Context, q DBTX, f JobFilter, o ListOpts) ([]models.Job, int, error) {
	w := jobWhere(f)
	total, err := count(ctx, q, "SELECT COUNT(*)"+jobFrom+w.SQL(), w.Args...)
	if err != nil {
		return nil, 0, err
	}
	rows, err := q.QueryContext(ctx, jobSelect+w.SQL()+o.SQL(), w.Args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []models.Job
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, j)
	}
	return out, total, rows.Err()
}

func CountJobs(ctx context.Context, q DBTX, f JobFilter) (int, error) {
	w := jobWhere(f)
	return count(ctx, q, "SELECT COUNT(*)"+jobFrom+w.SQL(), w.Args...)
}

func GetJob(ctx context.Context, q DBTX, id int64) (models.Job, error) {
	j, err := scanJob(q.QueryRowContext(ctx, jobSelect+" WHERE j.id = ?", id))
	return j, notFound(err)
}

func CreateJob(ctx context.Context, q DBTX, j models.Job) (int64, error) {
	now := models.NowUTC()
	res, err := q.ExecContext(ctx, `INSERT INTO jobs(job_number, customer_id, vehicle_id, job_date, odometer, reported_issue, inspection_notes,
		work_performed, internal_notes, assigned_user_id, status, created_by, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'Open', ?, ?, ?)`,
		j.JobNumber, j.CustomerID, j.VehicleID, j.JobDate, j.Odometer, j.ReportedIssue, j.InspectionNotes, j.WorkPerformed, j.InternalNotes,
		j.AssignedUserID, j.CreatedBy, now, now)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func UpdateJobDetails(ctx context.Context, q DBTX, j models.Job) error {
	_, err := q.ExecContext(ctx, `UPDATE jobs SET customer_id = ?, vehicle_id = ?, job_date = ?, odometer = ?, reported_issue = ?,
		assigned_user_id = ?, inspection_notes = ?, work_performed = ?, internal_notes = ?, updated_at = ? WHERE id = ?`,
		j.CustomerID, j.VehicleID, j.JobDate, j.Odometer, j.ReportedIssue, j.AssignedUserID,
		j.InspectionNotes, j.WorkPerformed, j.InternalNotes, models.NowUTC(), j.ID)
	return err
}

func UpdateJobNotes(ctx context.Context, q DBTX, j models.Job) error {
	_, err := q.ExecContext(ctx, `UPDATE jobs SET odometer = ?, inspection_notes = ?, work_performed = ?, internal_notes = ?, updated_at = ? WHERE id = ?`,
		j.Odometer, j.InspectionNotes, j.WorkPerformed, j.InternalNotes, models.NowUTC(), j.ID)
	return err
}

func SetJobStatus(ctx context.Context, q DBTX, id int64, status string, completedDate sql.NullString) error {
	_, err := q.ExecContext(ctx, "UPDATE jobs SET status = ?, completed_date = ?, updated_at = ? WHERE id = ?", status, completedDate, models.NowUTC(), id)
	return err
}

// ---------- job charges ----------

func JobCharges(ctx context.Context, q DBTX, jobID int64) ([]models.JobCharge, error) {
	rows, err := q.QueryContext(ctx, `SELECT jc.id, jc.job_id, jc.category_id, COALESCE(cc.name, ''), jc.description, jc.quantity, jc.unit,
		jc.unit_price, jc.line_total, jc.price_overridden, jc.notes, jc.created_at
		FROM job_charges jc LEFT JOIN charge_categories cc ON cc.id = jc.category_id WHERE jc.job_id = ? ORDER BY jc.id`, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.JobCharge
	for rows.Next() {
		var c models.JobCharge
		if err := rows.Scan(&c.ID, &c.JobID, &c.CategoryID, &c.CategoryName, &c.Description, &c.Quantity, &c.Unit,
			&c.UnitPrice, &c.LineTotal, &c.PriceOverridden, &c.Notes, &c.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func GetJobCharge(ctx context.Context, q DBTX, jobID, id int64) (models.JobCharge, error) {
	var c models.JobCharge
	err := q.QueryRowContext(ctx, `SELECT id, job_id, category_id, description, quantity, unit, unit_price, line_total, price_overridden, notes, created_at
		FROM job_charges WHERE id = ? AND job_id = ?`, id, jobID).
		Scan(&c.ID, &c.JobID, &c.CategoryID, &c.Description, &c.Quantity, &c.Unit, &c.UnitPrice, &c.LineTotal, &c.PriceOverridden, &c.Notes, &c.CreatedAt)
	return c, notFound(err)
}

func InsertJobCharge(ctx context.Context, q DBTX, c models.JobCharge, userID int64) (int64, error) {
	now := models.NowUTC()
	res, err := q.ExecContext(ctx, `INSERT INTO job_charges(job_id, category_id, description, quantity, unit, unit_price, line_total, price_overridden, notes, created_by, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.JobID, c.CategoryID, c.Description, c.Quantity, c.Unit, c.UnitPrice, c.LineTotal, c.PriceOverridden, c.Notes, userID, now, now)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func UpdateJobCharge(ctx context.Context, q DBTX, c models.JobCharge) error {
	_, err := q.ExecContext(ctx, `UPDATE job_charges SET description = ?, quantity = ?, unit_price = ?, line_total = ?, price_overridden = ?, notes = ?, updated_at = ?
		WHERE id = ? AND job_id = ?`, c.Description, c.Quantity, c.UnitPrice, c.LineTotal, c.PriceOverridden, c.Notes, models.NowUTC(), c.ID, c.JobID)
	return err
}

func DeleteJobCharge(ctx context.Context, q DBTX, jobID, id int64) error {
	_, err := q.ExecContext(ctx, "DELETE FROM job_charges WHERE id = ? AND job_id = ?", id, jobID)
	return err
}

// ---------- job parts ----------

const jobPartSelect = `SELECT jp.id, jp.job_id, jp.part_id, p.code, jp.description, p.unit, jp.quantity, jp.unit_price, jp.unit_cost,
	jp.line_total, jp.price_overridden, jp.notes, jp.created_at, p.qty_on_hand
	FROM job_parts jp JOIN parts p ON p.id = jp.part_id`

func scanJobPart(sc interface{ Scan(...any) error }) (models.JobPart, error) {
	var p models.JobPart
	err := sc.Scan(&p.ID, &p.JobID, &p.PartID, &p.PartCode, &p.Description, &p.Unit, &p.Quantity, &p.UnitPrice, &p.UnitCost,
		&p.LineTotal, &p.PriceOverridden, &p.Notes, &p.CreatedAt, &p.InStock)
	return p, err
}

func JobParts(ctx context.Context, q DBTX, jobID int64) ([]models.JobPart, error) {
	rows, err := q.QueryContext(ctx, jobPartSelect+" WHERE jp.job_id = ? ORDER BY jp.id", jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.JobPart
	for rows.Next() {
		p, err := scanJobPart(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func GetJobPart(ctx context.Context, q DBTX, jobID, id int64) (models.JobPart, error) {
	p, err := scanJobPart(q.QueryRowContext(ctx, jobPartSelect+" WHERE jp.id = ? AND jp.job_id = ?", id, jobID))
	return p, notFound(err)
}

func InsertJobPart(ctx context.Context, q DBTX, p models.JobPart, userID int64) (int64, error) {
	now := models.NowUTC()
	res, err := q.ExecContext(ctx, `INSERT INTO job_parts(job_id, part_id, description, quantity, unit_price, unit_cost, line_total, price_overridden, notes, created_by, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.JobID, p.PartID, p.Description, p.Quantity, p.UnitPrice, p.UnitCost, p.LineTotal, p.PriceOverridden, p.Notes, userID, now, now)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func UpdateJobPart(ctx context.Context, q DBTX, p models.JobPart) error {
	_, err := q.ExecContext(ctx, `UPDATE job_parts SET quantity = ?, unit_price = ?, line_total = ?, price_overridden = ?, notes = ?, updated_at = ?
		WHERE id = ? AND job_id = ?`, p.Quantity, p.UnitPrice, p.LineTotal, p.PriceOverridden, p.Notes, models.NowUTC(), p.ID, p.JobID)
	return err
}

func DeleteJobPart(ctx context.Context, q DBTX, jobID, id int64) error {
	_, err := q.ExecContext(ctx, "DELETE FROM job_parts WHERE id = ? AND job_id = ?", id, jobID)
	return err
}

// ---------- charge categories ----------

func ListChargeCategories(ctx context.Context, q DBTX, activeOnly bool) ([]models.ChargeCategory, error) {
	query := "SELECT id, name, default_price, unit, manual_price, is_custom, active, sort_order FROM charge_categories"
	if activeOnly {
		query += " WHERE active = 1"
	}
	rows, err := q.QueryContext(ctx, query+" ORDER BY sort_order, id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.ChargeCategory
	for rows.Next() {
		var c models.ChargeCategory
		if err := rows.Scan(&c.ID, &c.Name, &c.DefaultPrice, &c.Unit, &c.ManualPrice, &c.IsCustom, &c.Active, &c.SortOrder); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func GetChargeCategory(ctx context.Context, q DBTX, id int64) (models.ChargeCategory, error) {
	var c models.ChargeCategory
	err := q.QueryRowContext(ctx, "SELECT id, name, default_price, unit, manual_price, is_custom, active, sort_order FROM charge_categories WHERE id = ?", id).
		Scan(&c.ID, &c.Name, &c.DefaultPrice, &c.Unit, &c.ManualPrice, &c.IsCustom, &c.Active, &c.SortOrder)
	return c, notFound(err)
}

func CreateChargeCategory(ctx context.Context, q DBTX, c models.ChargeCategory) (int64, error) {
	now := models.NowUTC()
	res, err := q.ExecContext(ctx, `INSERT INTO charge_categories(name, default_price, unit, manual_price, is_custom, active, sort_order, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, 1, (SELECT COALESCE(MAX(sort_order), 0) + 10 FROM charge_categories), ?, ?)`,
		c.Name, c.DefaultPrice, c.Unit, c.ManualPrice, c.IsCustom, now, now)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func UpdateChargeCategory(ctx context.Context, q DBTX, c models.ChargeCategory) error {
	_, err := q.ExecContext(ctx, `UPDATE charge_categories SET name = ?, default_price = ?, unit = ?, manual_price = ?, is_custom = ?, active = ?, updated_at = ? WHERE id = ?`,
		c.Name, c.DefaultPrice, c.Unit, c.ManualPrice, c.IsCustom, c.Active, models.NowUTC(), c.ID)
	return err
}

func SetChargeCategoryOrder(ctx context.Context, q DBTX, id int64, order int) error {
	_, err := q.ExecContext(ctx, "UPDATE charge_categories SET sort_order = ?, updated_at = ? WHERE id = ?", order, models.NowUTC(), id)
	return err
}
