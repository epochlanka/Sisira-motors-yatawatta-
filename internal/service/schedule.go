package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"garagems/internal/models"
	"garagems/internal/repository"
)

// ---------- appointments ----------

// SaveAppointment books a new appointment (in.ID == 0) or updates a scheduled one.
// Linked customer and vehicle details are copied onto the booking, and the assigned
// staff member cannot be double-booked.
func (s *Service) SaveAppointment(ctx context.Context, a models.Actor, in models.Appointment) (int64, error) {
	if err := need(a, "appointments.edit"); err != nil {
		return 0, err
	}
	id := in.ID
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		if in.ID != 0 {
			old, err := repository.GetAppointment(ctx, tx, in.ID)
			if err != nil {
				return err
			}
			if !old.Open() {
				return Fail("Only scheduled appointments can be changed. This one is %s.", strings.ToLower(old.Status))
			}
		} else if in.ApptDate < models.Today() {
			return Fail("Choose today or a future date for a new appointment.")
		}
		if in.CustomerID.Valid {
			c, err := repository.GetCustomer(ctx, tx, in.CustomerID.Int64)
			if err != nil {
				return err
			}
			if c.Archived {
				return Fail("Customer %s is archived. Restore the customer before booking.", c.Name)
			}
			in.ContactName, in.ContactPhone = c.Name, c.Phone
		} else if in.VehicleID.Valid {
			return Fail("Choose the customer for the selected vehicle.")
		}
		if in.VehicleID.Valid {
			v, err := repository.GetVehicle(ctx, tx, in.VehicleID.Int64)
			if err != nil {
				return err
			}
			if v.CustomerID != in.CustomerID.Int64 {
				return Fail("The selected vehicle does not belong to this customer.")
			}
			in.VehicleReg = v.Registration
		}
		if in.AssignedUserID.Valid {
			u, err := repository.GetUser(ctx, tx, in.AssignedUserID.Int64)
			if err != nil || !u.Active {
				return Fail("Select an active staff member.")
			}
			if err := checkDoubleBooking(ctx, tx, in, u.FullName); err != nil {
				return err
			}
		}
		var err error
		action := "appointment.update"
		if in.ID == 0 {
			action = "appointment.create"
			if id, err = repository.CreateAppointment(ctx, tx, in, a.ID); err != nil {
				return err
			}
		} else if err = repository.UpdateAppointment(ctx, tx, in); err != nil {
			return err
		}
		return repository.Audit(ctx, tx, a, action, "appointment", id,
			fmt.Sprintf("%s %s %s: %s", in.ApptDate, in.StartTime, in.ContactName, in.Service))
	})
	return id, err
}

func checkDoubleBooking(ctx context.Context, tx *sql.Tx, in models.Appointment, staff string) error {
	others, err := repository.StaffBookings(ctx, tx, in.AssignedUserID.Int64, in.ApptDate)
	if err != nil {
		return err
	}
	start := models.Minutes(in.StartTime)
	end := start + in.DurationMin
	for _, o := range others {
		if o.ID == in.ID {
			continue
		}
		os := models.Minutes(o.StartTime)
		if start < os+o.DurationMin && os < end {
			return Fail("%s is already booked %s–%s (%s, %s). Choose another time or staff member.",
				staff, o.StartTime, o.EndTime(), o.Name(), o.Service)
		}
	}
	return nil
}

// CloseAppointment marks a scheduled appointment as cancelled or a no-show.
func (s *Service) CloseAppointment(ctx context.Context, a models.Actor, id int64, status, reason string) error {
	if err := need(a, "appointments.edit"); err != nil {
		return err
	}
	if status != models.ApptCancelled && status != models.ApptNoShow {
		return Fail("Invalid status.")
	}
	if status == models.ApptCancelled && strings.TrimSpace(reason) == "" {
		return Fail("Enter a reason for cancelling.")
	}
	return s.Tx(ctx, func(tx *sql.Tx) error {
		ap, err := repository.GetAppointment(ctx, tx, id)
		if err != nil {
			return err
		}
		if !ap.Open() {
			return Fail("This appointment is already %s.", strings.ToLower(ap.Status))
		}
		if err := repository.SetAppointmentStatus(ctx, tx, id, status, reason, sql.NullInt64{}); err != nil {
			return err
		}
		return repository.Audit(ctx, tx, a, "appointment."+strings.ReplaceAll(strings.ToLower(status), " ", "_"), "appointment", id,
			fmt.Sprintf("%s %s %s: %s", ap.ApptDate, ap.StartTime, ap.Name(), reason))
	})
}

// ReinstateAppointment returns a cancelled or no-show appointment to the diary.
func (s *Service) ReinstateAppointment(ctx context.Context, a models.Actor, id int64) error {
	if err := need(a, "appointments.edit"); err != nil {
		return err
	}
	return s.Tx(ctx, func(tx *sql.Tx) error {
		ap, err := repository.GetAppointment(ctx, tx, id)
		if err != nil {
			return err
		}
		if ap.Status != models.ApptCancelled && ap.Status != models.ApptNoShow {
			return Fail("Only cancelled or no-show appointments can be reinstated.")
		}
		if ap.AssignedUserID.Valid {
			if err := checkDoubleBooking(ctx, tx, ap, ap.AssignedName); err != nil {
				return err
			}
		}
		if err := repository.SetAppointmentStatus(ctx, tx, id, models.ApptScheduled, "", sql.NullInt64{}); err != nil {
			return err
		}
		return repository.Audit(ctx, tx, a, "appointment.reinstate", "appointment", id, ap.ApptDate+" "+ap.StartTime+" "+ap.Name())
	})
}

// CheckInAppointment opens a job for an arriving customer in one transaction. A booking
// that is not linked to a customer record is matched by phone number, or a new customer
// (and vehicle, if a registration was given) is created from the booking details.
func (s *Service) CheckInAppointment(ctx context.Context, a models.Actor, id int64, jobPrefix string) (int64, error) {
	if err := need(a, "appointments.edit"); err != nil {
		return 0, err
	}
	if err := need(a, "jobs.create"); err != nil {
		return 0, err
	}
	var jobID int64
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		ap, err := repository.GetAppointment(ctx, tx, id)
		if err != nil {
			return err
		}
		if !ap.Open() {
			return Fail("This appointment is already %s.", strings.ToLower(ap.Status))
		}
		if !ap.CustomerID.Valid {
			if err := linkBookingCustomer(ctx, tx, a, &ap); err != nil {
				return err
			}
		}
		issue := ap.Service
		if ap.Notes != "" {
			issue += "\n" + ap.Notes
		}
		j := models.Job{CustomerID: ap.CustomerID.Int64, VehicleID: ap.VehicleID, JobDate: models.Today(),
			ReportedIssue: issue, AssignedUserID: ap.AssignedUserID}
		if j.AssignedUserID.Valid {
			if u, err := repository.GetUser(ctx, tx, j.AssignedUserID.Int64); err != nil || !u.Active {
				j.AssignedUserID = sql.NullInt64{}
			}
		}
		if jobID, err = createJobTx(ctx, tx, a, j, jobPrefix); err != nil {
			return err
		}
		if err := repository.SetAppointmentStatus(ctx, tx, id, models.ApptCheckedIn, "", repository.NullInt(jobID)); err != nil {
			return err
		}
		return repository.Audit(ctx, tx, a, "appointment.check_in", "appointment", id, ap.Name()+": "+ap.Service)
	})
	return jobID, err
}

func linkBookingCustomer(ctx context.Context, tx *sql.Tx, a models.Actor, ap *models.Appointment) error {
	if !a.Can("customers.edit") {
		return Fail("This booking is not linked to a customer record. Ask someone who can add customers to check it in.")
	}
	if len(models.DigitsOnly(ap.ContactPhone)) < 7 {
		return Fail("Add a valid phone number to the booking (or link it to a customer) before checking in.")
	}
	c, err := repository.FindCustomerByPhone(ctx, tx, ap.ContactPhone)
	switch {
	case err == nil:
	case errors.Is(err, repository.ErrNotFound):
		c = models.Customer{Name: ap.ContactName, Phone: ap.ContactPhone}
		if c.ID, err = repository.CreateCustomer(ctx, tx, c); err != nil {
			return err
		}
		if err := repository.Audit(ctx, tx, a, "customer.create", "customer", c.ID, c.Name+" (from appointment)"); err != nil {
			return err
		}
	default:
		return err
	}
	ap.CustomerID = repository.NullInt(c.ID)
	if ap.VehicleReg != "" && models.NormalizeReg(ap.VehicleReg) != "" {
		v, err := repository.FindVehicleByReg(ctx, tx, ap.VehicleReg)
		switch {
		case err == nil:
			if v.CustomerID != c.ID {
				return Fail("Vehicle %s is registered to %s. Edit the booking and link it to the right customer before checking in.", v.Registration, v.CustomerName)
			}
		case errors.Is(err, repository.ErrNotFound):
			v = models.Vehicle{CustomerID: c.ID, Registration: ap.VehicleReg}
			if v.ID, err = repository.CreateVehicle(ctx, tx, v); err != nil {
				return err
			}
			if err := repository.Audit(ctx, tx, a, "vehicle.create", "vehicle", v.ID, v.Registration+" for "+c.Name+" (from appointment)"); err != nil {
				return err
			}
		default:
			return err
		}
		ap.VehicleID = repository.NullInt(v.ID)
	}
	_, err = tx.ExecContext(ctx, "UPDATE appointments SET customer_id = ?, vehicle_id = ?, updated_at = ? WHERE id = ?",
		ap.CustomerID, ap.VehicleID, models.NowUTC(), ap.ID)
	return err
}

// ---------- timesheets ----------

// ClockIn starts today's timesheet for the actor.
func (s *Service) ClockIn(ctx context.Context, a models.Actor, now string) error {
	if err := need(a, "timesheet.self"); err != nil {
		return err
	}
	return s.Tx(ctx, func(tx *sql.Tx) error {
		today := models.Today()
		if t, err := repository.TimesheetFor(ctx, tx, a.ID, today); err == nil {
			if t.TimeOut.Valid {
				return Fail("You already clocked out today at %s. Ask a manager to correct your timesheet if needed.", t.TimeOut.String)
			}
			return Fail("You are already clocked in since %s.", t.TimeIn)
		} else if !errors.Is(err, repository.ErrNotFound) {
			return err
		}
		id, err := repository.CreateTimesheet(ctx, tx, models.Timesheet{UserID: a.ID, WorkDate: today, TimeIn: now}, a.ID)
		if err != nil {
			return err
		}
		return repository.Audit(ctx, tx, a, "timesheet.clock_in", "timesheet", id, today+" "+now)
	})
}

// ClockOut ends today's timesheet for the actor.
func (s *Service) ClockOut(ctx context.Context, a models.Actor, now string, breakMin int) error {
	if err := need(a, "timesheet.self"); err != nil {
		return err
	}
	return s.Tx(ctx, func(tx *sql.Tx) error {
		t, err := repository.TimesheetFor(ctx, tx, a.ID, models.Today())
		if errors.Is(err, repository.ErrNotFound) {
			return Fail("You have not clocked in today.")
		} else if err != nil {
			return err
		}
		if t.TimeOut.Valid {
			return Fail("You already clocked out today at %s.", t.TimeOut.String)
		}
		t.TimeOut = sql.NullString{String: now, Valid: true}
		t.BreakMin = breakMin
		if t.WorkedMin() == 0 && breakMin > 0 {
			return Fail("The break is longer than the time worked.")
		}
		if err := repository.UpdateTimesheet(ctx, tx, t); err != nil {
			return err
		}
		return repository.Audit(ctx, tx, a, "timesheet.clock_out", "timesheet", t.ID, fmt.Sprintf("%s %s–%s, break %d min", t.WorkDate, t.TimeIn, now, breakMin))
	})
}

// SaveTimesheet adds or corrects a timesheet entry for any staff member (managers).
func (s *Service) SaveTimesheet(ctx context.Context, a models.Actor, in models.Timesheet) (int64, error) {
	if err := need(a, "staff.timesheets"); err != nil {
		return 0, err
	}
	id := in.ID
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		u, err := repository.GetUser(ctx, tx, in.UserID)
		if err != nil {
			return err
		}
		if in.ID == 0 {
			if _, err := repository.TimesheetFor(ctx, tx, in.UserID, in.WorkDate); err == nil {
				return Fail("%s already has a timesheet for %s. Edit that entry instead.", u.FullName, in.WorkDate)
			} else if !errors.Is(err, repository.ErrNotFound) {
				return err
			}
			if id, err = repository.CreateTimesheet(ctx, tx, in, a.ID); err != nil {
				return err
			}
		} else {
			old, err := repository.GetTimesheet(ctx, tx, in.ID)
			if err != nil {
				return err
			}
			in.UserID, in.WorkDate = old.UserID, old.WorkDate
			if err := repository.UpdateTimesheet(ctx, tx, in); err != nil {
				return err
			}
		}
		return repository.Audit(ctx, tx, a, "timesheet.save", "timesheet", id,
			fmt.Sprintf("%s %s %s–%s, break %d min", u.FullName, in.WorkDate, in.TimeIn, in.Out(), in.BreakMin))
	})
	return id, err
}

// ---------- expenses ----------

func (s *Service) CreateExpense(ctx context.Context, a models.Actor, e models.Expense) (int64, error) {
	if err := need(a, "expenses.edit"); err != nil {
		return 0, err
	}
	var id int64
	err := s.Tx(ctx, func(tx *sql.Tx) (err error) {
		if id, err = repository.CreateExpense(ctx, tx, e, a.ID); err != nil {
			return err
		}
		return repository.Audit(ctx, tx, a, "expense.create", "expense", id,
			fmt.Sprintf("%s %s: %s %s", e.ExpenseDate, e.Category, e.Description, e.Amount))
	})
	return id, err
}

func (s *Service) VoidExpense(ctx context.Context, a models.Actor, id int64, reason string) error {
	if err := need(a, "expenses.void"); err != nil {
		return err
	}
	if strings.TrimSpace(reason) == "" {
		return Fail("Enter a reason for voiding this expense.")
	}
	return s.Tx(ctx, func(tx *sql.Tx) error {
		e, err := repository.GetExpense(ctx, tx, id)
		if err != nil {
			return err
		}
		if e.IsVoid() {
			return Fail("This expense is already void.")
		}
		if err := repository.VoidExpense(ctx, tx, id, reason, a.ID); err != nil {
			return err
		}
		return repository.Audit(ctx, tx, a, "expense.void", "expense", id, fmt.Sprintf("%s %s: %s", e.Description, e.Amount, reason))
	})
}
