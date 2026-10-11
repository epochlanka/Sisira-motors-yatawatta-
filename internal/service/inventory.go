package service

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"garagems/internal/models"
	"garagems/internal/repository"
)

// CreatePart adds a part and records any opening stock as a movement.
func (s *Service) CreatePart(ctx context.Context, a models.Actor, p models.Part, opening models.Qty) (int64, error) {
	if err := need(a, "parts.edit"); err != nil {
		return 0, err
	}
	if opening < 0 {
		return 0, Fail("Opening stock cannot be negative.")
	}
	var id int64
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		taken, err := repository.PartCodeTaken(ctx, tx, p.Code, 0)
		if err != nil {
			return err
		}
		if taken {
			return Fail("Part code %s already exists.", p.Code)
		}
		id, err = repository.CreatePart(ctx, tx, p)
		if err != nil {
			return err
		}
		if opening > 0 {
			cost := p.PurchaseCost
			if err := applyStock(ctx, tx, a, id, opening, models.MoveOpening, 0, 0, &cost, "Opening stock", false); err != nil {
				return err
			}
		}
		return repository.Audit(ctx, tx, a, "part.create", "part", id, fmt.Sprintf("%s %s, opening stock %s", p.Code, p.Name, opening))
	})
	return id, err
}

func (s *Service) UpdatePart(ctx context.Context, a models.Actor, p models.Part) error {
	if err := need(a, "parts.edit"); err != nil {
		return err
	}
	return s.Tx(ctx, func(tx *sql.Tx) error {
		old, err := repository.GetPart(ctx, tx, p.ID)
		if err != nil {
			return err
		}
		taken, err := repository.PartCodeTaken(ctx, tx, p.Code, p.ID)
		if err != nil {
			return err
		}
		if taken {
			return Fail("Part code %s already exists.", p.Code)
		}
		if err := repository.UpdatePart(ctx, tx, p); err != nil {
			return err
		}
		detail := p.Code
		if old.SalePrice != p.SalePrice {
			detail += fmt.Sprintf("; sale price %s -> %s", old.SalePrice, p.SalePrice)
		}
		if old.PurchaseCost != p.PurchaseCost {
			detail += fmt.Sprintf("; cost %s -> %s", old.PurchaseCost, p.PurchaseCost)
		}
		if old.Code != p.Code {
			detail += "; code changed from " + old.Code
		}
		return repository.Audit(ctx, tx, a, "part.update", "part", p.ID, detail)
	})
}

// AdjustStock records an authorized adjustment. mode "delta" adds/removes qty,
// mode "count" sets the on-hand quantity to a counted value.
func (s *Service) AdjustStock(ctx context.Context, a models.Actor, partID int64, mode string, qty models.Qty, reason string, allowNegative bool) error {
	if err := need(a, "stock.adjust"); err != nil {
		return err
	}
	reason = strings.TrimSpace(reason)
	if len(reason) < 3 {
		return Fail("Enter a reason for the adjustment.")
	}
	return s.Tx(ctx, func(tx *sql.Tx) error {
		p, err := repository.GetPart(ctx, tx, partID)
		if err != nil {
			return err
		}
		delta := qty
		if mode == "count" {
			if qty < 0 {
				return Fail("Counted quantity cannot be negative.")
			}
			delta = qty - p.QtyOnHand
		}
		if delta == 0 {
			return Fail("The adjustment does not change the stock level.")
		}
		cost := p.PurchaseCost
		if err := applyStock(ctx, tx, a, partID, delta, models.MoveAdjustment, 0, 0, &cost, reason, allowNegative); err != nil {
			return err
		}
		return repository.Audit(ctx, tx, a, "stock.adjust", "part", partID,
			fmt.Sprintf("%s %s (%s -> %s): %s", p.Code, delta.Signed(), p.QtyOnHand, p.QtyOnHand+delta, reason))
	})
}

// ---------- purchases ----------

func (s *Service) CreatePurchase(ctx context.Context, a models.Actor, p models.Purchase, prefix string) (int64, error) {
	if err := need(a, "purchases.edit"); err != nil {
		return 0, err
	}
	var id int64
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		sup, err := repository.GetSupplier(ctx, tx, p.SupplierID)
		if err != nil {
			return Fail("Select a supplier.")
		}
		if !sup.Active {
			return Fail("Supplier %s is inactive.", sup.Name)
		}
		p.PurchaseNumber, err = nextNumber(ctx, tx, "purchase", prefix, 6)
		if err != nil {
			return err
		}
		id, err = repository.CreatePurchase(ctx, tx, p, a.ID)
		if err != nil {
			return err
		}
		return repository.Audit(ctx, tx, a, "purchase.create", "purchase", id, p.PurchaseNumber+" from "+sup.Name)
	})
	return id, err
}

func (s *Service) draftPurchase(ctx context.Context, tx *sql.Tx, id int64) (models.Purchase, error) {
	p, err := repository.GetPurchase(ctx, tx, id)
	if err != nil {
		return p, err
	}
	if p.Status != models.PurchaseDraft {
		return p, Fail("Only draft purchases can be edited.")
	}
	return p, nil
}

func (s *Service) UpdatePurchase(ctx context.Context, a models.Actor, p models.Purchase) error {
	if err := need(a, "purchases.edit"); err != nil {
		return err
	}
	return s.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := s.draftPurchase(ctx, tx, p.ID); err != nil {
			return err
		}
		if _, err := repository.GetSupplier(ctx, tx, p.SupplierID); err != nil {
			return Fail("Select a supplier.")
		}
		return repository.UpdatePurchaseHeader(ctx, tx, p)
	})
}

func (s *Service) AddPurchaseItem(ctx context.Context, a models.Actor, purchaseID, partID int64, qty models.Qty, cost models.Money) error {
	if err := need(a, "purchases.edit"); err != nil {
		return err
	}
	if qty <= 0 {
		return Fail("Quantity must be greater than zero.")
	}
	if cost < 0 {
		return Fail("Cost cannot be negative.")
	}
	return s.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := s.draftPurchase(ctx, tx, purchaseID); err != nil {
			return err
		}
		part, err := repository.GetPart(ctx, tx, partID)
		if err != nil {
			return Fail("Select a valid part.")
		}
		if !part.Active {
			return Fail("Part %s is inactive.", part.Code)
		}
		if err := repository.InsertPurchaseItem(ctx, tx, models.PurchaseItem{PurchaseID: purchaseID, PartID: partID, Quantity: qty,
			UnitCost: cost, LineTotal: models.LineTotal(qty, cost)}); err != nil {
			return err
		}
		return repository.RecalcPurchaseTotal(ctx, tx, purchaseID)
	})
}

func (s *Service) RemovePurchaseItem(ctx context.Context, a models.Actor, purchaseID, itemID int64) error {
	if err := need(a, "purchases.edit"); err != nil {
		return err
	}
	return s.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := s.draftPurchase(ctx, tx, purchaseID); err != nil {
			return err
		}
		if err := repository.DeletePurchaseItem(ctx, tx, purchaseID, itemID); err != nil {
			return err
		}
		return repository.RecalcPurchaseTotal(ctx, tx, purchaseID)
	})
}

// ReceivePurchase increases stock for every item and updates each part's latest purchase cost.
func (s *Service) ReceivePurchase(ctx context.Context, a models.Actor, purchaseID int64) error {
	if err := need(a, "purchases.receive"); err != nil {
		return err
	}
	return s.Tx(ctx, func(tx *sql.Tx) error {
		p, err := s.draftPurchase(ctx, tx, purchaseID)
		if err != nil {
			return err
		}
		items, err := repository.PurchaseItems(ctx, tx, purchaseID)
		if err != nil {
			return err
		}
		if len(items) == 0 {
			return Fail("Add at least one item before receiving the purchase.")
		}
		for _, it := range items {
			cost := it.UnitCost
			if err := applyStock(ctx, tx, a, it.PartID, it.Quantity, models.MovePurchaseReceipt, 0, purchaseID, &cost,
				p.PurchaseNumber+" "+p.Reference, false); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, "UPDATE parts SET purchase_cost = ?, updated_at = ? WHERE id = ?", it.UnitCost, models.NowUTC(), it.PartID); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, "UPDATE purchases SET status = 'Received', received_at = ?, received_by = ?, updated_at = ? WHERE id = ?",
			models.NowUTC(), a.ID, models.NowUTC(), purchaseID); err != nil {
			return err
		}
		return repository.Audit(ctx, tx, a, "purchase.receive", "purchase", purchaseID,
			fmt.Sprintf("%s received: %d item(s), total %s", p.PurchaseNumber, len(items), p.Total))
	})
}

// CancelPurchase cancels a draft, or reverses the stock of a received purchase.
func (s *Service) CancelPurchase(ctx context.Context, a models.Actor, purchaseID int64, reason string, allowNegative bool) error {
	if err := need(a, "purchases.receive"); err != nil {
		return err
	}
	reason = strings.TrimSpace(reason)
	if len(reason) < 3 {
		return Fail("Enter a reason for cancelling.")
	}
	return s.Tx(ctx, func(tx *sql.Tx) error {
		p, err := repository.GetPurchase(ctx, tx, purchaseID)
		if err != nil {
			return err
		}
		switch p.Status {
		case models.PurchaseCancelled:
			return Fail("Purchase is already cancelled.")
		case models.PurchaseReceived:
			items, err := repository.PurchaseItems(ctx, tx, purchaseID)
			if err != nil {
				return err
			}
			for _, it := range items {
				cost := it.UnitCost
				if err := applyStock(ctx, tx, a, it.PartID, -it.Quantity, models.MovePurchaseReverse, 0, purchaseID, &cost,
					p.PurchaseNumber+" cancelled: "+reason, allowNegative); err != nil {
					return err
				}
			}
		}
		if _, err := tx.ExecContext(ctx, "UPDATE purchases SET status = 'Cancelled', cancelled_at = ?, cancelled_by = ?, cancel_reason = ?, updated_at = ? WHERE id = ?",
			models.NowUTC(), a.ID, reason, models.NowUTC(), purchaseID); err != nil {
			return err
		}
		return repository.Audit(ctx, tx, a, "purchase.cancel", "purchase", purchaseID,
			fmt.Sprintf("%s cancelled (was %s): %s", p.PurchaseNumber, p.Status, reason))
	})
}
