package handlers

import (
	"database/sql"
	"net/http"
	"strconv"
	"strings"
	"time"

	mw "garagems/internal/middleware"
	"garagems/internal/models"
	"garagems/internal/repository"
	"garagems/internal/service"
)

var apptSorts = map[string]string{
	"date":     "a.appt_date, a.start_time",
	"customer": "COALESCE(c.name, a.contact_name) COLLATE NOCASE",
	"status":   "a.status",
	"assigned": "u.full_name",
}

// ApptDurations are the booking lengths offered on the form, in minutes.
var ApptDurations = []int{15, 30, 45, 60, 90, 120, 180, 240, 360, 480}

func apptURL(id int64) string { return "/appointments/" + strconv.FormatInt(id, 10) }

type diaryDay struct {
	Date     string
	Count    int
	Selected bool
	IsToday  bool
}

func (a *App) appointmentsList(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := a.page(r, "Appointments", "appointments")
	users, err := repository.ListUsers(ctx, a.DB, true)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	p.D["users"], p.D["statuses"] = users, models.ApptStatuses
	assigned := queryID(r, "assigned")
	p.D["assigned"] = assigned

	if r.URL.Query().Get("view") == "list" {
		l := newList(r, apptSorts, "date", "asc")
		f := repository.ApptFilter{Q: l.Get("q"), Status: l.Get("status"), From: dateParam(r, "from", ""), To: dateParam(r, "to", ""), AssignedID: assigned}
		if f.Status != "" && f.Status != "open" && !models.ValidApptStatus(f.Status) {
			f.Status = ""
		}
		list, total, err := repository.ListAppointments(ctx, a.DB, f, l.Opts())
		if err != nil {
			a.serverError(w, r, err)
			return
		}
		l.Total = total
		p.D["list"], p.D["appts"], p.D["view"] = l, list, "list"
		a.render(w, r, "appointments_list", p)
		return
	}

	day := dateParam(r, "date", models.Today())
	start := weekStart(day)
	end := addDays(start, 6)
	week, _, err := repository.ListAppointments(ctx, a.DB, repository.ApptFilter{From: start, To: end, AssignedID: assigned}, repository.ListOpts{OrderBy: "a.appt_date, a.start_time, a.id"})
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	counts := map[string]int{}
	var todays []models.Appointment
	for _, ap := range week {
		if ap.Status == models.ApptScheduled || ap.Status == models.ApptCheckedIn {
			counts[ap.ApptDate]++
		}
		if ap.ApptDate == day {
			todays = append(todays, ap)
		}
	}
	var days []diaryDay
	for i := 0; i < 7; i++ {
		d := addDays(start, i)
		days = append(days, diaryDay{Date: d, Count: counts[d], Selected: d == day, IsToday: d == models.Today()})
	}
	p.D["view"], p.D["day"], p.D["days"], p.D["appts"] = "day", day, days, todays
	p.D["prevWeek"], p.D["nextWeek"] = addDays(start, -7), addDays(start, 7)
	a.render(w, r, "appointments_list", p)
}

func (a *App) appointmentNew(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := r.URL.Query()
	p := a.page(r, "Book appointment", "appointments")
	ap := models.Appointment{ApptDate: dateParam(r, "date", models.Today()), DurationMin: 60}
	if t := q.Get("time"); models.ValidTimeOfDay(t) {
		ap.StartTime = t
	}
	cid := queryID(r, "customer_id")
	if cid == 0 && q.Get("new") != "1" {
		// Step 1: find an existing customer, or continue with a new caller.
		s := strings.TrimSpace(q.Get("q"))
		p.D["q"], p.D["date"] = s, ap.ApptDate
		if s != "" {
			vs, _, err := repository.ListVehicles(ctx, a.DB, repository.VehicleFilter{Q: s}, repository.ListOpts{OrderBy: "v.registration_norm", Limit: 20})
			if err != nil {
				a.serverError(w, r, err)
				return
			}
			cs, err := repository.SearchCustomersQuick(ctx, a.DB, s, 20)
			if err != nil {
				a.serverError(w, r, err)
				return
			}
			p.D["vehicles"], p.D["customers"] = vs, cs
		}
		a.render(w, r, "appointment_find", p)
		return
	}
	ap.CustomerID = repository.NullInt(cid)
	ap.VehicleID = repository.NullInt(queryID(r, "vehicle_id"))
	if err := a.apptFormData(r, p, ap); err != nil {
		a.handleErr(w, r, err)
		return
	}
	a.render(w, r, "appointment_form", p)
}

func (a *App) apptFormData(r *http.Request, p *Page, ap models.Appointment) error {
	ctx := r.Context()
	if ap.CustomerID.Valid {
		c, err := repository.GetCustomer(ctx, a.DB, ap.CustomerID.Int64)
		if err != nil {
			return err
		}
		vehicles, _, err := repository.ListVehicles(ctx, a.DB, repository.VehicleFilter{CustomerID: c.ID}, repository.ListOpts{OrderBy: "v.registration_norm"})
		if err != nil {
			return err
		}
		p.D["c"], p.D["vehicles"] = c, vehicles
	}
	users, err := repository.ListUsers(ctx, a.DB, true)
	if err != nil {
		return err
	}
	p.D["ap"], p.D["users"], p.D["durations"] = ap, users, ApptDurations
	if ap.AssignedUserID.Valid && models.ValidDate(ap.ApptDate) {
		booked, err := repository.StaffBookings(ctx, a.DB, ap.AssignedUserID.Int64, ap.ApptDate)
		if err != nil {
			return err
		}
		p.D["booked"] = booked
	}
	return nil
}

func apptFromForm(r *http.Request, ap models.Appointment) (models.Appointment, V) {
	v := V{}
	ap.ApptDate = val(r, "appt_date")
	v.Date("appt_date", ap.ApptDate, "Date")
	ap.StartTime = val(r, "start_time")
	if !models.ValidTimeOfDay(ap.StartTime) {
		v.set("start_time", "Enter a start time (HH:MM).")
	}
	ap.DurationMin, _ = strconv.Atoi(val(r, "duration_min"))
	if ap.DurationMin < 5 || ap.DurationMin > 1440 {
		v.set("duration_min", "Choose how long the work will take.")
	} else if m := models.Minutes(ap.StartTime); m >= 0 && m+ap.DurationMin > 1440 {
		v.set("duration_min", "The appointment must end by midnight.")
	}
	ap.Service = val(r, "service")
	v.Required("service", ap.Service, "Service / reason for visit")
	v.MaxLen("service", ap.Service, 200, "Service / reason for visit")
	ap.Notes = val(r, "notes")
	v.MaxLen("notes", ap.Notes, 2000, "Notes")
	ap.AssignedUserID = repository.NullInt(formID(r, "assigned_user_id"))
	ap.CustomerID = repository.NullInt(formID(r, "customer_id"))
	if ap.CustomerID.Valid {
		ap.VehicleID = repository.NullInt(formID(r, "vehicle_id"))
	} else {
		ap.VehicleID = sql.NullInt64{}
		ap.ContactName, ap.ContactPhone, ap.VehicleReg = val(r, "contact_name"), val(r, "contact_phone"), val(r, "vehicle_reg")
		v.Required("contact_name", ap.ContactName, "Name")
		v.MaxLen("contact_name", ap.ContactName, 120, "Name")
		v.Required("contact_phone", ap.ContactPhone, "Phone number")
		v.MaxLen("contact_phone", ap.ContactPhone, 40, "Phone number")
		if ap.ContactPhone != "" && len(models.DigitsOnly(ap.ContactPhone)) < 7 {
			v.set("contact_phone", "Enter a valid phone number (at least 7 digits).")
		}
		v.MaxLen("vehicle_reg", ap.VehicleReg, 30, "Registration number")
	}
	return ap, v
}

func (a *App) appointmentCreate(w http.ResponseWriter, r *http.Request) {
	ap, v := apptFromForm(r, models.Appointment{})
	a.apptSave(w, r, ap, v, "Book appointment")
}

func (a *App) apptSave(w http.ResponseWriter, r *http.Request, ap models.Appointment, v V, title string) {
	if len(v) == 0 {
		id, err := a.Svc.SaveAppointment(r.Context(), mw.ActorFrom(r), ap)
		if err == nil {
			msg := "Appointment booked for " + a.fmtDate(ap.ApptDate) + " at " + ap.StartTime + "."
			if ap.ID != 0 {
				msg = "Appointment updated."
			}
			a.redirectFlash(w, r, apptURL(id), "success", msg)
			return
		}
		msg, ok := service.IsUserError(err)
		if !ok {
			a.serverError(w, r, err)
			return
		}
		v["_"] = msg
	}
	p := a.page(r, title, "appointments")
	if err := a.apptFormData(r, p, ap); err != nil {
		a.handleErr(w, r, err)
		return
	}
	p.Errors = v
	if m := v["_"]; m != "" {
		p.FlashKind, p.FlashMsg = "error", m
	}
	a.render(w, r, "appointment_form", p, http.StatusUnprocessableEntity)
}

func (a *App) appointmentView(w http.ResponseWriter, r *http.Request) {
	ap, err := repository.GetAppointment(r.Context(), a.DB, pathID(r, "id"))
	if err != nil {
		a.handleErr(w, r, err)
		return
	}
	p := a.page(r, "Appointment", "appointments")
	p.D["ap"] = ap
	a.render(w, r, "appointment_view", p)
}

func (a *App) appointmentEdit(w http.ResponseWriter, r *http.Request) {
	ap, err := repository.GetAppointment(r.Context(), a.DB, pathID(r, "id"))
	if err != nil {
		a.handleErr(w, r, err)
		return
	}
	if !ap.Open() {
		a.redirectFlash(w, r, apptURL(ap.ID), "error", "Only scheduled appointments can be changed.")
		return
	}
	p := a.page(r, "Edit appointment", "appointments")
	if err := a.apptFormData(r, p, ap); err != nil {
		a.handleErr(w, r, err)
		return
	}
	a.render(w, r, "appointment_form", p)
}

func (a *App) appointmentUpdate(w http.ResponseWriter, r *http.Request) {
	old, err := repository.GetAppointment(r.Context(), a.DB, pathID(r, "id"))
	if err != nil {
		a.handleErr(w, r, err)
		return
	}
	ap, v := apptFromForm(r, models.Appointment{ID: old.ID})
	a.apptSave(w, r, ap, v, "Edit appointment")
}

func (a *App) appointmentClose(w http.ResponseWriter, r *http.Request) {
	id := pathID(r, "id")
	status := val(r, "status")
	err := a.Svc.CloseAppointment(r.Context(), mw.ActorFrom(r), id, status, val(r, "reason"))
	a.done(w, r, apptURL(id), err, "Appointment marked as "+strings.ToLower(status)+".")
}

func (a *App) appointmentReinstate(w http.ResponseWriter, r *http.Request) {
	id := pathID(r, "id")
	err := a.Svc.ReinstateAppointment(r.Context(), mw.ActorFrom(r), id)
	a.done(w, r, apptURL(id), err, "Appointment is back in the diary.")
}

func (a *App) appointmentCheckIn(w http.ResponseWriter, r *http.Request) {
	id := pathID(r, "id")
	jobID, err := a.Svc.CheckInAppointment(r.Context(), mw.ActorFrom(r), id, a.Settings().JobPrefix)
	if err != nil {
		a.done(w, r, apptURL(id), err, "")
		return
	}
	a.redirectFlash(w, r, jobURL(jobID), "success", "Customer checked in and job opened. Add charges and parts below.")
}

// ---------- dates ----------

// fmtDate formats a business date with the configured date format.
func (a *App) fmtDate(date string) string {
	t, err := time.Parse(models.DateLayout, date)
	if err != nil {
		return date
	}
	return t.Format(a.Settings().DateFormat)
}

func addDays(date string, n int) string {
	t, err := time.Parse(models.DateLayout, date)
	if err != nil {
		return date
	}
	return t.AddDate(0, 0, n).Format(models.DateLayout)
}

// weekStart returns the Monday on or before date.
func weekStart(date string) string {
	t, err := time.Parse(models.DateLayout, date)
	if err != nil {
		return date
	}
	return t.AddDate(0, 0, -((int(t.Weekday()) + 6) % 7)).Format(models.DateLayout)
}
