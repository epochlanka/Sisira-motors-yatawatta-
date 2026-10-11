package handlers

import (
	"net/http"
	"strconv"

	mw "garagems/internal/middleware"
	"garagems/internal/models"
	"garagems/internal/repository"
	"garagems/internal/service"
)

func supplierURL(id int64) string { return "/suppliers/" + strconv.FormatInt(id, 10) }
func purchaseURL(id int64) string { return "/purchases/" + strconv.FormatInt(id, 10) }

var supplierSorts = map[string]string{"name": "name COLLATE NOCASE", "contact": "contact_person COLLATE NOCASE", "created": "id"}

func (a *App) suppliersList(w http.ResponseWriter, r *http.Request) {
	l := newList(r, supplierSorts, "name", "asc")
	list, total, err := repository.ListSuppliers(r.Context(), a.DB, repository.SupplierFilter{Q: l.Get("q"), Status: l.Get("status")}, l.Opts())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	l.Total = total
	p := a.page(r, "Suppliers", "suppliers")
	p.D["list"], p.D["suppliers"] = l, list
	a.render(w, r, "suppliers_list", p)
}

func supplierFromForm(r *http.Request) (models.Supplier, V) {
	v := V{}
	s := models.Supplier{Name: val(r, "name"), ContactPerson: val(r, "contact_person"), Phone: val(r, "phone"), Email: val(r, "email"),
		Address: val(r, "address"), Notes: val(r, "notes")}
	v.Required("name", s.Name, "Name")
	v.MaxLen("name", s.Name, 120, "Name")
	v.MaxLen("contact_person", s.ContactPerson, 120, "Contact person")
	v.MaxLen("phone", s.Phone, 40, "Phone")
	v.Email("email", s.Email)
	v.MaxLen("address", s.Address, 500, "Address")
	v.MaxLen("notes", s.Notes, 2000, "Notes")
	return s, v
}

func (a *App) supplierNew(w http.ResponseWriter, r *http.Request) {
	p := a.page(r, "New supplier", "suppliers")
	p.D["s"] = models.Supplier{}
	a.render(w, r, "supplier_form", p)
}

func (a *App) supplierCreate(w http.ResponseWriter, r *http.Request) {
	s, v := supplierFromForm(r)
	if len(v) > 0 {
		p := a.page(r, "New supplier", "suppliers")
		p.D["s"], p.Errors = s, v
		a.render(w, r, "supplier_form", p, http.StatusUnprocessableEntity)
		return
	}
	id, err := repository.CreateSupplier(r.Context(), a.DB, s)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	a.audit(r, "supplier.create", "supplier", id, s.Name)
	a.redirectFlash(w, r, supplierURL(id), "success", "Supplier created.")
}

func (a *App) supplierView(w http.ResponseWriter, r *http.Request) {
	s, err := repository.GetSupplier(r.Context(), a.DB, pathID(r, "id"))
	if err != nil {
		a.handleErr(w, r, err)
		return
	}
	p := a.page(r, s.Name, "suppliers")
	p.D["s"] = s
	if p.Can("purchases.view") {
		list, _, sum, err := repository.ListPurchases(r.Context(), a.DB, repository.PurchaseFilter{SupplierID: s.ID}, repository.ListOpts{OrderBy: "pu.purchase_date DESC, pu.id DESC", Limit: 100})
		if err != nil {
			a.serverError(w, r, err)
			return
		}
		p.D["purchases"], p.D["sum"] = list, sum
	}
	a.render(w, r, "supplier_view", p)
}

func (a *App) supplierEdit(w http.ResponseWriter, r *http.Request) {
	s, err := repository.GetSupplier(r.Context(), a.DB, pathID(r, "id"))
	if err != nil {
		a.handleErr(w, r, err)
		return
	}
	p := a.page(r, "Edit supplier", "suppliers")
	p.D["s"] = s
	a.render(w, r, "supplier_form", p)
}

func (a *App) supplierUpdate(w http.ResponseWriter, r *http.Request) {
	id := pathID(r, "id")
	if _, err := repository.GetSupplier(r.Context(), a.DB, id); err != nil {
		a.handleErr(w, r, err)
		return
	}
	s, v := supplierFromForm(r)
	s.ID = id
	if len(v) > 0 {
		p := a.page(r, "Edit supplier", "suppliers")
		p.D["s"], p.Errors = s, v
		a.render(w, r, "supplier_form", p, http.StatusUnprocessableEntity)
		return
	}
	if err := repository.UpdateSupplier(r.Context(), a.DB, s); err != nil {
		a.serverError(w, r, err)
		return
	}
	a.audit(r, "supplier.update", "supplier", id, s.Name)
	a.redirectFlash(w, r, supplierURL(id), "success", "Supplier saved.")
}

func (a *App) supplierSetActive(w http.ResponseWriter, r *http.Request) {
	s, err := repository.GetSupplier(r.Context(), a.DB, pathID(r, "id"))
	if err != nil {
		a.handleErr(w, r, err)
		return
	}
	active := r.PostFormValue("active") == "1"
	if err := repository.SetSupplierActive(r.Context(), a.DB, s.ID, active); err != nil {
		a.serverError(w, r, err)
		return
	}
	act := "supplier.deactivate"
	if active {
		act = "supplier.activate"
	}
	a.audit(r, act, "supplier", s.ID, s.Name)
	a.redirectFlash(w, r, supplierURL(s.ID), "success", "Supplier updated.")
}

// ---------- purchases ----------

var purchaseSorts = map[string]string{
	"date":     "pu.purchase_date, pu.id",
	"number":   "pu.id",
	"supplier": "s.name COLLATE NOCASE",
	"total":    "pu.total",
	"status":   "pu.status",
}

func (a *App) purchaseFilter(r *http.Request, l *List) repository.PurchaseFilter {
	f := repository.PurchaseFilter{Q: l.Get("q"), SupplierID: queryID(r, "supplier_id"), Status: l.Get("status"),
		From: dateParam(r, "from", ""), To: dateParam(r, "to", "")}
	if code := l.Get("part"); code != "" {
		if p, err := repository.GetPartByCode(r.Context(), a.DB, code); err == nil {
			f.PartID = p.ID
		} else {
			f.PartID = -1
		}
	}
	switch f.Status {
	case "", models.PurchaseDraft, models.PurchaseReceived, models.PurchaseCancelled:
	default:
		f.Status = ""
	}
	return f
}

func (a *App) purchasesList(w http.ResponseWriter, r *http.Request) {
	l := newList(r, purchaseSorts, "date", "desc")
	f := a.purchaseFilter(r, l)
	list, total, sum, err := repository.ListPurchases(r.Context(), a.DB, f, l.Opts())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	sups, err := repository.AllSuppliers(r.Context(), a.DB, false)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	l.Total = total
	p := a.page(r, "Purchases", "purchases")
	p.D["list"], p.D["purchases"], p.D["sum"], p.D["suppliers"] = l, list, sum, sups
	a.render(w, r, "purchases_list", p)
}

func (a *App) purchaseNew(w http.ResponseWriter, r *http.Request) {
	sups, err := repository.AllSuppliers(r.Context(), a.DB, true)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	p := a.page(r, "New purchase", "purchases")
	p.D["pu"], p.D["suppliers"] = models.Purchase{PurchaseDate: models.Today(), SupplierID: queryID(r, "supplier_id")}, sups
	a.render(w, r, "purchase_form", p)
}

func purchaseFromForm(r *http.Request) (models.Purchase, V) {
	v := V{}
	pu := models.Purchase{SupplierID: formID(r, "supplier_id"), PurchaseDate: val(r, "purchase_date"), Reference: val(r, "reference"), Notes: val(r, "notes")}
	if pu.SupplierID == 0 {
		v["supplier_id"] = "Select a supplier."
	}
	v.Date("purchase_date", pu.PurchaseDate, "Date")
	v.MaxLen("reference", pu.Reference, 100, "Reference")
	v.MaxLen("notes", pu.Notes, 2000, "Notes")
	return pu, v
}

func (a *App) purchaseCreate(w http.ResponseWriter, r *http.Request) {
	pu, v := purchaseFromForm(r)
	if len(v) == 0 {
		id, err := a.Svc.CreatePurchase(r.Context(), mw.ActorFrom(r), pu, a.Settings().PurchasePrefix)
		if err == nil {
			a.redirectFlash(w, r, purchaseURL(id), "success", "Draft purchase created. Add the items received.")
			return
		}
		msg, ok := service.IsUserError(err)
		if !ok {
			a.serverError(w, r, err)
			return
		}
		v["supplier_id"] = msg
	}
	sups, _ := repository.AllSuppliers(r.Context(), a.DB, true)
	p := a.page(r, "New purchase", "purchases")
	p.D["pu"], p.D["suppliers"], p.Errors = pu, sups, v
	a.render(w, r, "purchase_form", p, http.StatusUnprocessableEntity)
}

func (a *App) purchaseView(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	pu, err := repository.GetPurchase(ctx, a.DB, pathID(r, "id"))
	if err != nil {
		a.handleErr(w, r, err)
		return
	}
	items, err := repository.PurchaseItems(ctx, a.DB, pu.ID)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	p := a.page(r, "Purchase "+pu.PurchaseNumber, "purchases")
	p.D["pu"], p.D["items"] = pu, items
	if pu.Status == models.PurchaseDraft && p.Can("purchases.edit") {
		opts, err := repository.ActivePartOptions(ctx, a.DB)
		if err != nil {
			a.serverError(w, r, err)
			return
		}
		sups, _ := repository.AllSuppliers(ctx, a.DB, true)
		p.D["partOptions"], p.D["suppliers"] = opts, sups
	}
	if p.Can("parts.view") {
		moves, _, err := repository.ListMovements(ctx, a.DB, repository.MovementFilter{PurchaseID: pu.ID}, repository.ListOpts{OrderBy: "m.id"})
		if err != nil {
			a.serverError(w, r, err)
			return
		}
		p.D["movements"] = moves
	}
	a.render(w, r, "purchase_view", p)
}

func (a *App) purchaseUpdate(w http.ResponseWriter, r *http.Request) {
	id := pathID(r, "id")
	pu, v := purchaseFromForm(r)
	pu.ID = id
	if len(v) > 0 {
		a.redirectFlash(w, r, purchaseURL(id), "error", firstErr(v))
		return
	}
	err := a.Svc.UpdatePurchase(r.Context(), mw.ActorFrom(r), pu)
	a.done(w, r, purchaseURL(id), err, "Purchase details saved.")
}

func (a *App) purchaseItemAdd(w http.ResponseWriter, r *http.Request) {
	id := pathID(r, "id")
	to := purchaseURL(id) + "#items"
	part, ok := a.partFromCode(r, "part")
	if !ok {
		a.redirectFlash(w, r, to, "error", "Part not found. Choose a part code from the list (create new parts under Parts first).")
		return
	}
	v := V{}
	qty := v.Qty(r, "quantity", "Quantity", true, false)
	cost := part.PurchaseCost
	if val(r, "unit_cost") != "" {
		cost = v.Money(r, "unit_cost", "Unit cost", true)
	}
	if len(v) > 0 {
		a.redirectFlash(w, r, to, "error", firstErr(v))
		return
	}
	err := a.Svc.AddPurchaseItem(r.Context(), mw.ActorFrom(r), id, part.ID, qty, cost)
	a.done(w, r, to, err, "Item added.")
}

func (a *App) purchaseItemDelete(w http.ResponseWriter, r *http.Request) {
	id := pathID(r, "id")
	err := a.Svc.RemovePurchaseItem(r.Context(), mw.ActorFrom(r), id, pathID(r, "iid"))
	a.done(w, r, purchaseURL(id)+"#items", err, "Item removed.")
}

func (a *App) purchaseReceive(w http.ResponseWriter, r *http.Request) {
	id := pathID(r, "id")
	err := a.Svc.ReceivePurchase(r.Context(), mw.ActorFrom(r), id)
	a.done(w, r, purchaseURL(id), err, "Purchase received. Stock levels have been increased.")
}

func (a *App) purchaseCancel(w http.ResponseWriter, r *http.Request) {
	id := pathID(r, "id")
	err := a.Svc.CancelPurchase(r.Context(), mw.ActorFrom(r), id, val(r, "reason"), checked(r, "allow_negative"))
	a.done(w, r, purchaseURL(id), err, "Purchase cancelled.")
}
