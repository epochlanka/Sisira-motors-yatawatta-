package database

import (
	"context"
	"database/sql"
	"strconv"

	"garagems/internal/config"
	"garagems/internal/models"
)

// SeedDefaults inserts reference data that must exist. It is idempotent: it only
// inserts rows that are missing and never overwrites edited values.
func SeedDefaults(ctx context.Context, db *sql.DB, cfg config.Config) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := models.NowUTC()

	for _, p := range models.AllPermissions {
		if _, err := tx.ExecContext(ctx, `INSERT INTO permissions(code, description) VALUES (?, ?)
			ON CONFLICT(code) DO UPDATE SET description = excluded.description`, p.Code, p.Description); err != nil {
			return err
		}
	}

	var roleCount int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM roles").Scan(&roleCount); err != nil {
		return err
	}
	if roleCount == 0 {
		for _, r := range models.DefaultRoles {
			res, err := tx.ExecContext(ctx, "INSERT INTO roles(name, description, is_system) VALUES (?, ?, ?)", r.Name, r.Description, r.System)
			if err != nil {
				return err
			}
			id, _ := res.LastInsertId()
			perms := r.Perms
			if len(perms) == 1 && perms[0] == "*" {
				perms = nil
				for _, p := range models.AllPermissions {
					perms = append(perms, p.Code)
				}
			}
			for _, p := range perms {
				if _, err := tx.ExecContext(ctx, "INSERT INTO role_permissions(role_id, permission) VALUES (?, ?)", id, p); err != nil {
					return err
				}
			}
		}
	}
	// The system (Administrator) role always holds every permission, including ones added in upgrades.
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO role_permissions(role_id, permission)
		SELECT r.id, p.code FROM roles r CROSS JOIN permissions p WHERE r.is_system = 1`); err != nil {
		return err
	}

	var catCount int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM charge_categories").Scan(&catCount); err != nil {
		return err
	}
	if catCount == 0 {
		cats := []struct {
			name, unit string
			custom     bool
		}{
			{"Garage labour", "hour", false},
			{"Shop labour", "hour", false},
			{"Welding", "job", false},
			{"Grease service", "job", false},
			{"Lathe work", "job", false},
			{"Battery service", "job", false},
			{"Custom charge", "item", true},
		}
		for i, c := range cats {
			if _, err := tx.ExecContext(ctx, `INSERT INTO charge_categories(name, default_price, unit, manual_price, is_custom, active, sort_order, created_at, updated_at)
				VALUES (?, 0, ?, 1, ?, 1, ?, ?, ?)`, c.name, c.unit, c.custom, (i+1)*10, now, now); err != nil {
				return err
			}
		}
	}

	b := cfg.Business
	defaults := map[string]string{
		"business_name":     b.Name,
		"address":           b.Address,
		"phone":             b.Phone,
		"email":             b.Email,
		"registration_no":   "",
		"currency_code":     b.CurrencyCode,
		"currency_symbol":   b.CurrencySymbol,
		"currency_decimals": strconv.Itoa(b.CurrencyDecimals),
		"symbol_after":      "0",
		"tax_enabled":       "0",
		"tax_name":          "VAT",
		"tax_rate_bp":       "0",
		"tax_number":        "",
		"discount_enabled":  "1",
		"invoice_prefix":    b.InvoicePrefix,
		"invoice_pad":       "6",
		"receipt_prefix":    "RCT-",
		"job_prefix":        "JOB-",
		"purchase_prefix":   "PO-",
		"invoice_footer":    b.InvoiceFooter,
		"date_format":       "02 Jan 2006",
	}
	for k, v := range defaults {
		if _, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO settings(key, value) VALUES (?, ?)", k, v); err != nil {
			return err
		}
	}
	for _, s := range []string{"invoice", "receipt", "job", "purchase"} {
		if _, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO sequences(name, next_value) VALUES (?, 1)", s); err != nil {
			return err
		}
	}
	return tx.Commit()
}
