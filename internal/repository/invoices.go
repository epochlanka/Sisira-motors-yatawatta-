package repository

import (
	"context"

	"garagems/internal/models"
)

type InvoiceFilter struct {
	Q             string
	PaymentStatus string // Unpaid, Partially Paid, Paid, "outstanding"
	Status        string // Issued (default ""), Void, all
	From, To      string
	CustomerID    int64
	JobID         int64
}

const invoiceSelect = `SELECT i.id, i.invoice_number, i.job_id, j.job_number, i.customer_id, i.vehicle_id, i.invoice_date,
	i.customer_name, i.customer_phone, i.customer_address, i.customer_email, i.vehicle_reg, i.vehicle_desc, i.odometer,
	i.subtotal, i.discount, i.discount_note, i.tax_name, i.tax_rate_bp, i.tax_amount, i.total, i.amount_paid, i.payment_status,
	i.status, i.void_reason, COALESCE(i.voided_at, ''), COALESCE(vu.full_name, ''), i.replaces_invoice_id,
	COALESCE((SELECT invoice_number FROM invoices r WHERE r.id = i.replaces_invoice_id), ''), i.notes, cu.full_name, i.created_at
	FROM invoices i JOIN jobs j ON j.id = i.job_id JOIN users cu ON cu.id = i.created_by LEFT JOIN users vu ON vu.id = i.voided_by`

func scanInvoice(sc interface{ Scan(...any) error }) (models.Invoice, error) {
	var i models.Invoice
	err := sc.Scan(&i.ID, &i.InvoiceNumber, &i.JobID, &i.JobNumber, &i.CustomerID, &i.VehicleID, &i.InvoiceDate,
		&i.CustomerName, &i.CustomerPhone, &i.CustomerAddress, &i.CustomerEmail, &i.VehicleReg, &i.VehicleDesc, &i.Odometer,
		&i.Subtotal, &i.Discount, &i.DiscountNote, &i.TaxName, &i.TaxRateBP, &i.TaxAmount, &i.Total, &i.AmountPaid, &i.PaymentStatus,
		&i.Status, &i.VoidReason, &i.VoidedAt, &i.VoidedBy, &i.ReplacesID, &i.ReplacesNumber, &i.Notes, &i.CreatedBy, &i.CreatedAt)
	return i, err
}

func invoiceWhere(f InvoiceFilter) Where {
	var w Where
	if f.Q != "" {
		cond := `(i.invoice_number LIKE ? ESCAPE '\' OR i.customer_name LIKE ? ESCAPE '\' OR j.job_number LIKE ? ESCAPE '\'`
		args := []any{Like(f.Q), Like(f.Q), Like(f.Q)}
		if reg := models.NormalizeReg(f.Q); reg != "" {
			cond += ` OR REPLACE(REPLACE(UPPER(i.vehicle_reg), ' ', ''), '-', '') LIKE ? ESCAPE '\'`
			args = append(args, Like(reg))
		}
		if d := models.DigitsOnly(f.Q); d != "" {
			cond += ` OR REPLACE(REPLACE(REPLACE(i.customer_phone, ' ', ''), '-', ''), '+', '') LIKE ? ESCAPE '\'`
			args = append(args, Like(d))
		}
		w.Add(cond+")", args...)
	}
	switch f.Status {
	case "all":
	case models.InvoiceVoid:
		w.Add("i.status = 'Void'")
	default:
		w.Add("i.status = 'Issued'")
	}
	switch f.PaymentStatus {
	case "":
	case "outstanding":
		w.Add("i.total > i.amount_paid")
	default:
		w.Add("i.payment_status = ?", f.PaymentStatus)
	}
	if f.From != "" {
		w.Add("i.invoice_date >= ?", f.From)
	}
	if f.To != "" {
		w.Add("i.invoice_date <= ?", f.To)
	}
	if f.CustomerID != 0 {
		w.Add("i.customer_id = ?", f.CustomerID)
	}
	if f.JobID != 0 {
		w.Add("i.job_id = ?", f.JobID)
	}
	return w
}

type InvoiceTotals struct {
	Count                                int
	Subtotal, Discount, Tax, Total, Paid models.Money
}

func (t InvoiceTotals) Balance() models.Money { return t.Total - t.Paid }

func ListInvoices(ctx context.Context, q DBTX, f InvoiceFilter, o ListOpts) ([]models.Invoice, InvoiceTotals, error) {
	w := invoiceWhere(f)
	var t InvoiceTotals
	err := q.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(i.subtotal),0), COALESCE(SUM(i.discount),0), COALESCE(SUM(i.tax_amount),0),
		COALESCE(SUM(i.total),0), COALESCE(SUM(i.amount_paid),0) FROM invoices i JOIN jobs j ON j.id = i.job_id`+w.SQL(), w.Args...).
		Scan(&t.Count, &t.Subtotal, &t.Discount, &t.Tax, &t.Total, &t.Paid)
	if err != nil {
		return nil, t, err
	}
	rows, err := q.QueryContext(ctx, invoiceSelect+w.SQL()+o.SQL(), w.Args...)
	if err != nil {
		return nil, t, err
	}
	defer rows.Close()
	var out []models.Invoice
	for rows.Next() {
		i, err := scanInvoice(rows)
		if err != nil {
			return nil, t, err
		}
		out = append(out, i)
	}
	return out, t, rows.Err()
}

func GetInvoice(ctx context.Context, q DBTX, id int64) (models.Invoice, error) {
	i, err := scanInvoice(q.QueryRowContext(ctx, invoiceSelect+" WHERE i.id = ?", id))
	return i, notFound(err)
}

func InvoiceLines(ctx context.Context, q DBTX, invoiceID int64) ([]models.InvoiceLine, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, invoice_id, line_type, part_id, part_code, description, notes, quantity, unit, unit_price, unit_cost, line_total, sort_order
		FROM invoice_lines WHERE invoice_id = ? ORDER BY sort_order, id`, invoiceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.InvoiceLine
	for rows.Next() {
		var l models.InvoiceLine
		if err := rows.Scan(&l.ID, &l.InvoiceID, &l.LineType, &l.PartID, &l.PartCode, &l.Description, &l.Notes, &l.Quantity, &l.Unit,
			&l.UnitPrice, &l.UnitCost, &l.LineTotal, &l.SortOrder); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func InsertInvoice(ctx context.Context, q DBTX, i models.Invoice, userID int64) (int64, error) {
	res, err := q.ExecContext(ctx, `INSERT INTO invoices(invoice_number, job_id, customer_id, vehicle_id, invoice_date, customer_name, customer_phone,
		customer_email, customer_address, vehicle_reg, vehicle_desc, odometer, subtotal, discount, discount_note, tax_name, tax_rate_bp, tax_amount,
		total, amount_paid, payment_status, status, replaces_invoice_id, notes, created_by, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, 'Issued', ?, ?, ?, ?)`,
		i.InvoiceNumber, i.JobID, i.CustomerID, i.VehicleID, i.InvoiceDate, i.CustomerName, i.CustomerPhone,
		i.CustomerEmail, i.CustomerAddress, i.VehicleReg, i.VehicleDesc, i.Odometer, i.Subtotal, i.Discount, i.DiscountNote, i.TaxName, i.TaxRateBP, i.TaxAmount,
		i.Total, models.PaymentStatus(i.Total, 0), i.ReplacesID, i.Notes, userID, models.NowUTC())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func InsertInvoiceLine(ctx context.Context, q DBTX, l models.InvoiceLine) error {
	_, err := q.ExecContext(ctx, `INSERT INTO invoice_lines(invoice_id, line_type, part_id, part_code, description, notes, quantity, unit, unit_price, unit_cost, line_total, sort_order)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		l.InvoiceID, l.LineType, l.PartID, l.PartCode, l.Description, l.Notes, l.Quantity, l.Unit, l.UnitPrice, l.UnitCost, l.LineTotal, l.SortOrder)
	return err
}

// RefreshInvoicePaid recalculates amount_paid and payment_status from valid payments.
func RefreshInvoicePaid(ctx context.Context, q DBTX, invoiceID int64) error {
	_, err := q.ExecContext(ctx, `UPDATE invoices SET amount_paid = (SELECT COALESCE(SUM(amount), 0) FROM payments WHERE invoice_id = invoices.id AND status = 'Valid')
		WHERE id = ?`, invoiceID)
	if err != nil {
		return err
	}
	_, err = q.ExecContext(ctx, `UPDATE invoices SET payment_status = CASE WHEN amount_paid >= total THEN 'Paid' WHEN amount_paid <= 0 THEN 'Unpaid' ELSE 'Partially Paid' END
		WHERE id = ?`, invoiceID)
	return err
}

func VoidInvoice(ctx context.Context, q DBTX, id int64, reason string, userID int64) error {
	_, err := q.ExecContext(ctx, "UPDATE invoices SET status = 'Void', void_reason = ?, voided_at = ?, voided_by = ? WHERE id = ? AND status = 'Issued'",
		reason, models.NowUTC(), userID, id)
	return err
}

// ---------- payments ----------

type PaymentFilter struct {
	InvoiceID  int64
	CustomerID int64
	Method     string
	From, To   string
	Status     string // Valid (default ""), Void, all
	Q          string
}

const paymentSelect = `SELECT p.id, p.receipt_number, p.invoice_id, i.invoice_number, i.customer_name, p.payment_date, p.amount, p.method,
	p.reference, p.notes, p.overpayment, u.full_name, p.status, p.void_reason, COALESCE(p.voided_at, ''), p.created_at
	FROM payments p JOIN invoices i ON i.id = p.invoice_id JOIN users u ON u.id = p.received_by`

func scanPayment(sc interface{ Scan(...any) error }) (models.Payment, error) {
	var p models.Payment
	err := sc.Scan(&p.ID, &p.ReceiptNumber, &p.InvoiceID, &p.InvoiceNumber, &p.CustomerName, &p.PaymentDate, &p.Amount, &p.Method,
		&p.Reference, &p.Notes, &p.Overpayment, &p.ReceivedBy, &p.Status, &p.VoidReason, &p.VoidedAt, &p.CreatedAt)
	return p, err
}

func ListPayments(ctx context.Context, q DBTX, f PaymentFilter, o ListOpts) ([]models.Payment, int, models.Money, error) {
	var w Where
	if f.InvoiceID != 0 {
		w.Add("p.invoice_id = ?", f.InvoiceID)
	}
	if f.CustomerID != 0 {
		w.Add("i.customer_id = ?", f.CustomerID)
	}
	if f.Method != "" {
		w.Add("p.method = ?", f.Method)
	}
	if f.From != "" {
		w.Add("p.payment_date >= ?", f.From)
	}
	if f.To != "" {
		w.Add("p.payment_date <= ?", f.To)
	}
	if f.Q != "" {
		w.Add(`(p.receipt_number LIKE ? ESCAPE '\' OR i.invoice_number LIKE ? ESCAPE '\' OR i.customer_name LIKE ? ESCAPE '\' OR p.reference LIKE ? ESCAPE '\')`,
			Like(f.Q), Like(f.Q), Like(f.Q), Like(f.Q))
	}
	switch f.Status {
	case "all":
	case "Void":
		w.Add("p.status = 'Void'")
	default:
		w.Add("p.status = 'Valid'")
	}
	var n int
	var sum models.Money
	if err := q.QueryRowContext(ctx, "SELECT COUNT(*), COALESCE(SUM(p.amount), 0) FROM payments p JOIN invoices i ON i.id = p.invoice_id"+w.SQL(), w.Args...).Scan(&n, &sum); err != nil {
		return nil, 0, 0, err
	}
	rows, err := q.QueryContext(ctx, paymentSelect+w.SQL()+o.SQL(), w.Args...)
	if err != nil {
		return nil, 0, 0, err
	}
	defer rows.Close()
	var out []models.Payment
	for rows.Next() {
		p, err := scanPayment(rows)
		if err != nil {
			return nil, 0, 0, err
		}
		out = append(out, p)
	}
	return out, n, sum, rows.Err()
}

func GetPayment(ctx context.Context, q DBTX, id int64) (models.Payment, error) {
	p, err := scanPayment(q.QueryRowContext(ctx, paymentSelect+" WHERE p.id = ?", id))
	return p, notFound(err)
}

func InsertPayment(ctx context.Context, q DBTX, p models.Payment, userID int64) (int64, error) {
	res, err := q.ExecContext(ctx, `INSERT INTO payments(receipt_number, invoice_id, payment_date, amount, method, reference, notes, overpayment, received_by, status, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'Valid', ?)`,
		p.ReceiptNumber, p.InvoiceID, p.PaymentDate, p.Amount, p.Method, p.Reference, p.Notes, p.Overpayment, userID, models.NowUTC())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func VoidPayment(ctx context.Context, q DBTX, id int64, reason string, userID int64) error {
	_, err := q.ExecContext(ctx, "UPDATE payments SET status = 'Void', void_reason = ?, voided_at = ?, voided_by = ? WHERE id = ? AND status = 'Valid'",
		reason, models.NowUTC(), userID, id)
	return err
}

// PaymentsByMethod sums valid payments in a date range grouped by method.
func PaymentsByMethod(ctx context.Context, q DBTX, from, to string) (map[string]models.Money, error) {
	var w Where
	w.Add("status = 'Valid'")
	if from != "" {
		w.Add("payment_date >= ?", from)
	}
	if to != "" {
		w.Add("payment_date <= ?", to)
	}
	rows, err := q.QueryContext(ctx, "SELECT method, SUM(amount) FROM payments"+w.SQL()+" GROUP BY method", w.Args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := map[string]models.Money{}
	for rows.Next() {
		var k string
		var v models.Money
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		m[k] = v
	}
	return m, rows.Err()
}
