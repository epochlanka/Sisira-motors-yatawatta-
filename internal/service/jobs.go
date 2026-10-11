package service

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"garagems/internal/models"
	"garagems/internal/repository"
)

// CanAccessJob enforces "assigned jobs only" for users without jobs.view_all.
func CanAccessJob(a models.Actor, j models.Job) bool {
	if a.Can("jobs.view_all") {
		return true
	}
	return a.Can("jobs.view_assigned") && j.AssignedUserID.Valid && j.AssignedUserID.Int64 == a.ID
}

func (s *Service) loadJob(ctx context.Context, q repository.DBTX, a models.Actor, jobID int64) (models.Job, error) {
	j, err := repository.GetJob(ctx, q, jobID)
	if err != nil {
		return j, err
	}
	if !CanAccessJob(a, j) {
		return j, Fail("You do not have access to this job.")
	}
	return j, nil
}

// editableJob loads a job whose charges/parts may be changed: not cancelled and not invoiced.
func (s *Service) editableJob(ctx context.Context, tx *sql.Tx, a models.Actor, jobID int64) (models.Job, error) {
	j, err := s.loadJob(ctx, tx, a, jobID)
	if err != nil {
		return j, err
	}
	if j.Status == models.JobCancelled {
		return j, Fail("This job is cancelled. Reopen it before changing charges or parts.")
	}
	if j.InvoiceID.Valid {
		return j, Fail("Invoice %s has been issued for this job. Void the invoice (authorized correction) before changing charges or parts.", j.InvoiceNo)
	}
	return j, nil
}

func (s *Service) CreateJob(ctx context.Context, a models.Actor, j models.Job, prefix string) (int64, error) {
	if err := need(a, "jobs.create"); err != nil {
		return 0, err
	}
	var id int64
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		c, err := repository.GetCustomer(ctx, tx, j.CustomerID)
		if err != nil {
			return err
		}
		if c.Archived {
			return Fail("Customer is archived. Restore the customer before opening a job.")
		}
		if j.VehicleID.Valid {
			v, err := repository.GetVehicle(ctx, tx, j.VehicleID.Int64)
			if err != nil {
				return err
			}
			if v.CustomerID != j.CustomerID {
				return Fail("The selected vehicle does not belong to this customer.")
			}
			if v.Archived {
				return Fail("The selected vehicle is archived.")
			}
		}
		j.JobNumber, err = nextNumber(ctx, tx, "job", prefix, 6)
		if err != nil {
			return err
		}
		j.CreatedBy = a.ID
		id, err = repository.CreateJob(ctx, tx, j)
		if err != nil {
			return err
		}
		return repository.Audit(ctx, tx, a, "job.create", "job", id, j.JobNumber+" for "+c.Name)
	})
	return id, err
}

// UpdateJob saves details. Users with jobs.edit can change everything; users with only
// jobs.notes can change the odometer and notes fields.
func (s *Service) UpdateJob(ctx context.Context, a models.Actor, in models.Job) error {
	return s.Tx(ctx, func(tx *sql.Tx) error {
		j, err := s.loadJob(ctx, tx, a, in.ID)
		if err != nil {
			return err
		}
		if a.Can("jobs.edit") {
			if in.CustomerID != j.CustomerID && j.InvoiceID.Valid {
				return Fail("The customer cannot be changed after an invoice has been issued.")
			}
			if in.VehicleID.Valid {
				v, err := repository.GetVehicle(ctx, tx, in.VehicleID.Int64)
				if err != nil {
					return err
				}
				if v.CustomerID != in.CustomerID {
					return Fail("The selected vehicle does not belong to this customer.")
				}
			}
			if err := repository.UpdateJobDetails(ctx, tx, in); err != nil {
				return err
			}
		} else if a.Can("jobs.notes") {
			if err := repository.UpdateJobNotes(ctx, tx, in); err != nil {
				return err
			}
		} else {
			return Fail("You do not have permission to edit this job.")
		}
		return repository.Audit(ctx, tx, a, "job.update", "job", j.ID, j.JobNumber)
	})
}

func (s *Service) ChangeJobStatus(ctx context.Context, a models.Actor, jobID int64, status string) error {
	if err := need(a, "jobs.status"); err != nil {
		return err
	}
	if !models.ValidJobStatus(status) {
		return Fail("Invalid status.")
	}
	return s.Tx(ctx, func(tx *sql.Tx) error {
		j, err := s.loadJob(ctx, tx, a, jobID)
		if err != nil {
			return err
		}
		if j.Status == status {
			return nil
		}
		if j.InvoiceID.Valid {
			return Fail("Invoice %s has been issued for this job; its status cannot change unless the invoice is voided.", j.InvoiceNo)
		}
		detail := fmt.Sprintf("%s: %s -> %s", j.JobNumber, j.Status, status)
		if status == models.JobCancelled {
			// Return every part on the job to stock with a traceable reversal.
			parts, err := repository.JobParts(ctx, tx, jobID)
			if err != nil {
				return err
			}
			for _, p := range parts {
				cost := p.UnitCost
				if err := applyStock(ctx, tx, a, p.PartID, p.Quantity, models.MoveJobReturn, jobID, 0, &cost, "Job cancelled", false); err != nil {
					return err
				}
				if err := repository.DeleteJobPart(ctx, tx, jobID, p.ID); err != nil {
					return err
				}
			}
			if len(parts) > 0 {
				detail += fmt.Sprintf("; %d part line(s) returned to stock", len(parts))
			}
		}
		completed := sql.NullString{}
		if status == models.JobCompleted {
			completed = sql.NullString{String: models.Today(), Valid: true}
		}
		if err := repository.SetJobStatus(ctx, tx, jobID, status, completed); err != nil {
			return err
		}
		return repository.Audit(ctx, tx, a, "job.status", "job", jobID, detail)
	})
}

// ---------- charges ----------

type ChargeInput struct {
	CategoryID  int64
	Description string
	Quantity    models.Qty
	UnitPrice   *models.Money // nil = use category default
	Notes       string
}

func validateQty(q models.Qty) error {
	if q <= 0 {
		return Fail("Quantity must be greater than zero.")
	}
	return nil
}

func (s *Service) AddJobCharge(ctx context.Context, a models.Actor, jobID int64, in ChargeInput) error {
	if err := need(a, "jobs.charges"); err != nil {
		return err
	}
	if err := validateQty(in.Quantity); err != nil {
		return err
	}
	return s.Tx(ctx, func(tx *sql.Tx) error {
		j, err := s.editableJob(ctx, tx, a, jobID)
		if err != nil {
			return err
		}
		cat, err := repository.GetChargeCategory(ctx, tx, in.CategoryID)
		if err != nil {
			return Fail("Select a charge category.")
		}
		if !cat.Active {
			return Fail("Charge category %q is inactive.", cat.Name)
		}
		desc := strings.TrimSpace(in.Description)
		if desc == "" {
			if cat.IsCustom {
				return Fail("Enter a description for a custom charge.")
			}
			desc = cat.Name
		}
		price := cat.DefaultPrice
		if in.UnitPrice != nil {
			price = *in.UnitPrice
		}
		if price < 0 {
			return Fail("Price cannot be negative.")
		}
		overridden := price != cat.DefaultPrice
		if overridden && !cat.ManualPrice && !a.Can("jobs.price_override") {
			return Fail("You are not allowed to change the default price for %s (%s).", cat.Name, cat.DefaultPrice)
		}
		c := models.JobCharge{JobID: jobID, CategoryID: sql.NullInt64{Int64: cat.ID, Valid: true}, Description: desc,
			Quantity: in.Quantity, Unit: cat.Unit, UnitPrice: price, LineTotal: models.LineTotal(in.Quantity, price),
			PriceOverridden: overridden, Notes: strings.TrimSpace(in.Notes)}
		id, err := repository.InsertJobCharge(ctx, tx, c, a.ID)
		if err != nil {
			return err
		}
		return repository.Audit(ctx, tx, a, "job.charge_add", "job", jobID,
			fmt.Sprintf("%s: %s x %s @ %s = %s (line %d)", j.JobNumber, desc, in.Quantity, price, c.LineTotal, id))
	})
}

func (s *Service) UpdateJobCharge(ctx context.Context, a models.Actor, jobID, chargeID int64, in ChargeInput) error {
	if err := need(a, "jobs.charges"); err != nil {
		return err
	}
	if err := validateQty(in.Quantity); err != nil {
		return err
	}
	return s.Tx(ctx, func(tx *sql.Tx) error {
		j, err := s.editableJob(ctx, tx, a, jobID)
		if err != nil {
			return err
		}
		c, err := repository.GetJobCharge(ctx, tx, jobID, chargeID)
		if err != nil {
			return err
		}
		old := c
		if d := strings.TrimSpace(in.Description); d != "" {
			c.Description = d
		}
		c.Quantity = in.Quantity
		c.Notes = strings.TrimSpace(in.Notes)
		if in.UnitPrice != nil && *in.UnitPrice != c.UnitPrice {
			newPrice := *in.UnitPrice
			if newPrice < 0 {
				return Fail("Price cannot be negative.")
			}
			def, manual := models.Money(-1), false
			if c.CategoryID.Valid {
				if cat, err := repository.GetChargeCategory(ctx, tx, c.CategoryID.Int64); err == nil {
					def, manual = cat.DefaultPrice, cat.ManualPrice
				}
			}
			if newPrice != def && !manual && !a.Can("jobs.price_override") {
				return Fail("You are not allowed to change this price.")
			}
			c.UnitPrice = newPrice
			c.PriceOverridden = newPrice != def
		}
		c.LineTotal = models.LineTotal(c.Quantity, c.UnitPrice)
		if err := repository.UpdateJobCharge(ctx, tx, c); err != nil {
			return err
		}
		return repository.Audit(ctx, tx, a, "job.charge_update", "job", jobID,
			fmt.Sprintf("%s line %d: %s x %s @ %s -> %s x %s @ %s", j.JobNumber, c.ID, old.Description, old.Quantity, old.UnitPrice, c.Description, c.Quantity, c.UnitPrice))
	})
}

func (s *Service) DeleteJobCharge(ctx context.Context, a models.Actor, jobID, chargeID int64) error {
	if err := need(a, "jobs.charges"); err != nil {
		return err
	}
	return s.Tx(ctx, func(tx *sql.Tx) error {
		j, err := s.editableJob(ctx, tx, a, jobID)
		if err != nil {
			return err
		}
		c, err := repository.GetJobCharge(ctx, tx, jobID, chargeID)
		if err != nil {
			return err
		}
		if err := repository.DeleteJobCharge(ctx, tx, jobID, chargeID); err != nil {
			return err
		}
		return repository.Audit(ctx, tx, a, "job.charge_remove", "job", jobID,
			fmt.Sprintf("%s: removed %s x %s @ %s", j.JobNumber, c.Description, c.Quantity, c.UnitPrice))
	})
}

// ---------- parts ----------

type PartInput struct {
	PartID        int64
	Quantity      models.Qty
	UnitPrice     *models.Money // nil = part sale price
	Notes         string
	AllowNegative bool
}

// AddJobPart records a part on a job and deducts stock in the same transaction.
func (s *Service) AddJobPart(ctx context.Context, a models.Actor, jobID int64, in PartInput) error {
	if err := need(a, "jobs.parts"); err != nil {
		return err
	}
	if err := validateQty(in.Quantity); err != nil {
		return err
	}
	return s.Tx(ctx, func(tx *sql.Tx) error {
		j, err := s.editableJob(ctx, tx, a, jobID)
		if err != nil {
			return err
		}
		p, err := repository.GetPart(ctx, tx, in.PartID)
		if err != nil {
			return Fail("Select a valid part.")
		}
		if !p.Active {
			return Fail("Part %s is inactive.", p.Code)
		}
		price := p.SalePrice
		if in.UnitPrice != nil {
			price = *in.UnitPrice
		}
		if price < 0 {
			return Fail("Price cannot be negative.")
		}
		overridden := price != p.SalePrice
		if overridden && !a.Can("jobs.price_override") {
			return Fail("You are not allowed to change the sale price of %s (%s).", p.Code, p.SalePrice)
		}
		jp := models.JobPart{JobID: jobID, PartID: p.ID, Description: p.Name, Quantity: in.Quantity, UnitPrice: price,
			UnitCost: p.PurchaseCost, LineTotal: models.LineTotal(in.Quantity, price), PriceOverridden: overridden, Notes: strings.TrimSpace(in.Notes)}
		id, err := repository.InsertJobPart(ctx, tx, jp, a.ID)
		if err != nil {
			return err
		}
		cost := p.PurchaseCost
		if err := applyStock(ctx, tx, a, p.ID, -in.Quantity, models.MoveJobIssue, jobID, 0, &cost,
			fmt.Sprintf("%s line %d", j.JobNumber, id), in.AllowNegative); err != nil {
			return err
		}
		return repository.Audit(ctx, tx, a, "job.part_add", "job", jobID,
			fmt.Sprintf("%s: %s x %s @ %s", j.JobNumber, p.Code, in.Quantity, price))
	})
}

// UpdateJobPart changes quantity/price; only the quantity difference moves stock.
func (s *Service) UpdateJobPart(ctx context.Context, a models.Actor, jobID, jobPartID int64, in PartInput) error {
	if err := need(a, "jobs.parts"); err != nil {
		return err
	}
	if err := validateQty(in.Quantity); err != nil {
		return err
	}
	return s.Tx(ctx, func(tx *sql.Tx) error {
		j, err := s.editableJob(ctx, tx, a, jobID)
		if err != nil {
			return err
		}
		jp, err := repository.GetJobPart(ctx, tx, jobID, jobPartID)
		if err != nil {
			return err
		}
		old := jp
		if in.UnitPrice != nil && *in.UnitPrice != jp.UnitPrice {
			if *in.UnitPrice < 0 {
				return Fail("Price cannot be negative.")
			}
			if !a.Can("jobs.price_override") {
				return Fail("You are not allowed to change part prices.")
			}
			jp.UnitPrice = *in.UnitPrice
			jp.PriceOverridden = true
		}
		jp.Quantity = in.Quantity
		jp.Notes = strings.TrimSpace(in.Notes)
		jp.LineTotal = models.LineTotal(jp.Quantity, jp.UnitPrice)
		if err := repository.UpdateJobPart(ctx, tx, jp); err != nil {
			return err
		}
		diff := jp.Quantity - old.Quantity
		cost := jp.UnitCost
		reason := fmt.Sprintf("%s line %d qty %s -> %s", j.JobNumber, jp.ID, old.Quantity, jp.Quantity)
		if diff > 0 {
			err = applyStock(ctx, tx, a, jp.PartID, -diff, models.MoveJobIssue, jobID, 0, &cost, reason, in.AllowNegative)
		} else if diff < 0 {
			err = applyStock(ctx, tx, a, jp.PartID, -diff, models.MoveJobReturn, jobID, 0, &cost, reason, false)
		}
		if err != nil {
			return err
		}
		return repository.Audit(ctx, tx, a, "job.part_update", "job", jobID,
			fmt.Sprintf("%s: %s qty %s -> %s, price %s -> %s", j.JobNumber, jp.PartCode, old.Quantity, jp.Quantity, old.UnitPrice, jp.UnitPrice))
	})
}

// RemoveJobPart returns the full quantity to stock and records the reversal.
func (s *Service) RemoveJobPart(ctx context.Context, a models.Actor, jobID, jobPartID int64) error {
	if err := need(a, "jobs.parts"); err != nil {
		return err
	}
	return s.Tx(ctx, func(tx *sql.Tx) error {
		j, err := s.editableJob(ctx, tx, a, jobID)
		if err != nil {
			return err
		}
		jp, err := repository.GetJobPart(ctx, tx, jobID, jobPartID)
		if err != nil {
			return err
		}
		cost := jp.UnitCost
		if err := applyStock(ctx, tx, a, jp.PartID, jp.Quantity, models.MoveJobReturn, jobID, 0, &cost,
			fmt.Sprintf("%s line %d removed", j.JobNumber, jp.ID), false); err != nil {
			return err
		}
		if err := repository.DeleteJobPart(ctx, tx, jobID, jobPartID); err != nil {
			return err
		}
		return repository.Audit(ctx, tx, a, "job.part_remove", "job", jobID,
			fmt.Sprintf("%s: returned %s x %s to stock", j.JobNumber, jp.PartCode, jp.Quantity))
	})
}
