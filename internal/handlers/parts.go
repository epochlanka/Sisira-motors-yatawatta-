package handlers

import (
	"net/http"
	"strconv"

	mw "garagems/internal/middleware"
	"garagems/internal/models"
	"garagems/internal/repository"
	"garagems/internal/service"
)

func partURL(id int64) string { return "/parts/" + strconv.FormatInt(id, 10) }

var partSorts = map[string]string{
	"code":     "code COLLATE NOCASE",
	"name":     "name COLLATE NOCASE",
	"category": "category COLLATE NOCASE, name COLLATE NOCASE",
	"stock":    "qty_on_hand",
	"price":    "sale_price",
	"location": "location COLLATE NOCASE",
}

func (a *App) partsList(w http.ResponseWriter, r *http.Request) {
	l := newList(r, partSorts, "code", "asc")
	f := repository.PartFilter{Q: l.Get("q"), Category: l.Get("category"), LowOnly: l.Get("low") == "1", Status: l.Get("status")}
	list, total, err := repository.ListParts(r.Context(), a.DB, f, l.Opts())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	cats, err := repository.PartCategories(r.Context(), a.DB)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	l.Total = total
	p := a.page(r, "Parts", "parts")
	p.D["list"], p.D["parts"], p.D["categories"] = l, list, cats
	a.render(w, r, "parts_list", p)
}

func partFromForm(r *http.Request) (models.Part, V) {
	v := V{}
	p := models.Part{Code: val(r, "code"), Name: val(r, "name"), Description: val(r, "description"), Category: val(r, "category"),
		Compatible: val(r, "compatible"), Unit: val(r, "unit"), Location: val(r, "location")}
	v.Required("code", p.Code, "Part code / SKU")
	v.MaxLen("code", p.Code, 40, "Part code")
	v.Required("name", p.Name, "Name")
	v.MaxLen("name", p.Name, 150, "Name")
	v.MaxLen("description", p.Description, 1000, "Description")
	v.MaxLen("category", p.Category, 60, "Category")
	v.MaxLen("compatible", p.Compatible, 500, "Compatible vehicles")
	v.MaxLen("location", p.Location, 60, "Location")
	if p.Unit == "" {
		p.Unit = "pcs"
	}
	v.MaxLen("unit", p.Unit, 20, "Unit")
	p.SalePrice = v.Money(r, "sale_price", "Sale price", true)
	p.PurchaseCost = v.Money(r, "purchase_cost", "Purchase cost", false)
	p.ReorderLevel = v.Qty(r, "reorder_level", "Reorder level", false, false)
	return p, v
}

func (a *App) partFormPage(r *http.Request, title string, part models.Part) *Page {
	p := a.page(r, title, "parts")
	cats, _ := repository.PartCategories(r.Context(), a.DB)
	p.D["p"], p.D["categories"] = part, cats
	return p
}

func (a *App) partNew(w http.ResponseWriter, r *http.Request) {
	a.render(w, r, "part_form", a.partFormPage(r, "New part", models.Part{Unit: "pcs", Active: true}))
}

func (a *App) partCreate(w http.ResponseWriter, r *http.Request) {
	part, v := partFromForm(r)
	opening := v.Qty(r, "opening_qty", "Opening stock", false, false)
	if len(v) == 0 {
		id, err := a.Svc.CreatePart(r.Context(), mw.ActorFrom(r), part, opening)
		if err == nil {
			a.redirectFlash(w, r, partURL(id), "success", "Part "+part.Code+" created.")
			return
		}
		msg, ok := service.IsUserError(err)
		if !ok {
			a.serverError(w, r, err)
			return
		}
		v["code"] = msg
	}
	p := a.partFormPage(r, "New part", part)
	p.D["opening"] = val(r, "opening_qty")
	p.Errors = v
	a.render(w, r, "part_form", p, http.StatusUnprocessableEntity)
}

func (a *App) partView(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	part, err := repository.GetPart(ctx, a.DB, pathID(r, "id"))
	if err != nil {
		a.handleErr(w, r, err)
		return
	}
	l := newList(r, map[string]string{"date": "m.id"}, "date", "desc")
	moves, total, err := repository.ListMovements(ctx, a.DB, repository.MovementFilter{PartID: part.ID}, l.Opts())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	l.Total = total
	p := a.page(r, part.Code, "parts")
	p.D["p"], p.D["movements"], p.D["list"] = part, moves, l
	a.render(w, r, "part_view", p)
}

func (a *App) partEdit(w http.ResponseWriter, r *http.Request) {
	part, err := repository.GetPart(r.Context(), a.DB, pathID(r, "id"))
	if err != nil {
		a.handleErr(w, r, err)
		return
	}
	a.render(w, r, "part_form", a.partFormPage(r, "Edit "+part.Code, part))
}

func (a *App) partUpdate(w http.ResponseWriter, r *http.Request) {
	old, err := repository.GetPart(r.Context(), a.DB, pathID(r, "id"))
	if err != nil {
		a.handleErr(w, r, err)
		return
	}
	part, v := partFromForm(r)
	part.ID, part.QtyOnHand, part.Active = old.ID, old.QtyOnHand, old.Active
	if len(v) == 0 {
		err := a.Svc.UpdatePart(r.Context(), mw.ActorFrom(r), part)
		if err == nil {
			a.redirectFlash(w, r, partURL(part.ID), "success", "Part saved.")
			return
		}
		msg, ok := service.IsUserError(err)
		if !ok {
			a.serverError(w, r, err)
			return
		}
		v["code"] = msg
	}
	p := a.partFormPage(r, "Edit "+old.Code, part)
	p.Errors = v
	a.render(w, r, "part_form", p, http.StatusUnprocessableEntity)
}

func (a *App) partSetActive(w http.ResponseWriter, r *http.Request) {
	part, err := repository.GetPart(r.Context(), a.DB, pathID(r, "id"))
	if err != nil {
		a.handleErr(w, r, err)
		return
	}
	active := r.PostFormValue("active") == "1"
	if err := repository.SetPartActive(r.Context(), a.DB, part.ID, active); err != nil {
		a.serverError(w, r, err)
		return
	}
	msg, act := "Part reactivated.", "part.activate"
	if !active {
		msg, act = "Part deactivated. It can no longer be added to jobs or purchases; history is kept.", "part.deactivate"
	}
	a.audit(r, act, "part", part.ID, part.Code)
	a.redirectFlash(w, r, partURL(part.ID), "success", msg)
}

func (a *App) partAdjust(w http.ResponseWriter, r *http.Request) {
	id := pathID(r, "id")
	v := V{}
	mode := val(r, "mode")
	if mode != "count" {
		mode = "delta"
	}
	qty := v.Qty(r, "quantity", "Quantity", true, mode == "delta")
	if len(v) > 0 {
		a.redirectFlash(w, r, partURL(id)+"#adjust", "error", firstErr(v))
		return
	}
	err := a.Svc.AdjustStock(r.Context(), mw.ActorFrom(r), id, mode, qty, val(r, "reason"), checked(r, "allow_negative"))
	a.done(w, r, partURL(id), err, "Stock adjustment recorded.")
}

func (a *App) movementsList(w http.ResponseWriter, r *http.Request) {
	l := newList(r, map[string]string{"date": "m.id", "part": "p.code COLLATE NOCASE", "qty": "m.quantity"}, "date", "desc")
	from, to := dateParam(r, "from", ""), dateParam(r, "to", "")
	uf, ut := utcBounds(from, to)
	f := repository.MovementFilter{Q: l.Get("q"), Type: l.Get("type"), From: uf, To: ut, PartID: queryID(r, "part_id")}
	list, total, err := repository.ListMovements(r.Context(), a.DB, f, l.Opts())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	l.Total = total
	p := a.page(r, "Stock movements", "parts")
	p.D["list"], p.D["movements"], p.D["types"] = l, list, models.MovementTypes
	a.render(w, r, "movements_list", p)
}
