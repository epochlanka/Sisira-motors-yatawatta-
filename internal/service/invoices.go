package service

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"garagems/internal/models"
	"garagems/internal/repository"
)

type InvoiceInput struct {
	Date         string
	Discount     models.Money
	DiscountNote string
	Notes        string
}

// InvoiceTotals computes subtotal -> discount -> tax -> total using integer arithmetic.
func InvoiceTotals(subtotal, discount models.Money, taxEnabled bool, taxBP int64) (tax, total models.Money) {
	taxable := subtotal - discount
	if taxEnabled && taxBP > 0 {
		tax = models.PercentBP(taxable, taxBP)
	}
	return tax, taxable + tax
}

// CreateInvoice snapshots the job's charges and parts into a new invoice. Stock is
// not touched: parts were already deducted when they were added to the job.
func (s *Service) CreateInvoice(ctx context.Context, a models.Actor, jobID int64, in InvoiceInput) (int64, error) {
	if err := need(a, "invoices.create"); err != nil {
		return 0, err
	}
	if !models.ValidDate(in.Date) {
		return 0, Fail("Enter a valid invoice date.")
	}
	var id int64
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		set, err := repository.GetSettings(ctx, tx)
		if err != nil {
			return err
		}
		j, err := s.loadJob(ctx, tx, a, jobID)
		if err != nil {
			return err
		}
		if j.InvoiceID.Valid {
			return Fail("Invoice %s already exists for this job.", j.InvoiceNo)
		}
		if j.Status != models.JobCompleted {
			return Fail("Only completed jobs can be invoiced. Mark the job as Completed first.")
		}
		charges, err := repository.JobCharges(ctx, tx, jobID)
		if err != nil {
			return err
		}
		parts, err := repository.JobParts(ctx, tx, jobID)
		if err != nil {
			return err
		}
		if len(charges)+len(parts) == 0 {
			return Fail("The job has no charges or parts to invoice.")
		}
		var lines []models.InvoiceLine
		var subtotal models.Money
		order := 0
		for _, c := range charges {
			order++
			lines = append(lines, models.InvoiceLine{LineType: "charge", Description: c.Description, Notes: c.Notes, Quantity: c.Quantity,
				Unit: c.Unit, UnitPrice: c.UnitPrice, LineTotal: c.LineTotal, SortOrder: order})
			subtotal += c.LineTotal
		}
		for _, p := range parts {
			order++
			lines = append(lines, models.InvoiceLine{LineType: "part", PartID: sql.NullInt64{Int64: p.PartID, Valid: true}, PartCode: p.PartCode,
				Description: p.Description, Notes: p.Notes, Quantity: p.Quantity, Unit: p.Unit, UnitPrice: p.UnitPrice, UnitCost: p.UnitCost,
				LineTotal: p.LineTotal, SortOrder: order})
			subtotal += p.LineTotal
		}
		if in.Discount != 0 && !set.DiscountEnabled {
			return Fail("Discounts are disabled in settings.")
		}
		if in.Discount < 0 || in.Discount > subtotal {
			return Fail("Discount must be between 0 and the subtotal (%s).", subtotal)
		}
		tax, total := InvoiceTotals(subtotal, in.Discount, set.TaxEnabled, set.TaxRateBP)

		c, err := repository.GetCustomer(ctx, tx, j.CustomerID)
		if err != nil {
			return err
		}
		inv := models.Invoice{JobID: jobID, CustomerID: j.CustomerID, VehicleID: j.VehicleID, InvoiceDate: in.Date,
			CustomerName: c.Name, CustomerPhone: c.Phone, CustomerEmail: c.Email, CustomerAddress: c.Address,
			VehicleReg: j.VehicleReg, Odometer: j.Odometer, Subtotal: subtotal, Discount: in.Discount,
			DiscountNote: strings.TrimSpace(in.DiscountNote), Total: total, Notes: strings.TrimSpace(in.Notes)}
		if j.VehicleID.Valid {
			if v, err := repository.GetVehicle(ctx, tx, j.VehicleID.Int64); err == nil {
				desc := strings.TrimSpace(v.Make + " " + v.Model)
				if v.Year.Valid {
					desc = strings.TrimSpace(fmt.Sprintf("%s %d", desc, v.Year.Int64))
				}
				inv.VehicleDesc = desc
			}
		}
		if set.TaxEnabled {
			inv.TaxName, inv.TaxRateBP, inv.TaxAmount = set.TaxName, set.TaxRateBP, tax
		}
		// Link to the most recent voided invoice of this job (correction history).
		var prev sql.NullInt64
		if err := tx.QueryRowContext(ctx, "SELECT MAX(id) FROM invoices WHERE job_id = ? AND status = 'Void'", jobID).Scan(&prev); err != nil {
			return err
		}
		inv.ReplacesID = prev
		inv.InvoiceNumber, err = nextNumber(ctx, tx, "invoice", set.InvoicePrefix, set.InvoicePad)
		if err != nil {
			return err
		}
		id, err = repository.InsertInvoice(ctx, tx, inv, a.ID)
		if err != nil {
			if strings.Contains(err.Error(), "UNIQUE") {
				return Fail("Invoice number %s is already used. Check the next invoice number in Settings.", inv.InvoiceNumber)
			}
			return err
		}
		for _, l := range lines {
			l.InvoiceID = id
			if err := repository.InsertInvoiceLine(ctx, tx, l); err != nil {
				return err
			}
		}
		return repository.Audit(ctx, tx, a, "invoice.create", "invoice", id,
			fmt.Sprintf("%s for job %s, total %s", inv.InvoiceNumber, j.JobNumber, total))
	})
	return id, err
}

// VoidInvoice marks an invoice void (kept for history) and unlocks the job for correction.
func (s *Service) VoidInvoice(ctx context.Context, a models.Actor, invoiceID int64, reason string) error {
	if err := need(a, "invoices.void"); err != nil {
		return err
	}
	reason = strings.TrimSpace(reason)
	if len(reason) < 3 {
		return Fail("Enter a reason for voiding the invoice.")
	}
	return s.Tx(ctx, func(tx *sql.Tx) error {
		inv, err := repository.GetInvoice(ctx, tx, invoiceID)
		if err != nil {
			return err
		}
		if inv.IsVoid() {
			return Fail("Invoice is already void.")
		}
		var n int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM payments WHERE invoice_id = ? AND status = 'Valid'", invoiceID).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return Fail("This invoice has %d recorded payment(s). Void the payments first (or keep the invoice and record a correction).", n)
		}
		if err := repository.VoidInvoice(ctx, tx, invoiceID, reason, a.ID); err != nil {
			return err
		}
		return repository.Audit(ctx, tx, a, "invoice.void", "invoice", invoiceID,
			fmt.Sprintf("%s (total %s) voided: %s", inv.InvoiceNumber, inv.Total, reason))
	})
}

type PaymentInput struct {
	Date         string
	Amount       models.Money
	Method       string
	Reference    string
	Notes        string
	AllowOverpay bool
}

func validMethod(m string) bool {
	for _, x := range models.PaymentMethods {
		if x == m {
			return true
		}
	}
	return false
}

// RecordPayment inserts a payment and updates the invoice balance in one transaction.
func (s *Service) RecordPayment(ctx context.Context, a models.Actor, invoiceID int64, in PaymentInput) (int64, error) {
	if err := need(a, "payments.record"); err != nil {
		return 0, err
	}
	if !models.ValidDate(in.Date) {
		return 0, Fail("Enter a valid payment date.")
	}
	if in.Amount <= 0 {
		return 0, Fail("Payment amount must be greater than zero.")
	}
	if !validMethod(in.Method) {
		return 0, Fail("Select a payment method.")
	}
	var id int64
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		set, err := repository.GetSettings(ctx, tx)
		if err != nil {
			return err
		}
		inv, err := repository.GetInvoice(ctx, tx, invoiceID)
		if err != nil {
			return err
		}
		if inv.IsVoid() {
			return Fail("Payments cannot be recorded on a void invoice.")
		}
		balance := inv.Balance()
		over := in.Amount > balance
		notes := strings.TrimSpace(in.Notes)
		if over {
			if !in.AllowOverpay || !a.Can("payments.overpay") {
				return Fail("Payment of %s exceeds the outstanding balance of %s. An administrator must explicitly authorize an overpayment.", in.Amount, balance)
			}
			if notes == "" {
				return Fail("Enter a note explaining the authorized overpayment.")
			}
		}
		p := models.Payment{InvoiceID: invoiceID, PaymentDate: in.Date, Amount: in.Amount, Method: in.Method,
			Reference: strings.TrimSpace(in.Reference), Notes: notes, Overpayment: over}
		p.ReceiptNumber, err = nextNumber(ctx, tx, "receipt", set.ReceiptPrefix, set.InvoicePad)
		if err != nil {
			return err
		}
		id, err = repository.InsertPayment(ctx, tx, p, a.ID)
		if err != nil {
			return err
		}
		if err := repository.RefreshInvoicePaid(ctx, tx, invoiceID); err != nil {
			return err
		}
		detail := fmt.Sprintf("%s: %s %s on %s (balance before %s)", p.ReceiptNumber, in.Amount, in.Method, inv.InvoiceNumber, balance)
		if over {
			detail += "; OVERPAYMENT AUTHORIZED: " + notes
		}
		return repository.Audit(ctx, tx, a, "payment.record", "invoice", invoiceID, detail)
	})
	return id, err
}

func (s *Service) VoidPayment(ctx context.Context, a models.Actor, paymentID int64, reason string) (int64, error) {
	if err := need(a, "payments.void"); err != nil {
		return 0, err
	}
	reason = strings.TrimSpace(reason)
	if len(reason) < 3 {
		return 0, Fail("Enter a reason for voiding the payment.")
	}
	var invoiceID int64
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		p, err := repository.GetPayment(ctx, tx, paymentID)
		if err != nil {
			return err
		}
		invoiceID = p.InvoiceID
		if p.Status != "Valid" {
			return Fail("Payment is already void.")
		}
		if err := repository.VoidPayment(ctx, tx, paymentID, reason, a.ID); err != nil {
			return err
		}
		if err := repository.RefreshInvoicePaid(ctx, tx, p.InvoiceID); err != nil {
			return err
		}
		return repository.Audit(ctx, tx, a, "payment.void", "invoice", p.InvoiceID,
			fmt.Sprintf("%s (%s) on %s voided: %s", p.ReceiptNumber, p.Amount, p.InvoiceNumber, reason))
	})
	return invoiceID, err
}
