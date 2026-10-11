package repository

import (
	"context"

	"garagems/internal/models"
)

// ---------- suppliers ----------

type SupplierFilter struct {
	Q      string
	Status string // "" active, "inactive", "all"
}

const supplierSelect = `SELECT id, name, contact_person, phone, email, address, notes, active, created_at, updated_at FROM suppliers`

func scanSupplier(sc interface{ Scan(...any) error }) (models.Supplier, error) {
	var s models.Supplier
	err := sc.Scan(&s.ID, &s.Name, &s.ContactPerson, &s.Phone, &s.Email, &s.Address, &s.Notes, &s.Active, &s.CreatedAt, &s.UpdatedAt)
	return s, err
}

func ListSuppliers(ctx context.Context, q DBTX, f SupplierFilter, o ListOpts) ([]models.Supplier, int, error) {
	var w Where
	if f.Q != "" {
		w.Add(`(name LIKE ? ESCAPE '\' OR contact_person LIKE ? ESCAPE '\' OR phone LIKE ? ESCAPE '\' OR email LIKE ? ESCAPE '\')`, Like(f.Q), Like(f.Q), Like(f.Q), Like(f.Q))
	}
	switch f.Status {
	case "inactive":
		w.Add("active = 0")
	case "all":
	default:
		w.Add("active = 1")
	}
	total, err := count(ctx, q, "SELECT COUNT(*) FROM suppliers"+w.SQL(), w.Args...)
	if err != nil {
		return nil, 0, err
	}
	rows, err := q.QueryContext(ctx, supplierSelect+w.SQL()+o.SQL(), w.Args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []models.Supplier
	for rows.Next() {
		s, err := scanSupplier(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, s)
	}
	return out, total, rows.Err()
}

func AllSuppliers(ctx context.Context, q DBTX, activeOnly bool) ([]models.Supplier, error) {
	st := "all"
	if activeOnly {
		st = ""
	}
	list, _, err := ListSuppliers(ctx, q, SupplierFilter{Status: st}, ListOpts{OrderBy: "name COLLATE NOCASE"})
	return list, err
}

func GetSupplier(ctx context.Context, q DBTX, id int64) (models.Supplier, error) {
	s, err := scanSupplier(q.QueryRowContext(ctx, supplierSelect+" WHERE id = ?", id))
	return s, notFound(err)
}

func CreateSupplier(ctx context.Context, q DBTX, s models.Supplier) (int64, error) {
	now := models.NowUTC()
	res, err := q.ExecContext(ctx, `INSERT INTO suppliers(name, contact_person, phone, email, address, notes, active, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, 1, ?, ?)`, s.Name, s.ContactPerson, s.Phone, s.Email, s.Address, s.Notes, now, now)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func UpdateSupplier(ctx context.Context, q DBTX, s models.Supplier) error {
	_, err := q.ExecContext(ctx, `UPDATE suppliers SET name = ?, contact_person = ?, phone = ?, email = ?, address = ?, notes = ?, updated_at = ? WHERE id = ?`,
		s.Name, s.ContactPerson, s.Phone, s.Email, s.Address, s.Notes, models.NowUTC(), s.ID)
	return err
}

func SetSupplierActive(ctx context.Context, q DBTX, id int64, active bool) error {
	_, err := q.ExecContext(ctx, "UPDATE suppliers SET active = ?, updated_at = ? WHERE id = ?", active, models.NowUTC(), id)
	return err
}

// ---------- purchases ----------

type PurchaseFilter struct {
	Q          string
	SupplierID int64
	Status     string
	From, To   string
	PartID     int64
}

const purchaseSelect = `SELECT pu.id, pu.purchase_number, pu.supplier_id, s.name, pu.purchase_date, pu.reference, pu.notes, pu.status, pu.total,
	COALESCE(pu.received_at, ''), COALESCE(ru.full_name, ''), COALESCE(pu.cancelled_at, ''), pu.cancel_reason, pu.created_at,
	(SELECT COUNT(*) FROM purchase_items pi WHERE pi.purchase_id = pu.id)
	FROM purchases pu JOIN suppliers s ON s.id = pu.supplier_id LEFT JOIN users ru ON ru.id = pu.received_by`

func scanPurchase(sc interface{ Scan(...any) error }) (models.Purchase, error) {
	var p models.Purchase
	err := sc.Scan(&p.ID, &p.PurchaseNumber, &p.SupplierID, &p.SupplierName, &p.PurchaseDate, &p.Reference, &p.Notes, &p.Status, &p.Total,
		&p.ReceivedAt, &p.ReceivedBy, &p.CancelledAt, &p.CancelReason, &p.CreatedAt, &p.ItemCount)
	return p, err
}

func purchaseWhere(f PurchaseFilter) Where {
	var w Where
	if f.Q != "" {
		w.Add(`(pu.purchase_number LIKE ? ESCAPE '\' OR pu.reference LIKE ? ESCAPE '\' OR s.name LIKE ? ESCAPE '\')`, Like(f.Q), Like(f.Q), Like(f.Q))
	}
	if f.SupplierID != 0 {
		w.Add("pu.supplier_id = ?", f.SupplierID)
	}
	if f.Status != "" {
		w.Add("pu.status = ?", f.Status)
	}
	if f.From != "" {
		w.Add("pu.purchase_date >= ?", f.From)
	}
	if f.To != "" {
		w.Add("pu.purchase_date <= ?", f.To)
	}
	if f.PartID != 0 {
		w.Add("EXISTS (SELECT 1 FROM purchase_items pi WHERE pi.purchase_id = pu.id AND pi.part_id = ?)", f.PartID)
	}
	return w
}

func ListPurchases(ctx context.Context, q DBTX, f PurchaseFilter, o ListOpts) ([]models.Purchase, int, models.Money, error) {
	w := purchaseWhere(f)
	var total int
	var sum models.Money
	err := q.QueryRowContext(ctx, "SELECT COUNT(*), COALESCE(SUM(pu.total), 0) FROM purchases pu JOIN suppliers s ON s.id = pu.supplier_id"+w.SQL(), w.Args...).Scan(&total, &sum)
	if err != nil {
		return nil, 0, 0, err
	}
	rows, err := q.QueryContext(ctx, purchaseSelect+w.SQL()+o.SQL(), w.Args...)
	if err != nil {
		return nil, 0, 0, err
	}
	defer rows.Close()
	var out []models.Purchase
	for rows.Next() {
		p, err := scanPurchase(rows)
		if err != nil {
			return nil, 0, 0, err
		}
		out = append(out, p)
	}
	return out, total, sum, rows.Err()
}

func GetPurchase(ctx context.Context, q DBTX, id int64) (models.Purchase, error) {
	p, err := scanPurchase(q.QueryRowContext(ctx, purchaseSelect+" WHERE pu.id = ?", id))
	return p, notFound(err)
}

func PurchaseItems(ctx context.Context, q DBTX, purchaseID int64) ([]models.PurchaseItem, error) {
	rows, err := q.QueryContext(ctx, `SELECT pi.id, pi.purchase_id, pi.part_id, p.code, p.name, p.unit, pi.quantity, pi.unit_cost, pi.line_total
		FROM purchase_items pi JOIN parts p ON p.id = pi.part_id WHERE pi.purchase_id = ? ORDER BY pi.id`, purchaseID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.PurchaseItem
	for rows.Next() {
		var it models.PurchaseItem
		if err := rows.Scan(&it.ID, &it.PurchaseID, &it.PartID, &it.PartCode, &it.PartName, &it.Unit, &it.Quantity, &it.UnitCost, &it.LineTotal); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

func CreatePurchase(ctx context.Context, q DBTX, p models.Purchase, userID int64) (int64, error) {
	now := models.NowUTC()
	res, err := q.ExecContext(ctx, `INSERT INTO purchases(purchase_number, supplier_id, purchase_date, reference, notes, status, total, created_by, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, 'Draft', 0, ?, ?, ?)`, p.PurchaseNumber, p.SupplierID, p.PurchaseDate, p.Reference, p.Notes, userID, now, now)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func UpdatePurchaseHeader(ctx context.Context, q DBTX, p models.Purchase) error {
	_, err := q.ExecContext(ctx, `UPDATE purchases SET supplier_id = ?, purchase_date = ?, reference = ?, notes = ?, updated_at = ? WHERE id = ? AND status = 'Draft'`,
		p.SupplierID, p.PurchaseDate, p.Reference, p.Notes, models.NowUTC(), p.ID)
	return err
}

func InsertPurchaseItem(ctx context.Context, q DBTX, it models.PurchaseItem) error {
	_, err := q.ExecContext(ctx, "INSERT INTO purchase_items(purchase_id, part_id, quantity, unit_cost, line_total) VALUES (?, ?, ?, ?, ?)",
		it.PurchaseID, it.PartID, it.Quantity, it.UnitCost, it.LineTotal)
	return err
}

func DeletePurchaseItem(ctx context.Context, q DBTX, purchaseID, itemID int64) error {
	_, err := q.ExecContext(ctx, "DELETE FROM purchase_items WHERE id = ? AND purchase_id = ?", itemID, purchaseID)
	return err
}

func RecalcPurchaseTotal(ctx context.Context, q DBTX, purchaseID int64) error {
	_, err := q.ExecContext(ctx, `UPDATE purchases SET total = (SELECT COALESCE(SUM(line_total), 0) FROM purchase_items WHERE purchase_id = ?), updated_at = ? WHERE id = ?`,
		purchaseID, models.NowUTC(), purchaseID)
	return err
}
