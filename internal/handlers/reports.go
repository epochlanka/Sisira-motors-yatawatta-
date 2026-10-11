package handlers

import (
	"net/http"

	mw "garagems/internal/middleware"
	"garagems/internal/models"
	"garagems/internal/repository"
)

// needAny checks an area permission in addition to reports.view.
func (a *App) needAny(w http.ResponseWriter, r *http.Request, perms ...string) bool {
	s := mw.FromContext(r.Context())
	for _, p := range perms {
		if s.Can(p) {
			return true
		}
	}
	a.forbidden(w, r)
	return false
}

func (a *App) reportPage(r *http.Request, title string) (*Page, string, string) {
	p := a.page(r, title, "reports")
	from, to := dateParam(r, "from", monthStart()), dateParam(r, "to", models.Today())
	p.D["from"], p.D["to"] = from, to
	return p, from, to
}

func (a *App) reportsIndex(w http.ResponseWriter, r *http.Request) {
	a.render(w, r, "reports_index", a.page(r, "Reports", "reports"))
}

func (a *App) reportJobs(w http.ResponseWriter, r *http.Request) {
	if !a.needAny(w, r, "jobs.view_all") {
		return
	}
	ctx := r.Context()
	p, from, to := a.reportPage(r, "Jobs report")
	l := newList(r, jobSorts, "date", "desc")
	f := repository.JobFilter{Q: l.Get("q"), Status: l.Get("status"), From: from, To: to, AssignedID: queryID(r, "assigned")}
	if f.Status != "" && f.Status != "active" && !models.ValidJobStatus(f.Status) {
		f.Status = ""
	}
	byStatus, err := repository.JobsByStatus(ctx, a.DB, f)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	completed, err := repository.JobsCompletedBetween(ctx, a.DB, from, to)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	jobs, total, err := repository.ListJobs(ctx, a.DB, f, l.Opts())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	users, _ := repository.ListUsers(ctx, a.DB, false)
	l.Total = total
	var value models.Money
	for _, s := range byStatus {
		if s.Status != models.JobCancelled {
			value += s.Total
		}
	}
	p.D["byStatus"], p.D["completed"], p.D["jobs"], p.D["list"], p.D["users"], p.D["statuses"], p.D["opened"], p.D["value"] =
		byStatus, completed, jobs, l, users, models.JobStatuses, total, value
	a.render(w, r, "report_jobs", p)
}

func (a *App) reportSales(w http.ResponseWriter, r *http.Request) {
	if !a.needAny(w, r, "invoices.view") {
		return
	}
	ctx := r.Context()
	p, from, to := a.reportPage(r, "Sales & payments report")
	l := newList(r, invoiceSorts, "date", "desc")
	f := repository.InvoiceFilter{Q: l.Get("q"), PaymentStatus: l.Get("pay"), From: from, To: to}
	invs, totals, err := repository.ListInvoices(ctx, a.DB, f, l.Opts())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	l.Total = totals.Count
	byMethod, err := repository.PaymentsByMethod(ctx, a.DB, from, to)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	var received models.Money
	for _, m := range byMethod {
		received += m
	}
	pays, _, _, err := repository.ListPayments(ctx, a.DB, repository.PaymentFilter{From: from, To: to}, repository.ListOpts{OrderBy: "p.payment_date DESC, p.id DESC", Limit: 200})
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	charges, err := repository.ChargeSales(ctx, a.DB, from, to)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	p.D["invoices"], p.D["totals"], p.D["list"], p.D["byMethod"], p.D["methods"], p.D["received"], p.D["payments"], p.D["chargeSales"] =
		invs, totals, l, byMethod, models.PaymentMethods, received, pays, charges
	a.render(w, r, "report_sales", p)
}

func (a *App) reportOutstanding(w http.ResponseWriter, r *http.Request) {
	if !a.needAny(w, r, "invoices.view") {
		return
	}
	p := a.page(r, "Outstanding balances", "reports")
	l := newList(r, invoiceSorts, "date", "asc")
	f := repository.InvoiceFilter{Q: l.Get("q"), PaymentStatus: "outstanding", From: dateParam(r, "from", ""), To: dateParam(r, "to", "")}
	invs, totals, err := repository.ListInvoices(r.Context(), a.DB, f, l.Opts())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	l.Total = totals.Count
	p.D["invoices"], p.D["totals"], p.D["list"], p.D["from"], p.D["to"] = invs, totals, l, f.From, f.To
	a.render(w, r, "report_outstanding", p)
}

func (a *App) reportPartsSold(w http.ResponseWriter, r *http.Request) {
	if !a.needAny(w, r, "parts.view") {
		return
	}
	ctx := r.Context()
	p, from, to := a.reportPage(r, "Parts sold")
	var partID int64
	code := r.URL.Query().Get("part")
	if code != "" {
		if pt, err := repository.GetPartByCode(ctx, a.DB, code); err == nil {
			partID = pt.ID
		} else {
			partID = -1
		}
	}
	cat := r.URL.Query().Get("category")
	rows, err := repository.PartsSold(ctx, a.DB, from, to, partID, cat)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	var rev, cost models.Money
	for _, x := range rows {
		rev += x.Revenue
		cost += x.Cost
	}
	cats, _ := repository.PartCategories(ctx, a.DB)
	p.D["rows"], p.D["revenue"], p.D["cost"], p.D["margin"], p.D["part"], p.D["category"], p.D["categories"] =
		rows, rev, cost, rev-cost, code, cat, cats
	a.render(w, r, "report_parts", p)
}

func (a *App) reportStock(w http.ResponseWriter, r *http.Request) {
	if !a.needAny(w, r, "parts.view") {
		return
	}
	ctx := r.Context()
	p := a.page(r, "Stock levels", "reports")
	l := newList(r, partSorts, "code", "asc")
	l.PerPage = 100
	f := repository.PartFilter{Q: l.Get("q"), Category: l.Get("category"), LowOnly: l.Get("low") == "1", Status: l.Get("status")}
	parts, total, err := repository.ListParts(ctx, a.DB, f, l.Opts())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	l.Total = total
	cost, sale, err := repository.StockValue(ctx, a.DB, f)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	cats, _ := repository.PartCategories(ctx, a.DB)
	p.D["parts"], p.D["list"], p.D["costValue"], p.D["saleValue"], p.D["categories"] = parts, l, cost, sale, cats
	a.render(w, r, "report_stock", p)
}

func (a *App) reportPurchases(w http.ResponseWriter, r *http.Request) {
	if !a.needAny(w, r, "purchases.view") {
		return
	}
	ctx := r.Context()
	p, from, to := a.reportPage(r, "Purchases report")
	l := newList(r, purchaseSorts, "date", "desc")
	f := a.purchaseFilter(r, l)
	f.From, f.To = from, to
	if l.Get("status") == "" && !l.Values.Has("status") {
		f.Status = models.PurchaseReceived
		l.Values.Set("status", models.PurchaseReceived)
	}
	bySup, err := repository.PurchasesBySupplier(ctx, a.DB, f)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	list, total, sum, err := repository.ListPurchases(ctx, a.DB, f, l.Opts())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	l.Total = total
	sups, _ := repository.AllSuppliers(ctx, a.DB, false)
	p.D["bySupplier"], p.D["purchases"], p.D["sum"], p.D["list"], p.D["suppliers"] = bySup, list, sum, l, sups
	a.render(w, r, "report_purchases", p)
}

func (a *App) reportMovements(w http.ResponseWriter, r *http.Request) {
	if !a.needAny(w, r, "parts.view") {
		return
	}
	ctx := r.Context()
	p, from, to := a.reportPage(r, "Stock movements report")
	l := newList(r, map[string]string{"date": "m.id", "part": "p.code COLLATE NOCASE", "qty": "m.quantity"}, "date", "desc")
	uf, ut := utcBounds(from, to)
	f := repository.MovementFilter{Type: l.Get("type"), From: uf, To: ut}
	if code := l.Get("part"); code != "" {
		if pt, err := repository.GetPartByCode(ctx, a.DB, code); err == nil {
			f.PartID = pt.ID
		} else {
			f.PartID = -1
		}
	}
	summary, err := repository.MovementsByType(ctx, a.DB, f)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	list, total, err := repository.ListMovements(ctx, a.DB, f, l.Opts())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	l.Total = total
	p.D["summary"], p.D["movements"], p.D["list"], p.D["types"] = summary, list, l, models.MovementTypes
	a.render(w, r, "report_movements", p)
}
