package handlers

import (
	"database/sql"
	"net/http"
	"strconv"
	"strings"

	mw "garagems/internal/middleware"
	"garagems/internal/models"
	"garagems/internal/repository"
	"garagems/internal/service"
)

var jobSorts = map[string]string{
	"date":     "j.job_date, j.id",
	"number":   "j.id",
	"customer": "c.name COLLATE NOCASE",
	"reg":      "v.registration_norm",
	"status":   "j.status",
	"assigned": "u.full_name",
}

func jobURL(id int64) string { return "/jobs/" + strconv.FormatInt(id, 10) }

func (a *App) jobsList(w http.ResponseWriter, r *http.Request) {
	s := mw.FromContext(r.Context())
	l := newList(r, jobSorts, "date", "desc")
	f := repository.JobFilter{Q: l.Get("q"), Status: l.Get("status"), From: dateParam(r, "from", ""), To: dateParam(r, "to", ""),
		CustomerID: queryID(r, "customer_id"), VehicleID: queryID(r, "vehicle_id"), AssignedID: queryID(r, "assigned")}
	if f.Status != "" && f.Status != "active" && !models.ValidJobStatus(f.Status) {
		f.Status = ""
	}
	if l.Get("uninvoiced") == "1" {
		f.Uninvoiced = true
	}
	if !s.Can("jobs.view_all") {
		f.OnlyUserID = s.User.ID
	}
	list, total, err := repository.ListJobs(r.Context(), a.DB, f, l.Opts())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	l.Total = total
	users, err := repository.ListUsers(r.Context(), a.DB, false)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	p := a.page(r, "Jobs", "jobs")
	p.D["list"], p.D["jobs"], p.D["users"], p.D["statuses"] = l, list, users, models.JobStatuses
	a.render(w, r, "jobs_list", p)
}

func (a *App) jobNew(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := a.page(r, "Open a job", "jobs")
	cid := queryID(r, "customer_id")
	if cid == 0 {
		// Step 1: find the customer or vehicle.
		q := strings.TrimSpace(r.URL.Query().Get("q"))
		p.D["q"] = q
		if q != "" {
			vs, _, err := repository.ListVehicles(ctx, a.DB, repository.VehicleFilter{Q: q}, repository.ListOpts{OrderBy: "v.registration_norm", Limit: 20})
			if err != nil {
				a.serverError(w, r, err)
				return
			}
			cs, err := repository.SearchCustomersQuick(ctx, a.DB, q, 20)
			if err != nil {
				a.serverError(w, r, err)
				return
			}
			p.D["vehicles"], p.D["customers"] = vs, cs
		}
		a.render(w, r, "job_new", p)
		return
	}
	c, err := repository.GetCustomer(ctx, a.DB, cid)
	if err != nil {
		a.handleErr(w, r, err)
		return
	}
	j := models.Job{CustomerID: c.ID, JobDate: models.Today()}
	if vid := queryID(r, "vehicle_id"); vid != 0 {
		j.VehicleID = sql.NullInt64{Int64: vid, Valid: true}
	}
	if err := a.jobFormData(r, p, c, j); err != nil {
		a.serverError(w, r, err)
		return
	}
	a.render(w, r, "job_form", p)
}

func (a *App) jobFormData(r *http.Request, p *Page, c models.Customer, j models.Job) error {
	vehicles, _, err := repository.ListVehicles(r.Context(), a.DB, repository.VehicleFilter{CustomerID: c.ID}, repository.ListOpts{OrderBy: "v.registration_norm"})
	if err != nil {
		return err
	}
	users, err := repository.ListUsers(r.Context(), a.DB, true)
	if err != nil {
		return err
	}
	p.D["c"], p.D["j"], p.D["vehicles"], p.D["users"] = c, j, vehicles, users
	return nil
}

func jobFromForm(r *http.Request, j models.Job) (models.Job, V) {
	v := V{}
	j.JobDate = val(r, "job_date")
	v.Date("job_date", j.JobDate, "Job date")
	j.Odometer = v.OptInt(r, "odometer", "Odometer", 0, 99_999_999)
	j.ReportedIssue = val(r, "reported_issue")
	j.InspectionNotes = val(r, "inspection_notes")
	j.WorkPerformed = val(r, "work_performed")
	j.InternalNotes = val(r, "internal_notes")
	for f, s := range map[string]string{"reported_issue": j.ReportedIssue, "inspection_notes": j.InspectionNotes, "work_performed": j.WorkPerformed, "internal_notes": j.InternalNotes} {
		v.MaxLen(f, s, 5000, "This field")
	}
	j.VehicleID = repository.NullInt(formID(r, "vehicle_id"))
	j.AssignedUserID = repository.NullInt(formID(r, "assigned_user_id"))
	return j, v
}

func (a *App) jobCreate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	c, err := repository.GetCustomer(ctx, a.DB, formID(r, "customer_id"))
	if err != nil {
		a.handleErr(w, r, err)
		return
	}
	j, v := jobFromForm(r, models.Job{CustomerID: c.ID})
	if j.ReportedIssue == "" && j.VehicleID.Valid {
		v["reported_issue"] = "Describe the reported issue or requested work."
	}
	if j.AssignedUserID.Valid {
		if u, err := repository.GetUser(ctx, a.DB, j.AssignedUserID.Int64); err != nil || !u.Active {
			v["assigned_user_id"] = "Select an active staff member."
		}
	}
	if len(v) == 0 {
		id, err := a.Svc.CreateJob(ctx, mw.ActorFrom(r), j, a.Settings().JobPrefix)
		if err == nil {
			a.redirectFlash(w, r, jobURL(id), "success", "Job opened. Add charges and parts below.")
			return
		}
		msg, ok := service.IsUserError(err)
		if !ok {
			a.serverError(w, r, err)
			return
		}
		v["_"] = msg
	}
	p := a.page(r, "Open a job", "jobs")
	if err := a.jobFormData(r, p, c, j); err != nil {
		a.serverError(w, r, err)
		return
	}
	p.Errors = v
	if m := v["_"]; m != "" {
		p.FlashKind, p.FlashMsg = "error", m
	}
	a.render(w, r, "job_form", p, http.StatusUnprocessableEntity)
}

func (a *App) jobView(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	actor := mw.ActorFrom(r)
	j, err := repository.GetJob(ctx, a.DB, pathID(r, "id"))
	if err != nil {
		a.handleErr(w, r, err)
		return
	}
	if !service.CanAccessJob(actor, j) {
		a.forbidden(w, r)
		return
	}
	p := a.page(r, j.JobNumber, "jobs")
	p.D["j"] = j
	charges, err := repository.JobCharges(ctx, a.DB, j.ID)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	parts, err := repository.JobParts(ctx, a.DB, j.ID)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	p.D["charges"], p.D["parts"] = charges, parts
	locked := j.InvoiceID.Valid || j.Status == models.JobCancelled
	p.D["locked"] = locked
	if !locked && p.Can("jobs.charges") {
		cats, err := repository.ListChargeCategories(ctx, a.DB, true)
		if err != nil {
			a.serverError(w, r, err)
			return
		}
		p.D["categories"] = cats
	}
	if !locked && p.Can("jobs.parts") {
		opts, err := repository.ActivePartOptions(ctx, a.DB)
		if err != nil {
			a.serverError(w, r, err)
			return
		}
		p.D["partOptions"] = opts
	}
	set := a.Settings()
	tax, total := service.InvoiceTotals(j.Total(), 0, set.TaxEnabled, set.TaxRateBP)
	p.D["estTax"], p.D["estTotal"] = tax, total
	if p.Can("invoices.view") {
		invs, _, err := repository.ListInvoices(ctx, a.DB, repository.InvoiceFilter{JobID: j.ID, Status: "all"}, repository.ListOpts{OrderBy: "i.id DESC"})
		if err != nil {
			a.serverError(w, r, err)
			return
		}
		p.D["invoices"] = invs
		for _, inv := range invs {
			if !inv.IsVoid() {
				cur := inv
				p.D["invoice"] = &cur
			}
		}
	}
	if p.Can("parts.view") {
		moves, _, err := repository.ListMovements(ctx, a.DB, repository.MovementFilter{JobID: j.ID}, repository.ListOpts{OrderBy: "m.id DESC", Limit: 100})
		if err != nil {
			a.serverError(w, r, err)
			return
		}
		p.D["movements"] = moves
	}
	if j.VehicleID.Valid && p.CanAny("customers.edit", "jobs.edit") {
		veh, err := repository.GetVehicle(ctx, a.DB, j.VehicleID.Int64)
		if err != nil {
			a.serverError(w, r, err)
			return
		}
		p.D["vehicle"] = veh
	}
	if p.Can("appointments.view") {
		if ap, err := repository.AppointmentForJob(ctx, a.DB, j.ID); err == nil {
			p.D["appt"] = ap
		}
	}
	p.D["statuses"] = models.JobStatuses
	a.render(w, r, "job_view", p)
}

func (a *App) jobEdit(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	j, err := repository.GetJob(ctx, a.DB, pathID(r, "id"))
	if err != nil {
		a.handleErr(w, r, err)
		return
	}
	if !service.CanAccessJob(mw.ActorFrom(r), j) {
		a.forbidden(w, r)
		return
	}
	c, err := repository.GetCustomer(ctx, a.DB, j.CustomerID)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	p := a.page(r, "Edit "+j.JobNumber, "jobs")
	if err := a.jobFormData(r, p, c, j); err != nil {
		a.serverError(w, r, err)
		return
	}
	a.render(w, r, "job_form", p)
}

func (a *App) jobUpdate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	old, err := repository.GetJob(ctx, a.DB, pathID(r, "id"))
	if err != nil {
		a.handleErr(w, r, err)
		return
	}
	actor := mw.ActorFrom(r)
	if !service.CanAccessJob(actor, old) {
		a.forbidden(w, r)
		return
	}
	j, v := jobFromForm(r, old)
	if !actor.Can("jobs.edit") {
		// Notes-only editors keep the original details.
		j.JobDate, j.VehicleID, j.AssignedUserID, j.ReportedIssue = old.JobDate, old.VehicleID, old.AssignedUserID, old.ReportedIssue
		delete(v, "job_date")
	}
	if len(v) == 0 {
		err := a.Svc.UpdateJob(ctx, actor, j)
		if err == nil {
			a.redirectFlash(w, r, jobURL(j.ID), "success", "Job saved.")
			return
		}
		msg, ok := service.IsUserError(err)
		if !ok {
			a.serverError(w, r, err)
			return
		}
		v["_"] = msg
	}
	c, _ := repository.GetCustomer(ctx, a.DB, old.CustomerID)
	p := a.page(r, "Edit "+old.JobNumber, "jobs")
	if err := a.jobFormData(r, p, c, j); err != nil {
		a.serverError(w, r, err)
		return
	}
	p.Errors = v
	if m := v["_"]; m != "" {
		p.FlashKind, p.FlashMsg = "error", m
	}
	a.render(w, r, "job_form", p, http.StatusUnprocessableEntity)
}

func (a *App) jobStatus(w http.ResponseWriter, r *http.Request) {
	id := pathID(r, "id")
	status := val(r, "status")
	err := a.Svc.ChangeJobStatus(r.Context(), mw.ActorFrom(r), id, status)
	a.done(w, r, jobURL(id), err, "Job status changed to "+status+".")
}

func (a *App) jobChargeAdd(w http.ResponseWriter, r *http.Request) {
	id := pathID(r, "id")
	v := V{}
	in := service.ChargeInput{CategoryID: formID(r, "category_id"), Description: val(r, "description"), Notes: val(r, "notes")}
	in.Quantity = v.Qty(r, "quantity", "Quantity", true, false)
	in.UnitPrice = v.OptMoney(r, "unit_price", "Unit price")
	v.MaxLen("description", in.Description, 200, "Description")
	if len(v) > 0 {
		a.redirectFlash(w, r, jobURL(id)+"#charges", "error", firstErr(v))
		return
	}
	err := a.Svc.AddJobCharge(r.Context(), mw.ActorFrom(r), id, in)
	a.done(w, r, jobURL(id)+"#charges", err, "Charge added.")
}

func (a *App) jobChargeUpdate(w http.ResponseWriter, r *http.Request) {
	id, cid := pathID(r, "id"), pathID(r, "cid")
	v := V{}
	in := service.ChargeInput{Description: val(r, "description"), Notes: val(r, "notes")}
	in.Quantity = v.Qty(r, "quantity", "Quantity", true, false)
	in.UnitPrice = v.OptMoney(r, "unit_price", "Unit price")
	if len(v) > 0 {
		a.redirectFlash(w, r, jobURL(id)+"#charges", "error", firstErr(v))
		return
	}
	err := a.Svc.UpdateJobCharge(r.Context(), mw.ActorFrom(r), id, cid, in)
	a.done(w, r, jobURL(id)+"#charges", err, "Charge updated.")
}

func (a *App) jobChargeDelete(w http.ResponseWriter, r *http.Request) {
	id, cid := pathID(r, "id"), pathID(r, "cid")
	err := a.Svc.DeleteJobCharge(r.Context(), mw.ActorFrom(r), id, cid)
	a.done(w, r, jobURL(id)+"#charges", err, "Charge removed.")
}

// partFromCode resolves "CODE" or "CODE — Name" typed into the part picker.
func (a *App) partFromCode(r *http.Request, field string) (models.Part, bool) {
	s := val(r, field)
	if i := strings.Index(s, " — "); i > 0 {
		s = s[:i]
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return models.Part{}, false
	}
	p, err := repository.GetPartByCode(r.Context(), a.DB, s)
	return p, err == nil
}

func (a *App) jobPartAdd(w http.ResponseWriter, r *http.Request) {
	id := pathID(r, "id")
	to := jobURL(id) + "#parts"
	part, ok := a.partFromCode(r, "part")
	if !ok {
		a.redirectFlash(w, r, to, "error", "Part not found. Choose a part code from the list.")
		return
	}
	v := V{}
	in := service.PartInput{PartID: part.ID, Notes: val(r, "notes"), AllowNegative: checked(r, "allow_negative")}
	in.Quantity = v.Qty(r, "quantity", "Quantity", true, false)
	in.UnitPrice = v.OptMoney(r, "unit_price", "Unit price")
	if len(v) > 0 {
		a.redirectFlash(w, r, to, "error", firstErr(v))
		return
	}
	err := a.Svc.AddJobPart(r.Context(), mw.ActorFrom(r), id, in)
	a.done(w, r, to, err, "Added "+in.Quantity.String()+" × "+part.Code+"; stock updated.")
}

func (a *App) jobPartUpdate(w http.ResponseWriter, r *http.Request) {
	id, pid := pathID(r, "id"), pathID(r, "pid")
	to := jobURL(id) + "#parts"
	v := V{}
	in := service.PartInput{Notes: val(r, "notes"), AllowNegative: checked(r, "allow_negative")}
	in.Quantity = v.Qty(r, "quantity", "Quantity", true, false)
	in.UnitPrice = v.OptMoney(r, "unit_price", "Unit price")
	if len(v) > 0 {
		a.redirectFlash(w, r, to, "error", firstErr(v))
		return
	}
	err := a.Svc.UpdateJobPart(r.Context(), mw.ActorFrom(r), id, pid, in)
	a.done(w, r, to, err, "Part line updated; stock adjusted by the difference.")
}

func (a *App) jobPartDelete(w http.ResponseWriter, r *http.Request) {
	id, pid := pathID(r, "id"), pathID(r, "pid")
	err := a.Svc.RemoveJobPart(r.Context(), mw.ActorFrom(r), id, pid)
	a.done(w, r, jobURL(id)+"#parts", err, "Part removed from job and returned to stock.")
}

func firstErr(v V) string {
	for _, k := range []string{"quantity", "unit_price", "description", "amount", "_"} {
		if m, ok := v[k]; ok {
			return m
		}
	}
	for _, m := range v {
		return m
	}
	return ""
}
