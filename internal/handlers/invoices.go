package handlers

import (
	"net/http"
	"strconv"

	mw "garagems/internal/middleware"
	"garagems/internal/models"
	"garagems/internal/repository"
	"garagems/internal/service"
)

func invoiceURL(id int64) string { return "/invoices/" + strconv.FormatInt(id, 10) }

var invoiceSorts = map[string]string{
	"date":     "i.invoice_date, i.id",
	"number":   "i.id",
	"customer": "i.customer_name COLLATE NOCASE",
	"total":    "i.total",
	"balance":  "i.total - i.amount_paid",
}

func (a *App) invoicesList(w http.ResponseWriter, r *http.Request) {
	l := newList(r, invoiceSorts, "date", "desc")
	f := repository.InvoiceFilter{Q: l.Get("q"), PaymentStatus: l.Get("pay"), Status: l.Get("status"),
		From: dateParam(r, "from", ""), To: dateParam(r, "to", ""), CustomerID: queryID(r, "customer_id")}
	list, totals, err := repository.ListInvoices(r.Context(), a.DB, f, l.Opts())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	l.Total = totals.Count
	p := a.page(r, "Invoices", "invoices")
	p.D["list"], p.D["invoices"], p.D["totals"] = l, list, totals
	a.render(w, r, "invoices_list", p)
}

func (a *App) invoiceCreate(w http.ResponseWriter, r *http.Request) {
	jobID := pathID(r, "id")
	v := V{}
	in := service.InvoiceInput{Date: val(r, "invoice_date"), DiscountNote: val(r, "discount_note"), Notes: val(r, "notes")}
	in.Discount = v.Money(r, "discount", "Discount", false)
	v.MaxLen("notes", in.Notes, 1000, "Notes")
	if len(v) > 0 {
		a.redirectFlash(w, r, jobURL(jobID)+"#invoice", "error", firstErr(v))
		return
	}
	id, err := a.Svc.CreateInvoice(r.Context(), mw.ActorFrom(r), jobID, in)
	if err != nil {
		a.done(w, r, jobURL(jobID)+"#invoice", err, "")
		return
	}
	a.redirectFlash(w, r, invoiceURL(id), "success", "Invoice created.")
}

func (a *App) loadInvoice(w http.ResponseWriter, r *http.Request) (models.Invoice, []models.InvoiceLine, []models.Payment, bool) {
	ctx := r.Context()
	inv, err := repository.GetInvoice(ctx, a.DB, pathID(r, "id"))
	if err != nil {
		a.handleErr(w, r, err)
		return inv, nil, nil, false
	}
	lines, err := repository.InvoiceLines(ctx, a.DB, inv.ID)
	if err != nil {
		a.serverError(w, r, err)
		return inv, nil, nil, false
	}
	pays, _, _, err := repository.ListPayments(ctx, a.DB, repository.PaymentFilter{InvoiceID: inv.ID, Status: "all"}, repository.ListOpts{OrderBy: "p.id"})
	if err != nil {
		a.serverError(w, r, err)
		return inv, nil, nil, false
	}
	return inv, lines, pays, true
}

func (a *App) invoiceView(w http.ResponseWriter, r *http.Request) {
	inv, lines, pays, ok := a.loadInvoice(w, r)
	if !ok {
		return
	}
	p := a.page(r, "Invoice "+inv.InvoiceNumber, "invoices")
	p.D["inv"], p.D["lines"], p.D["payments"], p.D["methods"] = inv, lines, pays, models.PaymentMethods
	a.render(w, r, "invoice_view", p)
}

func (a *App) invoicePrint(w http.ResponseWriter, r *http.Request) {
	inv, lines, pays, ok := a.loadInvoice(w, r)
	if !ok {
		return
	}
	var charges, parts []models.InvoiceLine
	for _, l := range lines {
		if l.LineType == "part" {
			parts = append(parts, l)
		} else {
			charges = append(charges, l)
		}
	}
	var valid []models.Payment
	for _, pm := range pays {
		if pm.Status == "Valid" {
			valid = append(valid, pm)
		}
	}
	p := a.page(r, "Invoice "+inv.InvoiceNumber, "invoices")
	p.D["inv"], p.D["charges"], p.D["parts"], p.D["payments"] = inv, charges, parts, valid
	a.render(w, r, "invoice_print", p)
}

func (a *App) invoiceVoid(w http.ResponseWriter, r *http.Request) {
	id := pathID(r, "id")
	err := a.Svc.VoidInvoice(r.Context(), mw.ActorFrom(r), id, val(r, "reason"))
	a.done(w, r, invoiceURL(id), err, "Invoice voided. The job is unlocked for correction; create a new invoice when ready.")
}

func (a *App) paymentCreate(w http.ResponseWriter, r *http.Request) {
	id := pathID(r, "id")
	v := V{}
	in := service.PaymentInput{Date: val(r, "payment_date"), Method: val(r, "method"), Reference: val(r, "reference"),
		Notes: val(r, "notes"), AllowOverpay: checked(r, "allow_overpay")}
	in.Amount = v.Money(r, "amount", "Amount", true)
	v.MaxLen("reference", in.Reference, 100, "Reference")
	v.MaxLen("notes", in.Notes, 500, "Notes")
	if len(v) > 0 {
		a.redirectFlash(w, r, invoiceURL(id)+"#payment", "error", firstErr(v))
		return
	}
	pid, err := a.Svc.RecordPayment(r.Context(), mw.ActorFrom(r), id, in)
	if err != nil {
		a.done(w, r, invoiceURL(id)+"#payment", err, "")
		return
	}
	a.redirectFlash(w, r, invoiceURL(id)+"?receipt="+strconv.FormatInt(pid, 10), "success", "Payment of "+in.Amount.String()+" recorded.")
}

func (a *App) paymentVoid(w http.ResponseWriter, r *http.Request) {
	invID, err := a.Svc.VoidPayment(r.Context(), mw.ActorFrom(r), pathID(r, "id"), val(r, "reason"))
	to := "/invoices"
	if invID != 0 {
		to = invoiceURL(invID)
	}
	a.done(w, r, to, err, "Payment voided and invoice balance updated.")
}

func (a *App) receiptPrint(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	pm, err := repository.GetPayment(ctx, a.DB, pathID(r, "id"))
	if err != nil {
		a.handleErr(w, r, err)
		return
	}
	inv, err := repository.GetInvoice(ctx, a.DB, pm.InvoiceID)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	p := a.page(r, "Receipt "+pm.ReceiptNumber, "invoices")
	p.D["pay"], p.D["inv"] = pm, inv
	a.render(w, r, "receipt_print", p)
}

var paymentSorts = map[string]string{
	"date":   "p.payment_date, p.id",
	"amount": "p.amount",
	"method": "p.method",
}

func (a *App) paymentsList(w http.ResponseWriter, r *http.Request) {
	l := newList(r, paymentSorts, "date", "desc")
	f := repository.PaymentFilter{Q: l.Get("q"), Method: l.Get("method"), Status: l.Get("status"),
		From: dateParam(r, "from", ""), To: dateParam(r, "to", "")}
	list, n, sum, err := repository.ListPayments(r.Context(), a.DB, f, l.Opts())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	l.Total = n
	p := a.page(r, "Payments", "invoices")
	p.D["list"], p.D["payments"], p.D["sum"], p.D["methods"] = l, list, sum, models.PaymentMethods
	a.render(w, r, "payments_list", p)
}
