// Package repository contains parameterized SQL queries. Functions accept a DBTX so
// they can run either directly on the database or inside a service transaction.
package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"garagems/internal/models"
)

type DBTX interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

var ErrNotFound = errors.New("record not found")

func notFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// Where accumulates AND-ed conditions with positional parameters.
type Where struct {
	parts []string
	Args  []any
}

func (w *Where) Add(cond string, args ...any) {
	w.parts = append(w.parts, cond)
	w.Args = append(w.Args, args...)
}

func (w *Where) SQL() string {
	if len(w.parts) == 0 {
		return ""
	}
	return " WHERE " + strings.Join(w.parts, " AND ")
}

// ListOpts holds a whitelisted ORDER BY expression and paging.
type ListOpts struct {
	OrderBy string // must come from a whitelist, never from user input directly
	Limit   int
	Offset  int
}

func (o ListOpts) SQL() string {
	s := ""
	if o.OrderBy != "" {
		s += " ORDER BY " + o.OrderBy
	}
	if o.Limit > 0 {
		s += " LIMIT " + strconv.Itoa(o.Limit) + " OFFSET " + strconv.Itoa(o.Offset)
	}
	return s
}

// Like builds a LIKE pattern with wildcards escaped; use with ESCAPE '\'.
func Like(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return "%" + r.Replace(s) + "%"
}

func count(ctx context.Context, q DBTX, query string, args ...any) (int, error) {
	var n int
	err := q.QueryRowContext(ctx, query, args...).Scan(&n)
	return n, err
}

func nullStr(ns sql.NullString) string {
	if ns.Valid {
		return ns.String
	}
	return ""
}

func NullInt(v int64) sql.NullInt64 { return sql.NullInt64{Int64: v, Valid: v != 0} }

// ---------- settings ----------

func GetSettings(ctx context.Context, q DBTX) (models.Settings, error) {
	rows, err := q.QueryContext(ctx, "SELECT key, value FROM settings")
	if err != nil {
		return models.Settings{}, err
	}
	defer rows.Close()
	m := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return models.Settings{}, err
		}
		m[k] = v
	}
	if err := rows.Err(); err != nil {
		return models.Settings{}, err
	}
	atoi := func(s string) int64 { n, _ := strconv.ParseInt(s, 10, 64); return n }
	s := models.Settings{
		BusinessName:     m["business_name"],
		Address:          m["address"],
		Phone:            m["phone"],
		Email:            m["email"],
		RegistrationNo:   m["registration_no"],
		CurrencyCode:     m["currency_code"],
		CurrencySymbol:   m["currency_symbol"],
		CurrencyDecimals: int(atoi(m["currency_decimals"])),
		SymbolAfter:      m["symbol_after"] == "1",
		TaxEnabled:       m["tax_enabled"] == "1",
		TaxName:          m["tax_name"],
		TaxRateBP:        atoi(m["tax_rate_bp"]),
		TaxNumber:        m["tax_number"],
		DiscountEnabled:  m["discount_enabled"] == "1",
		InvoicePrefix:    m["invoice_prefix"],
		InvoicePad:       int(atoi(m["invoice_pad"])),
		ReceiptPrefix:    m["receipt_prefix"],
		JobPrefix:        m["job_prefix"],
		PurchasePrefix:   m["purchase_prefix"],
		InvoiceFooter:    m["invoice_footer"],
		DateFormat:       m["date_format"],
	}
	if s.DateFormat == "" {
		s.DateFormat = "02 Jan 2006"
	}
	if s.InvoicePad < 1 || s.InvoicePad > 12 {
		s.InvoicePad = 6
	}
	next, err := SequencePeek(ctx, q, "invoice")
	if err == nil {
		s.InvoiceNext = next
	}
	return s, nil
}

func SaveSettings(ctx context.Context, q DBTX, kv map[string]string) error {
	for k, v := range kv {
		if _, err := q.ExecContext(ctx, `INSERT INTO settings(key, value) VALUES (?, ?)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value`, k, v); err != nil {
			return err
		}
	}
	return nil
}

// NextSequence atomically reserves and returns the next number of a named sequence.
func NextSequence(ctx context.Context, q DBTX, name string) (int64, error) {
	var n int64
	err := q.QueryRowContext(ctx, "UPDATE sequences SET next_value = next_value + 1 WHERE name = ? RETURNING next_value - 1", name).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("sequence %s: %w", name, err)
	}
	return n, nil
}

func SequencePeek(ctx context.Context, q DBTX, name string) (int64, error) {
	var n int64
	err := q.QueryRowContext(ctx, "SELECT next_value FROM sequences WHERE name = ?", name).Scan(&n)
	return n, err
}

func SetSequence(ctx context.Context, q DBTX, name string, next int64) error {
	_, err := q.ExecContext(ctx, "UPDATE sequences SET next_value = ? WHERE name = ?", next, name)
	return err
}

func FormatNumber(prefix string, n int64, pad int) string {
	s := strconv.FormatInt(n, 10)
	for len(s) < pad {
		s = "0" + s
	}
	return prefix + s
}

// ---------- audit ----------

func Audit(ctx context.Context, q DBTX, a models.Actor, action, entityType string, entityID int64, details string) error {
	_, err := q.ExecContext(ctx, `INSERT INTO audit_log(user_id, username, action, entity_type, entity_id, details, ip, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, NullInt(a.ID), a.Username, action, entityType, NullInt(entityID), details, a.IP, models.NowUTC())
	return err
}

type AuditFilter struct {
	Q, Action, EntityType, From, To string
	UserID                          int64
}

func ListAudit(ctx context.Context, q DBTX, f AuditFilter, o ListOpts) ([]models.AuditEntry, int, error) {
	var w Where
	if f.Q != "" {
		w.Add(`(details LIKE ? ESCAPE '\' OR username LIKE ? ESCAPE '\' OR action LIKE ? ESCAPE '\')`, Like(f.Q), Like(f.Q), Like(f.Q))
	}
	if f.EntityType != "" {
		w.Add("entity_type = ?", f.EntityType)
	}
	if f.UserID != 0 {
		w.Add("user_id = ?", f.UserID)
	}
	if f.From != "" {
		w.Add("created_at >= ?", f.From)
	}
	if f.To != "" {
		w.Add("created_at < ?", f.To)
	}
	total, err := count(ctx, q, "SELECT COUNT(*) FROM audit_log"+w.SQL(), w.Args...)
	if err != nil {
		return nil, 0, err
	}
	rows, err := q.QueryContext(ctx, "SELECT id, user_id, username, action, entity_type, entity_id, details, ip, created_at FROM audit_log"+w.SQL()+o.SQL(), w.Args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []models.AuditEntry
	for rows.Next() {
		var e models.AuditEntry
		if err := rows.Scan(&e.ID, &e.UserID, &e.Username, &e.Action, &e.EntityType, &e.EntityID, &e.Details, &e.IP, &e.CreatedAt); err != nil {
			return nil, 0, err
		}
		out = append(out, e)
	}
	return out, total, rows.Err()
}
