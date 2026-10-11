package handlers

import (
	"net/http"
	"strconv"

	"garagems/internal/models"
	"garagems/internal/repository"
)

var customerSorts = map[string]string{
	"name":    "c.name COLLATE NOCASE",
	"phone":   "c.phone",
	"created": "c.id",
	"balance": "(SELECT COALESCE(SUM(i.total - i.amount_paid), 0) FROM invoices i WHERE i.customer_id = c.id AND i.status = 'Issued')",
}

func (a *App) customersList(w http.ResponseWriter, r *http.Request) {
	l := newList(r, customerSorts, "name", "asc")
	list, total, err := repository.ListCustomers(r.Context(), a.DB, repository.CustomerFilter{Q: l.Get("q"), Archived: l.Get("archived")}, l.Opts())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	l.Total = total
	p := a.page(r, "Customers", "customers")
	p.D["list"], p.D["customers"] = l, list
	a.render(w, r, "customers_list", p)
}

func customerFromForm(r *http.Request) (models.Customer, V) {
	c := models.Customer{Name: val(r, "name"), Phone: val(r, "phone"), Email: val(r, "email"), Address: val(r, "address"), Notes: val(r, "notes")}
	v := V{}
	v.Required("name", c.Name, "Name")
	v.MaxLen("name", c.Name, 120, "Name")
	v.Required("phone", c.Phone, "Phone number")
	v.MaxLen("phone", c.Phone, 40, "Phone number")
	if c.Phone != "" && len(models.DigitsOnly(c.Phone)) < 7 {
		v["phone"] = "Enter a valid phone number (at least 7 digits)."
	}
	v.Email("email", c.Email)
	v.MaxLen("address", c.Address, 500, "Address")
	v.MaxLen("notes", c.Notes, 2000, "Notes")
	return c, v
}

func (a *App) customerNew(w http.ResponseWriter, r *http.Request) {
	p := a.page(r, "New customer", "customers")
	p.D["c"] = models.Customer{}
	a.render(w, r, "customer_form", p)
}

func (a *App) customerCreate(w http.ResponseWriter, r *http.Request) {
	c, v := customerFromForm(r)
	if len(v) > 0 {
		p := a.page(r, "New customer", "customers")
		p.D["c"], p.Errors = c, v
		a.render(w, r, "customer_form", p, http.StatusUnprocessableEntity)
		return
	}
	id, err := repository.CreateCustomer(r.Context(), a.DB, c)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	a.audit(r, "customer.create", "customer", id, c.Name)
	a.redirectFlash(w, r, "/customers/"+strconv.FormatInt(id, 10), "success", "Customer created. Add a vehicle next.")
}

func (a *App) customerView(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := pathID(r, "id")
	c, err := repository.GetCustomer(ctx, a.DB, id)
	if err != nil {
		a.handleErr(w, r, err)
		return
	}
	p := a.page(r, c.Name, "customers")
	p.D["c"] = c
	vehicles, _, err := repository.ListVehicles(ctx, a.DB, repository.VehicleFilter{CustomerID: id, Archived: "all"}, repository.ListOpts{OrderBy: "v.archived, v.registration"})
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	p.D["vehicles"] = vehicles
	if p.Can("jobs.view_all") {
		jobs, _, err := repository.ListJobs(ctx, a.DB, repository.JobFilter{CustomerID: id}, repository.ListOpts{OrderBy: "j.job_date DESC, j.id DESC", Limit: 100})
		if err != nil {
			a.serverError(w, r, err)
			return
		}
		p.D["jobs"] = jobs
	}
	if p.Can("invoices.view") {
		invs, totals, err := repository.ListInvoices(ctx, a.DB, repository.InvoiceFilter{CustomerID: id, Status: "all"}, repository.ListOpts{OrderBy: "i.invoice_date DESC, i.id DESC", Limit: 100})
		if err != nil {
			a.serverError(w, r, err)
			return
		}
		p.D["invoices"], p.D["invTotals"] = invs, totals
	}
	a.render(w, r, "customer_view", p)
}

func (a *App) customerEdit(w http.ResponseWriter, r *http.Request) {
	c, err := repository.GetCustomer(r.Context(), a.DB, pathID(r, "id"))
	if err != nil {
		a.handleErr(w, r, err)
		return
	}
	p := a.page(r, "Edit customer", "customers")
	p.D["c"] = c
	a.render(w, r, "customer_form", p)
}

func (a *App) customerUpdate(w http.ResponseWriter, r *http.Request) {
	id := pathID(r, "id")
	if _, err := repository.GetCustomer(r.Context(), a.DB, id); err != nil {
		a.handleErr(w, r, err)
		return
	}
	c, v := customerFromForm(r)
	c.ID = id
	if len(v) > 0 {
		p := a.page(r, "Edit customer", "customers")
		p.D["c"], p.Errors = c, v
		a.render(w, r, "customer_form", p, http.StatusUnprocessableEntity)
		return
	}
	if err := repository.UpdateCustomer(r.Context(), a.DB, c); err != nil {
		a.serverError(w, r, err)
		return
	}
	a.audit(r, "customer.update", "customer", id, c.Name)
	a.redirectFlash(w, r, "/customers/"+strconv.FormatInt(id, 10), "success", "Customer saved.")
}

func (a *App) customerArchive(w http.ResponseWriter, r *http.Request) {
	id := pathID(r, "id")
	c, err := repository.GetCustomer(r.Context(), a.DB, id)
	if err != nil {
		a.handleErr(w, r, err)
		return
	}
	archive := r.PostFormValue("archived") == "1"
	if err := repository.SetCustomerArchived(r.Context(), a.DB, id, archive); err != nil {
		a.serverError(w, r, err)
		return
	}
	msg, act := "Customer restored.", "customer.restore"
	if archive {
		msg, act = "Customer archived. Their history is kept.", "customer.archive"
	}
	a.audit(r, act, "customer", id, c.Name)
	a.redirectFlash(w, r, "/customers/"+strconv.FormatInt(id, 10), "success", msg)
}

// ---------- vehicles ----------

var vehicleSorts = map[string]string{
	"reg":      "v.registration_norm",
	"make":     "v.make COLLATE NOCASE, v.model COLLATE NOCASE",
	"year":     "v.year",
	"customer": "c.name COLLATE NOCASE",
	"created":  "v.id",
}

func (a *App) vehiclesList(w http.ResponseWriter, r *http.Request) {
	l := newList(r, vehicleSorts, "reg", "asc")
	list, total, err := repository.ListVehicles(r.Context(), a.DB, repository.VehicleFilter{Q: l.Get("q"), Archived: l.Get("archived")}, l.Opts())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	l.Total = total
	p := a.page(r, "Vehicles", "vehicles")
	p.D["list"], p.D["vehicles"] = l, list
	a.render(w, r, "vehicles_list", p)
}

func vehicleFromForm(r *http.Request) (models.Vehicle, V) {
	v := V{}
	veh := models.Vehicle{Registration: val(r, "registration"), Make: val(r, "make"), Model: val(r, "model"), VIN: val(r, "vin"), Notes: val(r, "notes")}
	v.Required("registration", veh.Registration, "Registration number")
	if veh.Registration != "" && models.NormalizeReg(veh.Registration) == "" {
		v["registration"] = "Registration number must contain letters or digits."
	}
	v.MaxLen("registration", veh.Registration, 30, "Registration number")
	v.MaxLen("make", veh.Make, 60, "Make")
	v.MaxLen("model", veh.Model, 60, "Model")
	v.MaxLen("vin", veh.VIN, 40, "VIN/chassis number")
	v.MaxLen("notes", veh.Notes, 2000, "Notes")
	veh.Year = v.OptInt(r, "year", "Year", 1900, 2100)
	return veh, v
}

func (a *App) vehicleNew(w http.ResponseWriter, r *http.Request) {
	c, err := repository.GetCustomer(r.Context(), a.DB, pathID(r, "id"))
	if err != nil {
		a.handleErr(w, r, err)
		return
	}
	p := a.page(r, "Add vehicle", "vehicles")
	p.D["c"], p.D["v"] = c, models.Vehicle{CustomerID: c.ID}
	a.render(w, r, "vehicle_form", p)
}

func (a *App) vehicleCreate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	c, err := repository.GetCustomer(ctx, a.DB, pathID(r, "id"))
	if err != nil {
		a.handleErr(w, r, err)
		return
	}
	veh, v := vehicleFromForm(r)
	veh.CustomerID = c.ID
	if _, ok := v["registration"]; !ok {
		if taken, err := repository.RegistrationTaken(ctx, a.DB, veh.Registration, 0); err != nil {
			a.serverError(w, r, err)
			return
		} else if taken {
			v["registration"] = "A vehicle with this registration number already exists. Search for it under Vehicles."
		}
	}
	if len(v) > 0 {
		p := a.page(r, "Add vehicle", "vehicles")
		p.D["c"], p.D["v"], p.Errors = c, veh, v
		a.render(w, r, "vehicle_form", p, http.StatusUnprocessableEntity)
		return
	}
	id, err := repository.CreateVehicle(ctx, a.DB, veh)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	a.audit(r, "vehicle.create", "vehicle", id, veh.Registration+" for "+c.Name)
	a.redirectFlash(w, r, "/customers/"+strconv.FormatInt(c.ID, 10), "success", "Vehicle "+veh.Registration+" added.")
}

func (a *App) vehicleView(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	veh, err := repository.GetVehicle(ctx, a.DB, pathID(r, "id"))
	if err != nil {
		a.handleErr(w, r, err)
		return
	}
	p := a.page(r, veh.Registration, "vehicles")
	p.D["v"] = veh
	if p.Can("jobs.view_all") {
		jobs, _, err := repository.ListJobs(ctx, a.DB, repository.JobFilter{VehicleID: veh.ID}, repository.ListOpts{OrderBy: "j.job_date DESC, j.id DESC", Limit: 200})
		if err != nil {
			a.serverError(w, r, err)
			return
		}
		p.D["jobs"] = jobs
	}
	a.render(w, r, "vehicle_view", p)
}

func (a *App) vehicleEdit(w http.ResponseWriter, r *http.Request) {
	veh, err := repository.GetVehicle(r.Context(), a.DB, pathID(r, "id"))
	if err != nil {
		a.handleErr(w, r, err)
		return
	}
	c, _ := repository.GetCustomer(r.Context(), a.DB, veh.CustomerID)
	p := a.page(r, "Edit vehicle", "vehicles")
	p.D["c"], p.D["v"] = c, veh
	a.render(w, r, "vehicle_form", p)
}

func (a *App) vehicleUpdate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	old, err := repository.GetVehicle(ctx, a.DB, pathID(r, "id"))
	if err != nil {
		a.handleErr(w, r, err)
		return
	}
	veh, v := vehicleFromForm(r)
	veh.ID, veh.CustomerID = old.ID, old.CustomerID
	// Ownership transfer: optional new customer ID.
	if nc := formID(r, "customer_id"); nc != 0 && nc != old.CustomerID {
		if _, err := repository.GetCustomer(ctx, a.DB, nc); err != nil {
			v["customer_id"] = "Customer not found."
		} else {
			veh.CustomerID = nc
		}
	}
	if _, ok := v["registration"]; !ok && !old.Archived {
		if taken, err := repository.RegistrationTaken(ctx, a.DB, veh.Registration, veh.ID); err != nil {
			a.serverError(w, r, err)
			return
		} else if taken {
			v["registration"] = "Another vehicle already uses this registration number."
		}
	}
	if len(v) > 0 {
		c, _ := repository.GetCustomer(ctx, a.DB, old.CustomerID)
		p := a.page(r, "Edit vehicle", "vehicles")
		p.D["c"], p.D["v"], p.Errors = c, veh, v
		a.render(w, r, "vehicle_form", p, http.StatusUnprocessableEntity)
		return
	}
	if err := repository.UpdateVehicle(ctx, a.DB, veh); err != nil {
		a.serverError(w, r, err)
		return
	}
	detail := veh.Registration
	if veh.CustomerID != old.CustomerID {
		detail += " transferred from customer " + strconv.FormatInt(old.CustomerID, 10) + " to " + strconv.FormatInt(veh.CustomerID, 10)
	}
	a.audit(r, "vehicle.update", "vehicle", veh.ID, detail)
	a.redirectFlash(w, r, "/vehicles/"+strconv.FormatInt(veh.ID, 10), "success", "Vehicle saved.")
}

func (a *App) vehicleArchive(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	veh, err := repository.GetVehicle(ctx, a.DB, pathID(r, "id"))
	if err != nil {
		a.handleErr(w, r, err)
		return
	}
	archive := r.PostFormValue("archived") == "1"
	to := "/vehicles/" + strconv.FormatInt(veh.ID, 10)
	if !archive {
		if taken, err := repository.RegistrationTaken(ctx, a.DB, veh.Registration, veh.ID); err == nil && taken {
			a.redirectFlash(w, r, to, "error", "Cannot restore: another active vehicle uses this registration number.")
			return
		}
	}
	if err := repository.SetVehicleArchived(ctx, a.DB, veh.ID, archive); err != nil {
		a.serverError(w, r, err)
		return
	}
	msg, act := "Vehicle restored.", "vehicle.restore"
	if archive {
		msg, act = "Vehicle archived. Its job history is kept.", "vehicle.archive"
	}
	a.audit(r, act, "vehicle", veh.ID, veh.Registration)
	a.redirectFlash(w, r, to, "success", msg)
}
