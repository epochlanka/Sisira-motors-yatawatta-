package repository

import (
	"context"
	"database/sql"

	"garagems/internal/models"
)

type CustomerFilter struct {
	Q        string
	Archived string // "" active only, "1" archived only, "all"
}

// customerSearch matches name, phone (digits), email or any vehicle registration.
func customerSearch(w *Where, qs string) {
	if qs == "" {
		return
	}
	digits := models.DigitsOnly(qs)
	reg := models.NormalizeReg(qs)
	cond := `(c.name LIKE ? ESCAPE '\' OR c.email LIKE ? ESCAPE '\'`
	args := []any{Like(qs), Like(qs)}
	if digits != "" {
		cond += ` OR c.phone_norm LIKE ? ESCAPE '\'`
		args = append(args, Like(digits))
	}
	if reg != "" {
		cond += ` OR EXISTS (SELECT 1 FROM vehicles v WHERE v.customer_id = c.id AND v.registration_norm LIKE ? ESCAPE '\')`
		args = append(args, Like(reg))
	}
	w.Add(cond+")", args...)
}

func ListCustomers(ctx context.Context, q DBTX, f CustomerFilter, o ListOpts) ([]models.Customer, int, error) {
	var w Where
	customerSearch(&w, f.Q)
	switch f.Archived {
	case "1":
		w.Add("c.archived = 1")
	case "all":
	default:
		w.Add("c.archived = 0")
	}
	total, err := count(ctx, q, "SELECT COUNT(*) FROM customers c"+w.SQL(), w.Args...)
	if err != nil {
		return nil, 0, err
	}
	rows, err := q.QueryContext(ctx, `SELECT c.id, c.name, c.phone, c.email, c.address, c.notes, c.archived, c.created_at, c.updated_at,
		(SELECT COUNT(*) FROM vehicles v WHERE v.customer_id = c.id AND v.archived = 0),
		(SELECT COALESCE(SUM(i.total - i.amount_paid), 0) FROM invoices i WHERE i.customer_id = c.id AND i.status = 'Issued')
		FROM customers c`+w.SQL()+o.SQL(), w.Args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []models.Customer
	for rows.Next() {
		var c models.Customer
		if err := rows.Scan(&c.ID, &c.Name, &c.Phone, &c.Email, &c.Address, &c.Notes, &c.Archived, &c.CreatedAt, &c.UpdatedAt, &c.VehicleCount, &c.Balance); err != nil {
			return nil, 0, err
		}
		out = append(out, c)
	}
	return out, total, rows.Err()
}

func GetCustomer(ctx context.Context, q DBTX, id int64) (models.Customer, error) {
	var c models.Customer
	err := q.QueryRowContext(ctx, `SELECT id, name, phone, email, address, notes, archived, created_at, updated_at,
		(SELECT COALESCE(SUM(i.total - i.amount_paid), 0) FROM invoices i WHERE i.customer_id = customers.id AND i.status = 'Issued')
		FROM customers WHERE id = ?`, id).
		Scan(&c.ID, &c.Name, &c.Phone, &c.Email, &c.Address, &c.Notes, &c.Archived, &c.CreatedAt, &c.UpdatedAt, &c.Balance)
	return c, notFound(err)
}

func CreateCustomer(ctx context.Context, q DBTX, c models.Customer) (int64, error) {
	now := models.NowUTC()
	res, err := q.ExecContext(ctx, `INSERT INTO customers(name, phone, phone_norm, email, address, notes, archived, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, 0, ?, ?)`, c.Name, c.Phone, models.DigitsOnly(c.Phone), c.Email, c.Address, c.Notes, now, now)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func UpdateCustomer(ctx context.Context, q DBTX, c models.Customer) error {
	_, err := q.ExecContext(ctx, `UPDATE customers SET name = ?, phone = ?, phone_norm = ?, email = ?, address = ?, notes = ?, updated_at = ? WHERE id = ?`,
		c.Name, c.Phone, models.DigitsOnly(c.Phone), c.Email, c.Address, c.Notes, models.NowUTC(), c.ID)
	return err
}

func SetCustomerArchived(ctx context.Context, q DBTX, id int64, archived bool) error {
	_, err := q.ExecContext(ctx, "UPDATE customers SET archived = ?, updated_at = ? WHERE id = ?", archived, models.NowUTC(), id)
	return err
}

// ---------- vehicles ----------

type VehicleFilter struct {
	Q          string
	CustomerID int64
	Archived   string
	DueBy      string // service reminder date on or before this date
}

const vehicleSelect = `SELECT v.id, v.customer_id, c.name, c.phone, v.registration, v.make, v.model, v.year, v.vin, v.notes, v.archived, v.created_at, v.updated_at,
	COALESCE(v.next_service_date, ''), v.next_service_km, v.service_note,
	(SELECT MAX(odometer) FROM jobs WHERE vehicle_id = v.id AND status <> 'Cancelled'),
	COALESCE((SELECT MAX(job_date) FROM jobs WHERE vehicle_id = v.id AND status <> 'Cancelled'), '')
	FROM vehicles v JOIN customers c ON c.id = v.customer_id`

func scanVehicle(sc interface{ Scan(...any) error }) (models.Vehicle, error) {
	var v models.Vehicle
	err := sc.Scan(&v.ID, &v.CustomerID, &v.CustomerName, &v.CustomerPhone, &v.Registration, &v.Make, &v.Model, &v.Year, &v.VIN, &v.Notes, &v.Archived, &v.CreatedAt, &v.UpdatedAt,
		&v.NextServiceDate, &v.NextServiceKm, &v.ServiceNote, &v.LastOdometer, &v.LastVisit)
	return v, err
}

func ListVehicles(ctx context.Context, q DBTX, f VehicleFilter, o ListOpts) ([]models.Vehicle, int, error) {
	var w Where
	if f.Q != "" {
		cond := `(v.make LIKE ? ESCAPE '\' OR v.model LIKE ? ESCAPE '\' OR c.name LIKE ? ESCAPE '\' OR v.vin LIKE ? ESCAPE '\'`
		args := []any{Like(f.Q), Like(f.Q), Like(f.Q), Like(f.Q)}
		if reg := models.NormalizeReg(f.Q); reg != "" {
			cond += ` OR v.registration_norm LIKE ? ESCAPE '\'`
			args = append(args, Like(reg))
		}
		if d := models.DigitsOnly(f.Q); d != "" {
			cond += ` OR c.phone_norm LIKE ? ESCAPE '\'`
			args = append(args, Like(d))
		}
		w.Add(cond+")", args...)
	}
	if f.CustomerID != 0 {
		w.Add("v.customer_id = ?", f.CustomerID)
	}
	if f.DueBy != "" {
		w.Add("v.next_service_date IS NOT NULL AND v.next_service_date <= ? AND c.archived = 0", f.DueBy)
	}
	switch f.Archived {
	case "1":
		w.Add("v.archived = 1")
	case "all":
	default:
		w.Add("v.archived = 0")
	}
	total, err := count(ctx, q, "SELECT COUNT(*) FROM vehicles v JOIN customers c ON c.id = v.customer_id"+w.SQL(), w.Args...)
	if err != nil {
		return nil, 0, err
	}
	rows, err := q.QueryContext(ctx, vehicleSelect+w.SQL()+o.SQL(), w.Args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []models.Vehicle
	for rows.Next() {
		v, err := scanVehicle(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, v)
	}
	return out, total, rows.Err()
}

func GetVehicle(ctx context.Context, q DBTX, id int64) (models.Vehicle, error) {
	v, err := scanVehicle(q.QueryRowContext(ctx, vehicleSelect+" WHERE v.id = ?", id))
	return v, notFound(err)
}

// RegistrationTaken reports whether another active vehicle has the same registration.
func RegistrationTaken(ctx context.Context, q DBTX, reg string, exceptID int64) (bool, error) {
	n, err := count(ctx, q, "SELECT COUNT(*) FROM vehicles WHERE registration_norm = ? AND archived = 0 AND id <> ?", models.NormalizeReg(reg), exceptID)
	return n > 0, err
}

func CreateVehicle(ctx context.Context, q DBTX, v models.Vehicle) (int64, error) {
	now := models.NowUTC()
	res, err := q.ExecContext(ctx, `INSERT INTO vehicles(customer_id, registration, registration_norm, make, model, year, vin, notes,
		next_service_date, next_service_km, service_note, archived, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?)`, v.CustomerID, v.Registration, models.NormalizeReg(v.Registration), v.Make, v.Model, v.Year, v.VIN, v.Notes,
		nullDate(v.NextServiceDate), v.NextServiceKm, v.ServiceNote, now, now)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func UpdateVehicle(ctx context.Context, q DBTX, v models.Vehicle) error {
	_, err := q.ExecContext(ctx, `UPDATE vehicles SET customer_id = ?, registration = ?, registration_norm = ?, make = ?, model = ?, year = ?, vin = ?, notes = ?,
		next_service_date = ?, next_service_km = ?, service_note = ?, updated_at = ? WHERE id = ?`,
		v.CustomerID, v.Registration, models.NormalizeReg(v.Registration), v.Make, v.Model, v.Year, v.VIN, v.Notes,
		nullDate(v.NextServiceDate), v.NextServiceKm, v.ServiceNote, models.NowUTC(), v.ID)
	return err
}

// SetVehicleService updates only the service reminder of a vehicle.
func SetVehicleService(ctx context.Context, q DBTX, v models.Vehicle) error {
	_, err := q.ExecContext(ctx, "UPDATE vehicles SET next_service_date = ?, next_service_km = ?, service_note = ?, updated_at = ? WHERE id = ?",
		nullDate(v.NextServiceDate), v.NextServiceKm, v.ServiceNote, models.NowUTC(), v.ID)
	return err
}

func nullDate(s string) sql.NullString { return sql.NullString{String: s, Valid: s != ""} }

func SetVehicleArchived(ctx context.Context, q DBTX, id int64, archived bool) error {
	_, err := q.ExecContext(ctx, "UPDATE vehicles SET archived = ?, updated_at = ? WHERE id = ?", archived, models.NowUTC(), id)
	return err
}

// FindCustomerByPhone returns the first active customer whose phone digits match exactly.
func FindCustomerByPhone(ctx context.Context, q DBTX, phone string) (models.Customer, error) {
	var id int64
	err := q.QueryRowContext(ctx, "SELECT id FROM customers WHERE phone_norm = ? AND phone_norm <> '' AND archived = 0 ORDER BY id LIMIT 1", models.DigitsOnly(phone)).Scan(&id)
	if err != nil {
		return models.Customer{}, notFound(err)
	}
	return GetCustomer(ctx, q, id)
}

// FindVehicleByReg returns the active vehicle with this registration.
func FindVehicleByReg(ctx context.Context, q DBTX, reg string) (models.Vehicle, error) {
	v, err := scanVehicle(q.QueryRowContext(ctx, vehicleSelect+" WHERE v.registration_norm = ? AND v.archived = 0", models.NormalizeReg(reg)))
	return v, notFound(err)
}

// CustomerOptions returns active customers for select lists (limited).
func SearchCustomersQuick(ctx context.Context, q DBTX, qs string, limit int) ([]models.Customer, error) {
	list, _, err := ListCustomers(ctx, q, CustomerFilter{Q: qs}, ListOpts{OrderBy: "c.name COLLATE NOCASE", Limit: limit})
	return list, err
}

var _ = sql.ErrNoRows
