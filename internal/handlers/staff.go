package handlers

import (
	"database/sql"
	"net/http"
	"strconv"
	"time"

	mw "garagems/internal/middleware"
	"garagems/internal/models"
	"garagems/internal/repository"
	"garagems/internal/service"
)

func nowClock() string { return time.Now().In(models.Location()).Format(models.TimeOfDayLayout) }

// staffList shows today's workload for every active staff member.
func (a *App) staffList(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	today := models.Today()
	start := weekStart(today)
	stats, err := repository.StaffStats(ctx, a.DB, start, today, today)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	sheets, _, err := repository.ListTimesheets(ctx, a.DB, repository.TimesheetFilter{From: today, To: today}, repository.ListOpts{})
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	clock := map[int64]models.Timesheet{}
	for _, t := range sheets {
		clock[t.UserID] = t
	}
	var active []repository.StaffStat
	for _, s := range stats {
		if s.Active {
			active = append(active, s)
		}
	}
	p := a.page(r, "Staff", "staff")
	p.D["staff"], p.D["clock"], p.D["weekStart"] = active, clock, start
	a.render(w, r, "staff_list", p)
}

func (a *App) timesheetsList(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	s := mw.FromContext(ctx)
	all := s.Can("staff.view")
	start := weekStart(models.Today())
	from, to := dateParam(r, "from", start), dateParam(r, "to", addDays(start, 6))
	f := repository.TimesheetFilter{UserID: queryID(r, "user"), From: from, To: to}
	if !all {
		f.UserID = s.User.ID
	}
	list, _, err := repository.ListTimesheets(ctx, a.DB, f, repository.ListOpts{OrderBy: "t.work_date DESC, u.full_name COLLATE NOCASE", Limit: 1000})
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	type userTotal struct {
		Name    string
		Days    int
		Minutes int
	}
	var totals []userTotal
	pos := map[int64]int{}
	var grand int
	for _, t := range list {
		i, ok := pos[t.UserID]
		if !ok {
			i = len(totals)
			pos[t.UserID] = i
			totals = append(totals, userTotal{Name: t.UserName})
		}
		totals[i].Days++
		totals[i].Minutes += t.WorkedMin()
		grand += t.WorkedMin()
	}
	title := "Timesheets"
	if !all {
		title = "My timesheet"
	}
	p := a.page(r, title, "staff")
	if !all {
		p.Active = "timesheet"
	}
	p.D["sheets"], p.D["totals"], p.D["grand"], p.D["from"], p.D["to"], p.D["user"], p.D["all"] = list, totals, grand, from, to, f.UserID, all
	if all {
		users, err := repository.ListUsers(ctx, a.DB, false)
		if err != nil {
			a.serverError(w, r, err)
			return
		}
		p.D["users"] = users
	}
	if s.Can("timesheet.self") {
		if t, err := repository.TimesheetFor(ctx, a.DB, s.User.ID, models.Today()); err == nil {
			p.D["mine"] = t
		}
	}
	a.render(w, r, "timesheets_list", p)
}

func (a *App) timesheetFormPage(r *http.Request, t models.Timesheet) (*Page, error) {
	title := "Add timesheet entry"
	if t.ID != 0 {
		title = "Correct timesheet"
	}
	p := a.page(r, title, "staff")
	users, err := repository.ListUsers(r.Context(), a.DB, true)
	if err != nil {
		return nil, err
	}
	p.D["t"], p.D["users"] = t, users
	return p, nil
}

func (a *App) timesheetNew(w http.ResponseWriter, r *http.Request) {
	p, err := a.timesheetFormPage(r, models.Timesheet{WorkDate: models.Today(), UserID: queryID(r, "user"), TimeIn: "08:00",
		TimeOut: sql.NullString{String: "17:00", Valid: true}, BreakMin: 60})
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	a.render(w, r, "timesheet_form", p)
}

func (a *App) timesheetEdit(w http.ResponseWriter, r *http.Request) {
	t, err := repository.GetTimesheet(r.Context(), a.DB, pathID(r, "id"))
	if err != nil {
		a.handleErr(w, r, err)
		return
	}
	p, err := a.timesheetFormPage(r, t)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	a.render(w, r, "timesheet_form", p)
}

func timesheetFromForm(r *http.Request, t models.Timesheet) (models.Timesheet, V) {
	v := V{}
	if t.ID == 0 {
		t.UserID = formID(r, "user_id")
		if t.UserID == 0 {
			v.set("user_id", "Choose a staff member.")
		}
		t.WorkDate = val(r, "work_date")
		v.Date("work_date", t.WorkDate, "Date")
		if t.WorkDate > models.Today() {
			v.set("work_date", "Timesheets cannot be entered for future dates.")
		}
	}
	t.TimeIn = val(r, "time_in")
	if !models.ValidTimeOfDay(t.TimeIn) {
		v.set("time_in", "Enter the start time (HH:MM).")
	}
	out := val(r, "time_out")
	t.TimeOut = sql.NullString{String: out, Valid: out != ""}
	if out != "" && !models.ValidTimeOfDay(out) {
		v.set("time_out", "Enter the finish time (HH:MM), or leave it blank if still working.")
	}
	b := v.OptInt(r, "break_min", "Break", 0, 720)
	t.BreakMin = int(b.Int64)
	t.Notes = val(r, "notes")
	v.MaxLen("notes", t.Notes, 500, "Notes")
	if len(v) == 0 && t.TimeOut.Valid && t.WorkedMin() == 0 {
		v.set("break_min", "The break is as long as (or longer than) the time worked.")
	}
	return t, v
}

func (a *App) timesheetCreate(w http.ResponseWriter, r *http.Request) {
	t, v := timesheetFromForm(r, models.Timesheet{})
	a.timesheetSave(w, r, t, v)
}

func (a *App) timesheetUpdate(w http.ResponseWriter, r *http.Request) {
	old, err := repository.GetTimesheet(r.Context(), a.DB, pathID(r, "id"))
	if err != nil {
		a.handleErr(w, r, err)
		return
	}
	t, v := timesheetFromForm(r, old)
	a.timesheetSave(w, r, t, v)
}

func (a *App) timesheetSave(w http.ResponseWriter, r *http.Request, t models.Timesheet, v V) {
	if len(v) == 0 {
		_, err := a.Svc.SaveTimesheet(r.Context(), mw.ActorFrom(r), t)
		if err == nil {
			a.redirectFlash(w, r, "/staff/timesheets?from="+weekStart(t.WorkDate)+"&to="+addDays(weekStart(t.WorkDate), 6), "success", "Timesheet saved.")
			return
		}
		msg, ok := service.IsUserError(err)
		if !ok {
			a.serverError(w, r, err)
			return
		}
		v["_"] = msg
	}
	p, err := a.timesheetFormPage(r, t)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	p.Errors = v
	if m := v["_"]; m != "" {
		p.FlashKind, p.FlashMsg = "error", m
	}
	a.render(w, r, "timesheet_form", p, http.StatusUnprocessableEntity)
}

func (a *App) clockIn(w http.ResponseWriter, r *http.Request) {
	now := nowClock()
	err := a.Svc.ClockIn(r.Context(), mw.ActorFrom(r), now)
	a.done(w, r, safeNext(val(r, "next")), err, "Clocked in at "+now+".")
}

func (a *App) clockOut(w http.ResponseWriter, r *http.Request) {
	now := nowClock()
	brk, _ := strconv.Atoi(val(r, "break_min"))
	if brk < 0 || brk > 720 {
		brk = 0
	}
	err := a.Svc.ClockOut(r.Context(), mw.ActorFrom(r), now, brk)
	a.done(w, r, safeNext(val(r, "next")), err, "Clocked out at "+now+".")
}
