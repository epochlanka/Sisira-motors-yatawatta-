// Package service implements business rules. Operations that must succeed or fail
// together run inside a single database transaction and are rolled back on error.
package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"garagems/internal/models"
	"garagems/internal/repository"
)

// UserError is a problem the user can fix; its message is safe to display.
type UserError struct{ Msg string }

func (e *UserError) Error() string { return e.Msg }

func Fail(format string, args ...any) error { return &UserError{Msg: fmt.Sprintf(format, args...)} }

// IsUserError reports whether err should be shown to the user verbatim.
func IsUserError(err error) (string, bool) {
	var ue *UserError
	if errors.As(err, &ue) {
		return ue.Msg, true
	}
	if errors.Is(err, repository.ErrNotFound) {
		return "The requested record was not found.", true
	}
	return "", false
}

type Service struct {
	DB *sql.DB
}

func New(db *sql.DB) *Service { return &Service{DB: db} }

// Tx runs fn in a transaction, committing on success and rolling back on any error or panic.
func (s *Service) Tx(ctx context.Context, fn func(tx *sql.Tx) error) (err error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if p := recover(); p != nil {
			tx.Rollback()
			panic(p)
		}
		if err != nil {
			tx.Rollback()
		}
	}()
	if err = fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func need(a models.Actor, perm string) error {
	if !a.Can(perm) {
		return Fail("You do not have permission to do this (%s).", perm)
	}
	return nil
}

// applyStock changes a part's quantity and records a movement in the same transaction.
// Stock may only go below zero when allowNegative is set by a user holding stock.negative.
func applyStock(ctx context.Context, tx *sql.Tx, a models.Actor, partID int64, delta models.Qty, mtype string,
	jobID, purchaseID int64, unitCost *models.Money, reason string, allowNegative bool) error {
	if delta == 0 {
		return nil
	}
	var onHand models.Qty
	var code string
	if err := tx.QueryRowContext(ctx, "SELECT qty_on_hand, code FROM parts WHERE id = ?", partID).Scan(&onHand, &code); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Fail("Part not found.")
		}
		return err
	}
	newQty := onHand + delta
	override := false
	if newQty < 0 {
		if !allowNegative || !a.Can("stock.negative") {
			return Fail("Not enough stock for %s: %s in stock, %s needed. A user with permission can authorize negative stock.", code, onHand, -delta)
		}
		override = true
	}
	if _, err := tx.ExecContext(ctx, "UPDATE parts SET qty_on_hand = ?, updated_at = ? WHERE id = ?", newQty, models.NowUTC(), partID); err != nil {
		return err
	}
	m := models.StockMovement{
		PartID: partID, Type: mtype, Quantity: delta, BalanceAfter: newQty,
		JobID: repository.NullInt(jobID), PurchaseID: repository.NullInt(purchaseID),
		Reason: reason, NegativeOverride: override,
	}
	if unitCost != nil {
		m.UnitCost = sql.NullInt64{Int64: int64(*unitCost), Valid: true}
	}
	if err := repository.InsertMovement(ctx, tx, m, a.ID); err != nil {
		return err
	}
	if override {
		if err := repository.Audit(ctx, tx, a, "stock.negative_override", "part", partID,
			fmt.Sprintf("%s balance %s after %s (%s)", code, newQty, delta.Signed(), reason)); err != nil {
			return err
		}
	}
	return nil
}

func nextNumber(ctx context.Context, tx *sql.Tx, seq, prefix string, pad int) (string, error) {
	n, err := repository.NextSequence(ctx, tx, seq)
	if err != nil {
		return "", err
	}
	return repository.FormatNumber(prefix, n, pad), nil
}
