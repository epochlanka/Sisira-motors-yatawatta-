package database

// migrations are applied in order; never edit an applied migration, append a new one.
//
// Conventions:
//   - Money: INTEGER minor units (e.g. cents).
//   - Quantities: INTEGER thousandths (1.5 = 1500).
//   - System timestamps (*_at): TEXT, UTC, "YYYY-MM-DDTHH:MM:SSZ".
//   - Business dates (*_date): TEXT, local business date, "YYYY-MM-DD".
//   - Booleans: INTEGER 0/1.
var migrations = []string{
	// 1: initial schema
	`
CREATE TABLE settings (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);

CREATE TABLE sequences (
	name       TEXT PRIMARY KEY,
	next_value INTEGER NOT NULL CHECK (next_value > 0)
);

CREATE TABLE permissions (
	code        TEXT PRIMARY KEY,
	description TEXT NOT NULL
);

CREATE TABLE roles (
	id          INTEGER PRIMARY KEY,
	name        TEXT NOT NULL UNIQUE COLLATE NOCASE,
	description TEXT NOT NULL DEFAULT '',
	is_system   INTEGER NOT NULL DEFAULT 0 CHECK (is_system IN (0,1))
);

CREATE TABLE role_permissions (
	role_id    INTEGER NOT NULL REFERENCES roles(id) ON DELETE CASCADE,
	permission TEXT NOT NULL REFERENCES permissions(code) ON DELETE CASCADE,
	PRIMARY KEY (role_id, permission)
);

CREATE TABLE users (
	id                   INTEGER PRIMARY KEY,
	username             TEXT NOT NULL UNIQUE COLLATE NOCASE,
	full_name            TEXT NOT NULL,
	password_hash        TEXT NOT NULL,
	role_id              INTEGER NOT NULL REFERENCES roles(id),
	active               INTEGER NOT NULL DEFAULT 1 CHECK (active IN (0,1)),
	must_change_password INTEGER NOT NULL DEFAULT 0 CHECK (must_change_password IN (0,1)),
	created_at           TEXT NOT NULL,
	updated_at           TEXT NOT NULL
);

CREATE TABLE sessions (
	token_hash TEXT PRIMARY KEY,
	user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	csrf_token TEXT NOT NULL,
	flash      TEXT NOT NULL DEFAULT '',
	ip         TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL,
	expires_at TEXT NOT NULL
);
CREATE INDEX ix_sessions_user ON sessions(user_id);
CREATE INDEX ix_sessions_expires ON sessions(expires_at);

CREATE TABLE customers (
	id         INTEGER PRIMARY KEY,
	name       TEXT NOT NULL CHECK (length(name) > 0),
	phone      TEXT NOT NULL,
	phone_norm TEXT NOT NULL DEFAULT '',
	email      TEXT NOT NULL DEFAULT '',
	address    TEXT NOT NULL DEFAULT '',
	notes      TEXT NOT NULL DEFAULT '',
	archived   INTEGER NOT NULL DEFAULT 0 CHECK (archived IN (0,1)),
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);
CREATE INDEX ix_customers_name ON customers(name COLLATE NOCASE);
CREATE INDEX ix_customers_phone ON customers(phone_norm);

CREATE TABLE vehicles (
	id                INTEGER PRIMARY KEY,
	customer_id       INTEGER NOT NULL REFERENCES customers(id),
	registration      TEXT NOT NULL CHECK (length(registration) > 0),
	registration_norm TEXT NOT NULL,
	make              TEXT NOT NULL DEFAULT '',
	model             TEXT NOT NULL DEFAULT '',
	year              INTEGER CHECK (year IS NULL OR (year BETWEEN 1900 AND 2100)),
	vin               TEXT NOT NULL DEFAULT '',
	notes             TEXT NOT NULL DEFAULT '',
	archived          INTEGER NOT NULL DEFAULT 0 CHECK (archived IN (0,1)),
	created_at        TEXT NOT NULL,
	updated_at        TEXT NOT NULL
);
CREATE UNIQUE INDEX ux_vehicles_reg_active ON vehicles(registration_norm) WHERE archived = 0;
CREATE INDEX ix_vehicles_customer ON vehicles(customer_id);
CREATE INDEX ix_vehicles_reg ON vehicles(registration_norm);

CREATE TABLE charge_categories (
	id            INTEGER PRIMARY KEY,
	name          TEXT NOT NULL UNIQUE COLLATE NOCASE,
	default_price INTEGER NOT NULL DEFAULT 0 CHECK (default_price >= 0),
	unit          TEXT NOT NULL DEFAULT 'job',
	manual_price  INTEGER NOT NULL DEFAULT 1 CHECK (manual_price IN (0,1)),
	is_custom     INTEGER NOT NULL DEFAULT 0 CHECK (is_custom IN (0,1)),
	active        INTEGER NOT NULL DEFAULT 1 CHECK (active IN (0,1)),
	sort_order    INTEGER NOT NULL DEFAULT 0,
	created_at    TEXT NOT NULL,
	updated_at    TEXT NOT NULL
);

CREATE TABLE jobs (
	id               INTEGER PRIMARY KEY,
	job_number       TEXT NOT NULL UNIQUE,
	customer_id      INTEGER NOT NULL REFERENCES customers(id),
	vehicle_id       INTEGER REFERENCES vehicles(id),
	job_date         TEXT NOT NULL,
	odometer         INTEGER CHECK (odometer IS NULL OR odometer >= 0),
	reported_issue   TEXT NOT NULL DEFAULT '',
	inspection_notes TEXT NOT NULL DEFAULT '',
	work_performed   TEXT NOT NULL DEFAULT '',
	internal_notes   TEXT NOT NULL DEFAULT '',
	assigned_user_id INTEGER REFERENCES users(id),
	status           TEXT NOT NULL DEFAULT 'Open'
	                 CHECK (status IN ('Open','In Progress','Waiting for Parts','Completed','Cancelled')),
	completed_date   TEXT,
	created_by       INTEGER NOT NULL REFERENCES users(id),
	created_at       TEXT NOT NULL,
	updated_at       TEXT NOT NULL
);
CREATE INDEX ix_jobs_status ON jobs(status);
CREATE INDEX ix_jobs_date ON jobs(job_date);
CREATE INDEX ix_jobs_completed ON jobs(completed_date);
CREATE INDEX ix_jobs_customer ON jobs(customer_id);
CREATE INDEX ix_jobs_vehicle ON jobs(vehicle_id);
CREATE INDEX ix_jobs_assigned ON jobs(assigned_user_id);

CREATE TABLE job_charges (
	id               INTEGER PRIMARY KEY,
	job_id           INTEGER NOT NULL REFERENCES jobs(id),
	category_id      INTEGER REFERENCES charge_categories(id),
	description      TEXT NOT NULL CHECK (length(description) > 0),
	quantity         INTEGER NOT NULL CHECK (quantity > 0),
	unit             TEXT NOT NULL DEFAULT '',
	unit_price       INTEGER NOT NULL CHECK (unit_price >= 0),
	line_total       INTEGER NOT NULL CHECK (line_total >= 0),
	price_overridden INTEGER NOT NULL DEFAULT 0 CHECK (price_overridden IN (0,1)),
	notes            TEXT NOT NULL DEFAULT '',
	created_by       INTEGER NOT NULL REFERENCES users(id),
	created_at       TEXT NOT NULL,
	updated_at       TEXT NOT NULL
);
CREATE INDEX ix_job_charges_job ON job_charges(job_id);

CREATE TABLE parts (
	id            INTEGER PRIMARY KEY,
	code          TEXT NOT NULL UNIQUE COLLATE NOCASE CHECK (length(code) > 0),
	name          TEXT NOT NULL CHECK (length(name) > 0),
	description   TEXT NOT NULL DEFAULT '',
	category      TEXT NOT NULL DEFAULT '',
	compatible    TEXT NOT NULL DEFAULT '',
	unit          TEXT NOT NULL DEFAULT 'pcs',
	sale_price    INTEGER NOT NULL DEFAULT 0 CHECK (sale_price >= 0),
	purchase_cost INTEGER NOT NULL DEFAULT 0 CHECK (purchase_cost >= 0),
	qty_on_hand   INTEGER NOT NULL DEFAULT 0,
	reorder_level INTEGER NOT NULL DEFAULT 0 CHECK (reorder_level >= 0),
	location      TEXT NOT NULL DEFAULT '',
	active        INTEGER NOT NULL DEFAULT 1 CHECK (active IN (0,1)),
	created_at    TEXT NOT NULL,
	updated_at    TEXT NOT NULL
);
CREATE INDEX ix_parts_name ON parts(name COLLATE NOCASE);
CREATE INDEX ix_parts_category ON parts(category);

CREATE TABLE job_parts (
	id               INTEGER PRIMARY KEY,
	job_id           INTEGER NOT NULL REFERENCES jobs(id),
	part_id          INTEGER NOT NULL REFERENCES parts(id),
	description      TEXT NOT NULL,
	quantity         INTEGER NOT NULL CHECK (quantity > 0),
	unit_price       INTEGER NOT NULL CHECK (unit_price >= 0),
	unit_cost        INTEGER NOT NULL DEFAULT 0 CHECK (unit_cost >= 0),
	line_total       INTEGER NOT NULL CHECK (line_total >= 0),
	price_overridden INTEGER NOT NULL DEFAULT 0 CHECK (price_overridden IN (0,1)),
	notes            TEXT NOT NULL DEFAULT '',
	created_by       INTEGER NOT NULL REFERENCES users(id),
	created_at       TEXT NOT NULL,
	updated_at       TEXT NOT NULL
);
CREATE INDEX ix_job_parts_job ON job_parts(job_id);
CREATE INDEX ix_job_parts_part ON job_parts(part_id);

CREATE TABLE suppliers (
	id             INTEGER PRIMARY KEY,
	name           TEXT NOT NULL CHECK (length(name) > 0),
	contact_person TEXT NOT NULL DEFAULT '',
	phone          TEXT NOT NULL DEFAULT '',
	email          TEXT NOT NULL DEFAULT '',
	address        TEXT NOT NULL DEFAULT '',
	notes          TEXT NOT NULL DEFAULT '',
	active         INTEGER NOT NULL DEFAULT 1 CHECK (active IN (0,1)),
	created_at     TEXT NOT NULL,
	updated_at     TEXT NOT NULL
);
CREATE INDEX ix_suppliers_name ON suppliers(name COLLATE NOCASE);

CREATE TABLE purchases (
	id              INTEGER PRIMARY KEY,
	purchase_number TEXT NOT NULL UNIQUE,
	supplier_id     INTEGER NOT NULL REFERENCES suppliers(id),
	purchase_date   TEXT NOT NULL,
	reference       TEXT NOT NULL DEFAULT '',
	notes           TEXT NOT NULL DEFAULT '',
	status          TEXT NOT NULL DEFAULT 'Draft' CHECK (status IN ('Draft','Received','Cancelled')),
	total           INTEGER NOT NULL DEFAULT 0,
	received_at     TEXT,
	received_by     INTEGER REFERENCES users(id),
	cancelled_at    TEXT,
	cancelled_by    INTEGER REFERENCES users(id),
	cancel_reason   TEXT NOT NULL DEFAULT '',
	created_by      INTEGER NOT NULL REFERENCES users(id),
	created_at      TEXT NOT NULL,
	updated_at      TEXT NOT NULL
);
CREATE INDEX ix_purchases_supplier ON purchases(supplier_id);
CREATE INDEX ix_purchases_date ON purchases(purchase_date);
CREATE INDEX ix_purchases_status ON purchases(status);

CREATE TABLE purchase_items (
	id          INTEGER PRIMARY KEY,
	purchase_id INTEGER NOT NULL REFERENCES purchases(id),
	part_id     INTEGER NOT NULL REFERENCES parts(id),
	quantity    INTEGER NOT NULL CHECK (quantity > 0),
	unit_cost   INTEGER NOT NULL CHECK (unit_cost >= 0),
	line_total  INTEGER NOT NULL CHECK (line_total >= 0)
);
CREATE INDEX ix_purchase_items_purchase ON purchase_items(purchase_id);
CREATE INDEX ix_purchase_items_part ON purchase_items(part_id);

CREATE TABLE stock_movements (
	id                INTEGER PRIMARY KEY,
	part_id           INTEGER NOT NULL REFERENCES parts(id),
	movement_type     TEXT NOT NULL CHECK (movement_type IN
	                  ('opening','job_issue','job_return','purchase_receipt','purchase_reversal','adjustment')),
	quantity          INTEGER NOT NULL CHECK (quantity <> 0),
	balance_after     INTEGER NOT NULL,
	unit_cost         INTEGER,
	job_id            INTEGER REFERENCES jobs(id),
	purchase_id       INTEGER REFERENCES purchases(id),
	reason            TEXT NOT NULL DEFAULT '',
	negative_override INTEGER NOT NULL DEFAULT 0 CHECK (negative_override IN (0,1)),
	user_id           INTEGER NOT NULL REFERENCES users(id),
	created_at        TEXT NOT NULL
);
CREATE INDEX ix_stock_movements_part ON stock_movements(part_id, created_at);
CREATE INDEX ix_stock_movements_created ON stock_movements(created_at);
CREATE INDEX ix_stock_movements_job ON stock_movements(job_id);
CREATE INDEX ix_stock_movements_purchase ON stock_movements(purchase_id);

CREATE TABLE invoices (
	id               INTEGER PRIMARY KEY,
	invoice_number   TEXT NOT NULL UNIQUE,
	job_id           INTEGER NOT NULL REFERENCES jobs(id),
	customer_id      INTEGER NOT NULL REFERENCES customers(id),
	vehicle_id       INTEGER REFERENCES vehicles(id),
	invoice_date     TEXT NOT NULL,
	customer_name    TEXT NOT NULL,
	customer_phone   TEXT NOT NULL DEFAULT '',
	customer_email   TEXT NOT NULL DEFAULT '',
	customer_address TEXT NOT NULL DEFAULT '',
	vehicle_reg      TEXT NOT NULL DEFAULT '',
	vehicle_desc     TEXT NOT NULL DEFAULT '',
	odometer         INTEGER,
	subtotal         INTEGER NOT NULL CHECK (subtotal >= 0),
	discount         INTEGER NOT NULL DEFAULT 0 CHECK (discount >= 0 AND discount <= subtotal),
	discount_note    TEXT NOT NULL DEFAULT '',
	tax_name         TEXT NOT NULL DEFAULT '',
	tax_rate_bp      INTEGER NOT NULL DEFAULT 0 CHECK (tax_rate_bp >= 0),
	tax_amount       INTEGER NOT NULL DEFAULT 0 CHECK (tax_amount >= 0),
	total            INTEGER NOT NULL CHECK (total >= 0),
	amount_paid      INTEGER NOT NULL DEFAULT 0,
	payment_status   TEXT NOT NULL DEFAULT 'Unpaid' CHECK (payment_status IN ('Unpaid','Partially Paid','Paid')),
	status           TEXT NOT NULL DEFAULT 'Issued' CHECK (status IN ('Issued','Void')),
	void_reason      TEXT NOT NULL DEFAULT '',
	voided_at        TEXT,
	voided_by        INTEGER REFERENCES users(id),
	replaces_invoice_id INTEGER REFERENCES invoices(id),
	notes            TEXT NOT NULL DEFAULT '',
	created_by       INTEGER NOT NULL REFERENCES users(id),
	created_at       TEXT NOT NULL
);
-- Only one issued (non-void) invoice per job.
CREATE UNIQUE INDEX ux_invoices_job_issued ON invoices(job_id) WHERE status = 'Issued';
CREATE INDEX ix_invoices_date ON invoices(invoice_date);
CREATE INDEX ix_invoices_customer ON invoices(customer_id);
CREATE INDEX ix_invoices_payment_status ON invoices(payment_status);

CREATE TABLE invoice_lines (
	id          INTEGER PRIMARY KEY,
	invoice_id  INTEGER NOT NULL REFERENCES invoices(id),
	line_type   TEXT NOT NULL CHECK (line_type IN ('charge','part')),
	part_id     INTEGER REFERENCES parts(id),
	part_code   TEXT NOT NULL DEFAULT '',
	description TEXT NOT NULL,
	notes       TEXT NOT NULL DEFAULT '',
	quantity    INTEGER NOT NULL CHECK (quantity > 0),
	unit        TEXT NOT NULL DEFAULT '',
	unit_price  INTEGER NOT NULL CHECK (unit_price >= 0),
	unit_cost   INTEGER NOT NULL DEFAULT 0,
	line_total  INTEGER NOT NULL CHECK (line_total >= 0),
	sort_order  INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX ix_invoice_lines_invoice ON invoice_lines(invoice_id);
CREATE INDEX ix_invoice_lines_part ON invoice_lines(part_id);

CREATE TABLE payments (
	id             INTEGER PRIMARY KEY,
	receipt_number TEXT NOT NULL UNIQUE,
	invoice_id     INTEGER NOT NULL REFERENCES invoices(id),
	payment_date   TEXT NOT NULL,
	amount         INTEGER NOT NULL CHECK (amount > 0),
	method         TEXT NOT NULL CHECK (method IN ('Cash','Card','Bank Transfer','Cheque','Other')),
	reference      TEXT NOT NULL DEFAULT '',
	notes          TEXT NOT NULL DEFAULT '',
	overpayment    INTEGER NOT NULL DEFAULT 0 CHECK (overpayment IN (0,1)),
	received_by    INTEGER NOT NULL REFERENCES users(id),
	status         TEXT NOT NULL DEFAULT 'Valid' CHECK (status IN ('Valid','Void')),
	void_reason    TEXT NOT NULL DEFAULT '',
	voided_at      TEXT,
	voided_by      INTEGER REFERENCES users(id),
	created_at     TEXT NOT NULL
);
CREATE INDEX ix_payments_invoice ON payments(invoice_id);
CREATE INDEX ix_payments_date ON payments(payment_date);

CREATE TABLE audit_log (
	id          INTEGER PRIMARY KEY,
	user_id     INTEGER REFERENCES users(id),
	username    TEXT NOT NULL DEFAULT '',
	action      TEXT NOT NULL,
	entity_type TEXT NOT NULL DEFAULT '',
	entity_id   INTEGER,
	details     TEXT NOT NULL DEFAULT '',
	ip          TEXT NOT NULL DEFAULT '',
	created_at  TEXT NOT NULL
);
CREATE INDEX ix_audit_created ON audit_log(created_at);
CREATE INDEX ix_audit_entity ON audit_log(entity_type, entity_id);
`,
}
