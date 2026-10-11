package repository

import (
	"context"
	"database/sql"

	"garagems/internal/models"
)

type PartFilter struct {
	Q        string
	Category string
	LowOnly  bool
	Status   string // "" active, "inactive", "all"
}

const partSelect = `SELECT id, code, name, description, category, compatible, unit, sale_price, purchase_cost, qty_on_hand, reorder_level, location, active, created_at, updated_at FROM parts`

func scanPart(sc interface{ Scan(...any) error }) (models.Part, error) {
	var p models.Part
	err := sc.Scan(&p.ID, &p.Code, &p.Name, &p.Description, &p.Category, &p.Compatible, &p.Unit, &p.SalePrice, &p.PurchaseCost,
		&p.QtyOnHand, &p.ReorderLevel, &p.Location, &p.Active, &p.CreatedAt, &p.UpdatedAt)
	return p, err
}

func partWhere(f PartFilter) Where {
	var w Where
	if f.Q != "" {
		w.Add(`(code LIKE ? ESCAPE '\' OR name LIKE ? ESCAPE '\' OR description LIKE ? ESCAPE '\' OR compatible LIKE ? ESCAPE '\' OR location LIKE ? ESCAPE '\')`,
			Like(f.Q), Like(f.Q), Like(f.Q), Like(f.Q), Like(f.Q))
	}
	if f.Category != "" {
		w.Add("category = ?", f.Category)
	}
	if f.LowOnly {
		w.Add("qty_on_hand <= reorder_level")
	}
	switch f.Status {
	case "inactive":
		w.Add("active = 0")
	case "all":
	default:
		w.Add("active = 1")
	}
	return w
}

func ListParts(ctx context.Context, q DBTX, f PartFilter, o ListOpts) ([]models.Part, int, error) {
	w := partWhere(f)
	total, err := count(ctx, q, "SELECT COUNT(*) FROM parts"+w.SQL(), w.Args...)
	if err != nil {
		return nil, 0, err
	}
	rows, err := q.QueryContext(ctx, partSelect+w.SQL()+o.SQL(), w.Args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []models.Part
	for rows.Next() {
		p, err := scanPart(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, p)
	}
	return out, total, rows.Err()
}

// StockValue returns totals for the filtered parts (quantity * purchase cost, quantity * sale price).
func StockValue(ctx context.Context, q DBTX, f PartFilter) (cost, sale models.Money, err error) {
	w := partWhere(f)
	err = q.QueryRowContext(ctx, `SELECT COALESCE(SUM(MAX(qty_on_hand,0) * purchase_cost), 0) / 1000, COALESCE(SUM(MAX(qty_on_hand,0) * sale_price), 0) / 1000 FROM parts`+w.SQL(), w.Args...).Scan(&cost, &sale)
	return
}

func GetPart(ctx context.Context, q DBTX, id int64) (models.Part, error) {
	p, err := scanPart(q.QueryRowContext(ctx, partSelect+" WHERE id = ?", id))
	return p, notFound(err)
}

func GetPartByCode(ctx context.Context, q DBTX, code string) (models.Part, error) {
	p, err := scanPart(q.QueryRowContext(ctx, partSelect+" WHERE code = ?", code))
	return p, notFound(err)
}

func PartCodeTaken(ctx context.Context, q DBTX, code string, exceptID int64) (bool, error) {
	n, err := count(ctx, q, "SELECT COUNT(*) FROM parts WHERE code = ? AND id <> ?", code, exceptID)
	return n > 0, err
}

func CreatePart(ctx context.Context, q DBTX, p models.Part) (int64, error) {
	now := models.NowUTC()
	res, err := q.ExecContext(ctx, `INSERT INTO parts(code, name, description, category, compatible, unit, sale_price, purchase_cost, qty_on_hand, reorder_level, location, active, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?, 1, ?, ?)`,
		p.Code, p.Name, p.Description, p.Category, p.Compatible, p.Unit, p.SalePrice, p.PurchaseCost, p.ReorderLevel, p.Location, now, now)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// UpdatePart edits catalogue fields. Stock quantity is never changed here.
func UpdatePart(ctx context.Context, q DBTX, p models.Part) error {
	_, err := q.ExecContext(ctx, `UPDATE parts SET code = ?, name = ?, description = ?, category = ?, compatible = ?, unit = ?, sale_price = ?,
		purchase_cost = ?, reorder_level = ?, location = ?, updated_at = ? WHERE id = ?`,
		p.Code, p.Name, p.Description, p.Category, p.Compatible, p.Unit, p.SalePrice, p.PurchaseCost, p.ReorderLevel, p.Location, models.NowUTC(), p.ID)
	return err
}

func SetPartActive(ctx context.Context, q DBTX, id int64, active bool) error {
	_, err := q.ExecContext(ctx, "UPDATE parts SET active = ?, updated_at = ? WHERE id = ?", active, models.NowUTC(), id)
	return err
}

func PartCategories(ctx context.Context, q DBTX) ([]string, error) {
	rows, err := q.QueryContext(ctx, "SELECT DISTINCT category FROM parts WHERE category <> '' ORDER BY category COLLATE NOCASE")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func ActivePartOptions(ctx context.Context, q DBTX) ([]models.Part, error) {
	list, _, err := ListParts(ctx, q, PartFilter{}, ListOpts{OrderBy: "code COLLATE NOCASE", Limit: 5000})
	return list, err
}

// ---------- stock movements ----------

type MovementFilter struct {
	PartID     int64
	JobID      int64
	PurchaseID int64
	Type       string
	From, To   string // UTC timestamps (inclusive from, exclusive to)
	Q          string
}

func InsertMovement(ctx context.Context, q DBTX, m models.StockMovement, userID int64) error {
	_, err := q.ExecContext(ctx, `INSERT INTO stock_movements(part_id, movement_type, quantity, balance_after, unit_cost, job_id, purchase_id, reason, negative_override, user_id, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.PartID, m.Type, m.Quantity, m.BalanceAfter, m.UnitCost, m.JobID, m.PurchaseID, m.Reason, m.NegativeOverride, userID, models.NowUTC())
	return err
}

func ListMovements(ctx context.Context, q DBTX, f MovementFilter, o ListOpts) ([]models.StockMovement, int, error) {
	var w Where
	if f.PartID != 0 {
		w.Add("m.part_id = ?", f.PartID)
	}
	if f.JobID != 0 {
		w.Add("m.job_id = ?", f.JobID)
	}
	if f.PurchaseID != 0 {
		w.Add("m.purchase_id = ?", f.PurchaseID)
	}
	if f.Type != "" {
		w.Add("m.movement_type = ?", f.Type)
	}
	if f.From != "" {
		w.Add("m.created_at >= ?", f.From)
	}
	if f.To != "" {
		w.Add("m.created_at < ?", f.To)
	}
	if f.Q != "" {
		w.Add(`(p.code LIKE ? ESCAPE '\' OR p.name LIKE ? ESCAPE '\' OR m.reason LIKE ? ESCAPE '\')`, Like(f.Q), Like(f.Q), Like(f.Q))
	}
	from := ` FROM stock_movements m JOIN parts p ON p.id = m.part_id JOIN users u ON u.id = m.user_id
		LEFT JOIN jobs j ON j.id = m.job_id LEFT JOIN purchases pu ON pu.id = m.purchase_id`
	total, err := count(ctx, q, "SELECT COUNT(*)"+from+w.SQL(), w.Args...)
	if err != nil {
		return nil, 0, err
	}
	rows, err := q.QueryContext(ctx, `SELECT m.id, m.part_id, p.code, p.name, m.movement_type, m.quantity, m.balance_after, m.unit_cost,
		m.job_id, COALESCE(j.job_number, ''), m.purchase_id, COALESCE(pu.purchase_number, ''), m.reason, m.negative_override, u.full_name, m.created_at`+
		from+w.SQL()+o.SQL(), w.Args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []models.StockMovement
	for rows.Next() {
		var m models.StockMovement
		if err := rows.Scan(&m.ID, &m.PartID, &m.PartCode, &m.PartName, &m.Type, &m.Quantity, &m.BalanceAfter, &m.UnitCost,
			&m.JobID, &m.JobNumber, &m.PurchaseID, &m.PurchaseNumber, &m.Reason, &m.NegativeOverride, &m.UserName, &m.CreatedAt); err != nil {
			return nil, 0, err
		}
		out = append(out, m)
	}
	return out, total, rows.Err()
}

var _ = sql.ErrNoRows
