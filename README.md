# Garage & Spare-Parts Manager

A garage job and spare-parts inventory system for a small garage that also runs a parts shop.
It is a single Go program with one SQLite database file. The web interface uses plain HTML, CSS and JavaScript, and there is no frontend build step.

The example configuration is set up for **Sisira Motors Yatawatta** (LKR, `Rs.`, Asia/Colombo).
Business details, currency, tax and invoice numbering can all be changed under **Settings**. Nothing business-specific is written into the code.

## Contents

1. [Key decisions](#key-decisions)
2. [Requirements](#requirements)
3. [Setup and first run](#setup-and-first-run)
4. [Configuration](#configuration)
5. [Using it on the local network (LAN)](#using-it-on-the-local-network-lan)
6. [Backup and restore](#backup-and-restore)
7. [Demo data (optional)](#demo-data-optional)
8. [Features](#features)
9. [Roles and permissions](#roles-and-permissions)
10. [Data and integrity rules](#data-and-integrity-rules)
11. [Project layout](#project-layout)
12. [Limitations](#limitations)

## Key decisions

| Topic | Decision |
|---|---|
| HTTP | Go standard library `net/http` with Go 1.22+ method/path routing, and `html/template` (output is escaped automatically). No web framework. |
| SQLite driver | [`modernc.org/sqlite`](https://pkg.go.dev/modernc.org/sqlite) **v1.46.0**, pinned in `go.mod`. It is pure Go, so you need **no C compiler or CGO**, and it builds the same way on Windows, Linux and macOS. |
| Passwords | bcrypt (cost 12) from `golang.org/x/crypto`. Plain-text passwords are never stored. |
| Assets | Templates, CSS and JS are embedded in the binary with `embed`, so you deploy one executable plus `config.json`. |
| Money | Stored as `INTEGER` **minor units**, e.g. cents (Rs. 1,250.50 is stored as `125050`). No floating point is used. |
| Quantities | Stored as `INTEGER` **thousandths**, so 1.5 labour hours is `1500` and 0.5 L of oil is `500`. Line total = quantity × unit price, rounded half-up to a whole minor unit. |
| Timestamps | System times (`*_at` columns) are UTC text in the form `YYYY-MM-DDTHH:MM:SSZ`. Business dates (job, invoice, payment and purchase dates) are local text in the form `YYYY-MM-DD`, using the configured timezone. |
| Database | Foreign keys are enforced, WAL journal mode is on, and write transactions use `BEGIN IMMEDIATE`. Numbered migrations run on startup and are never re-run. **The database is never reset or recreated.** |
| Invoice integrity | An issued invoice locks its job's charges and parts. To correct one, an authorised user **voids** the invoice with a reason (it is kept in history) and issues a new invoice that references the voided one. Payments must be voided before their invoice can be. |
| Counter sales | The vehicle on a job is optional, so parts-only sales use the same job → invoice → payment flow. |

**Assumptions**

- Only jobs with status **Completed** can be invoiced.
- Cancelling a job automatically returns all its parts to stock, with reversal movements recorded.
- Tax, when enabled, is added on top of the subtotal after any discount.
- Receiving a purchase updates each part's "purchase cost" to the latest received cost.

## Requirements

- **Go 1.26 or newer** (the pinned dependency versions require it). Download it from <https://go.dev/dl/>.
- A modern browser: Chrome, Edge, Firefox or Safari, on a desktop, tablet or phone.
- No other software. The SQLite engine is compiled into the program.

## Setup and first run

```bash
# 1. Get the dependencies (versions are pinned in go.mod / go.sum)
go mod download

# 2. Build a single executable
go build -buildvcs=false -o garage ./cmd/server          # Windows: go build -buildvcs=false -o garage.exe ./cmd/server

# 3. Create your configuration from the safe example
cp config.example.json config.json       # Windows: copy config.example.json config.json
#    then edit business name/address/phone, timezone, currency if needed

# 4. Create (or upgrade) the database. Safe to run any number of times.
./garage init

# 5. Create the first administrator (you will be asked for a password, minimum 8 characters)
./garage create-admin -username admin -name "Your Name"

# 6. Start the server
./garage
```

Then open **<http://127.0.0.1:8080>** in a browser on the same computer and sign in.

The server also applies migrations on every start, so step 4 is optional.
`initial_business` values in `config.json` are copied into the database only when it is first created. After that, change business details under **Settings → Business & invoices**.

Other commands:

| Command | Purpose |
|---|---|
| `./garage` or `./garage serve` | Run the web server |
| `./garage init` | Create or migrate the database and exit |
| `./garage create-admin -username NAME -name "Full Name"` | Add an administrator. For scripts, set the `GARAGE_ADMIN_PASSWORD` environment variable instead of typing the password. |
| `./garage backup -out FILE.db` | Write a consistent copy of the live database |
| `./garage seed-demo` | Load optional sample data (only works on an empty database) |

All commands accept `-config path/to/config.json`.

Run the automated end-to-end test with:

```bash
go test ./...
```

It covers the full acceptance flow, permission and CSRF checks, stock rules and invoice locking, and it renders every page.

## Configuration

`config.json` (see `config.example.json`):

| Key | Default | Meaning |
|---|---|---|
| `listen_addr` | `127.0.0.1:8080` | Address and port to listen on. The default is **local-only**. |
| `db_path` | `data/garage.db` | SQLite file. A relative path is relative to the folder containing `config.json`. Its folder is created if needed. |
| `timezone` | `Local` | IANA name, e.g. `Asia/Colombo`. Used for business dates and display. |
| `session_hours` | `12` | Sign-in lifetime. It slides forward while you keep using the app. |
| `cookie_secure` | `false` | Set to `true` when serving over HTTPS. This is automatic when TLS files are set. |
| `tls_cert_file`, `tls_key_file` | empty | Serve HTTPS directly. |
| `initial_business` | — | Initial name, address, currency code, symbol and decimals, invoice prefix and footer. |

These environment variables override the file: `GARAGE_LISTEN_ADDR`, `GARAGE_DB_PATH`, `GARAGE_TIMEZONE`, `GARAGE_COOKIE_SECURE`, `GARAGE_TLS_CERT_FILE` and `GARAGE_TLS_KEY_FILE`.

Currency decimal places cannot be changed once any prices exist, because stored amounts would change meaning.

## Using it on the local network (LAN)

By default only the computer running the server can reach it. To let staff use tablets or phones on the garage network:

1. Give the server PC a **fixed LAN IP address**, for example `192.168.1.20` (set this in the router or in Windows network settings).
2. Set `"listen_addr": "192.168.1.20:8080"`. Use `"0.0.0.0:8080"` instead to listen on all network interfaces.
3. Allow inbound TCP port 8080 in the PC's firewall **for the private/LAN profile only**.
4. Staff then open `http://192.168.1.20:8080`.

**Securing a LAN deployment**

- **Never forward the port on your router to the internet.** This app is meant for a trusted local network.
- Use **HTTPS** where possible. Create a certificate, for example with [mkcert](https://github.com/FiloSottile/mkcert) for `192.168.1.20`, then set `tls_cert_file` and `tls_key_file`. This also enables secure cookies. Without TLS, sign-in cookies travel unencrypted on the Wi-Fi.
- Use a WPA2/WPA3-protected Wi-Fi network that customers cannot join, or a separate guest network for customers.
- Give each staff member their own account with the least-privileged role, and deactivate accounts when people leave.
- Built-in protections:
  - Login throttling: 5 failed attempts in 15 minutes locks that username and IP for 15 minutes.
  - CSRF tokens on every form, plus an Origin check.
  - A strict Content-Security-Policy (no inline scripts).
  - `HttpOnly` and `SameSite` cookies.
  - Server-side permission checks on every route.
  - Parameterised SQL everywhere.
  - An audit log of important actions.
- Keep the PC's operating system updated and **make regular backups** (see below).

## Backup and restore

SQLite keeps everything in `data/garage.db`. While the server runs in WAL mode there are also `-wal` and `-shm` files next to it. **Do not just copy `garage.db` while the server is running**, because the copy may be incomplete.

**Safe backup options (server can keep running):**

- In the app, go to **Settings → Download backup**. This downloads a consistent snapshot made with `VACUUM INTO`.
- From the command line: `./garage backup -out backups/garage-2026-10-10.db`.
- With the sqlite3 tool: `sqlite3 data/garage.db ".backup backups/garage.db"`.

Schedule a daily backup, for example with Windows Task Scheduler or cron, and keep copies on another device or USB drive.

**Restore:**

1. Stop the server.
2. Move the current `data/garage.db`, `garage.db-wal` and `garage.db-shm` somewhere safe.
3. Copy the backup file to `data/garage.db`.
4. Start the server. Any pending migrations are applied automatically.

## Demo data (optional)

`./garage seed-demo` adds 2 suppliers, 6 parts with opening stock, 2 customers with vehicles and one in-progress job.

- It only runs when there are no customers, parts or suppliers, and it requires an existing administrator.
- It never runs automatically.
- Don't use it on a production database. Start from a fresh `db_path` if you want to try it out.

## Features

**Customers and vehicles**
- Create, edit, search, archive and restore customers.
- Each customer can have multiple vehicles.
- Search by name, phone (digits only), email or registration. Spaces and dashes are ignored, so `wp cab 1234` matches `WP CAB-1234`.
- Vehicle ownership can be transferred.
- Duplicate active registrations are rejected.
- The customer page shows their vehicles, jobs, invoices and balance due. The vehicle page shows its service history.

**Jobs**
- Open a job by searching for a customer or vehicle.
- Each job records date, odometer, reported issue, inspection notes, work performed, internal notes and assigned staff member.
- Statuses: Open, In Progress, Waiting for Parts, Completed and Cancelled.
- The job screen shows charges, parts, subtotals, invoice, amount paid and balance together.
- Filter jobs by text (customer, phone or registration), status, assigned staff member and date range. Sort and paginate the list.

**Labour and extra charges**
- Default charge categories: Garage labour, Shop labour, Welding, Grease service, Lathe work, Battery service and Custom charge.
- Administrators can add, edit, deactivate and reorder categories, and set each category's default price and unit.
- Each category can be marked "price per job" (staff enter the price) and/or "custom" (a description is required).
- Charge lines store description, quantity or hours, unit price, line total and notes.
- Entering a price that differs from the default, when the category doesn't allow per-job pricing, requires the `jobs.price_override` permission.
- The recorded price stays with the job even if the default price changes later.

**Inventory**
- Parts store a unique code (case-insensitive), name, description, category, compatible vehicles, unit, sale price, purchase cost, stock, reorder level and shelf location.
- Low-stock warnings appear on the parts list and dashboard.
- Each part has a full movement history.
- Stock adjustments are either "add/remove" or "set to counted quantity", and both require a reason.
- Stock cannot go below zero unless the user holds `stock.negative` **and** ticks an authorisation box. The override is flagged and audited.

**Parts on jobs**
- Adding a part deducts stock in the **same transaction** that records the movement.
- Changing the quantity applies only the difference.
- Removing a part, or cancelling the job, returns the stock with a reversal movement.
- Creating an invoice does **not** deduct stock again.

**Suppliers and purchases**
- Supplier records.
- Purchases move from Draft to Received, or are Cancelled.
- Line items record part, quantity and cost.
- Stock increases **only** when a purchase is received.
- Cancelling a received purchase reverses its stock, with movements recorded.
- View purchase history by supplier, date, status or part.

**Invoices and payments**
- Invoices are created from completed jobs.
- Each invoice number is unique and uses a configurable prefix, next number and number of digits.
- Every line's description, quantity, unit and price is stored as a snapshot, along with customer and vehicle details.
- Optional discount and configurable tax.
- Full and partial payments, recorded with date, amount, method, reference and receiving staff member.
- Payment status: Unpaid, Partially Paid or Paid.
- A payment larger than the balance is rejected unless an administrator authorises it with a note. This is audited.
- Payments and invoices can be voided with a reason.
- Printable bill and printable payment receipts, using browser printing with print-specific CSS.

**Reports** (date range plus filters; all printable)
- Jobs opened and completed, by status and staff member.
- Sales, tax and discounts, with payments received by method.
- Outstanding balances, with days outstanding.
- Parts sold, with a cost-based margin. Margin is labelled clearly and shown only with `reports.profit`.
- Stock levels and stock value, with a low-stock filter.
- Purchases by supplier.
- Stock movements by type.

**Administration**
- Users, with password reset and forced password change.
- Roles and a permission matrix. Extra roles can be added.
- Business, tax and numbering settings.
- Audit log viewer.
- Backup download.

**Interface**
- Responsive layout: sidebar on desktop, collapsible menu on phones.
- Lists support search, sorting and pagination.
- Validation messages appear beside the fields they relate to.
- Success and error messages after actions.
- Empty states that explain the next step.
- Labelled inputs, keyboard-friendly forms and a "skip to content" link.
- Confirmation prompts for destructive actions.
- Money and dates are formatted consistently.

## Roles and permissions

There are 32 fine-grained permissions, enforced on the server for every page and action. The default roles are:

| Role | Default access |
|---|---|
| Administrator | Everything. This system role always keeps every permission. |
| Manager | Customers, jobs (including price override), invoices, voids, payments, inventory, purchases, reports and margins |
| Front desk | Customers, vehicles, jobs, charges and parts on jobs, invoices and recording payments |
| Mechanic | **Only jobs assigned to them**: notes, status, charges and parts. No customers, invoices or administration. |
| Inventory clerk | Parts, stock adjustments, suppliers, purchases and receiving, and stock reports |

Change these under **Settings → Roles & permissions**. For example, you can let the front desk void payments.
The app makes sure at least one active user always keeps `users.manage`.

## Data and integrity rules

- These operations each run in a single transaction and roll back completely on any error:
  - Adding, changing or removing parts on a job, together with its stock movements
  - Receiving or cancelling a purchase
  - Creating an invoice and its line snapshots
  - Recording or voiding a payment and updating the invoice balance
  - Changing a job's status (cancelling returns parts to stock)
- Each record type has its own number sequence (job, invoice, receipt, purchase), reserved inside the transaction that uses it.
- Database constraints back up the application rules:
  - Unique indexes allow only one *issued* invoice per job and one *active* vehicle per registration.
  - Foreign keys link related records.
  - `CHECK` constraints guard statuses, positive quantities and non-negative prices.
- Records that affect invoices, payments or stock are archived, deactivated or voided rather than deleted.
- The audit log records sign-ins, customer, job, part and supplier changes, charge and part line changes, invoices and voids, payments, stock adjustments and negative overrides, purchase receipts and cancellations, settings, users, roles and permissions, and backups.

## Project layout

```
cmd/server/            entry point and CLI commands (serve, init, create-admin, backup, seed-demo)
internal/config/       configuration loading and validation
internal/database/     connection, migrations, default reference data
internal/models/       data structures, money/quantity types, permissions
internal/repository/   parameterised SQL queries
internal/service/      business rules and transactions (stock, invoices, payments, purchases)
internal/handlers/     HTTP handlers, routing, rendering, end-to-end test
internal/middleware/   sessions, auth, permissions, CSRF, security headers, logging
web/templates/         html/template pages
web/static/            CSS and JavaScript (no build step)
```

## Limitations

- **No CSV or PDF export.** Reports and bills are printed or saved as PDF through the browser's print dialog.
- **Discounts apply to the whole invoice only**, not to individual lines. There is one tax rate, applied to the whole invoice.
- **Credit notes and refunds are not separate documents.** Corrections use void-and-reissue, and money handed back is recorded by voiding the payment with a reason.
- **Parts with the same code in different units** need separate part records. There is no unit conversion.
- **Vehicle ownership is transferred by customer ID** on the vehicle edit form, rather than through a search picker.
- **Emails and SMS are not sent.**
- **No built-in TLS certificate management.** Supply your own certificate files, as described above.
- **Login throttling is held in memory** and resets when the server restarts.
- **Designed for one server process.** Don't run two servers against the same database file over a network share.
