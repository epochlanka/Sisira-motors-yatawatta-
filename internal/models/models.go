package models

import (
	"database/sql"
	"strings"
	"time"
	"unicode"
)

// Timestamp format used for all system timestamps (stored as TEXT, always UTC).
const TimeLayout = "2006-01-02T15:04:05Z"

// DateLayout is used for business dates (job, invoice, payment, purchase dates) in local time.
const DateLayout = "2006-01-02"

func NowUTC() string { return time.Now().UTC().Format(TimeLayout) }

var location = time.Local

func SetLocation(l *time.Location) { location = l }
func Location() *time.Location     { return location }

// Today returns the current local business date.
func Today() string { return time.Now().In(location).Format(DateLayout) }

// ValidDate reports whether s is a valid YYYY-MM-DD date.
func ValidDate(s string) bool {
	_, err := time.Parse(DateLayout, s)
	return err == nil
}

// NormalizeReg turns "wp cab-1234" into "WPCAB1234" for searching and uniqueness.
func NormalizeReg(s string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// DigitsOnly keeps only digits (phone search).
func DigitsOnly(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

type User struct {
	ID                 int64
	Username           string
	FullName           string
	PasswordHash       string
	RoleID             int64
	RoleName           string
	Active             bool
	MustChangePassword bool
	CreatedAt          string
	UpdatedAt          string
}

type Role struct {
	ID          int64
	Name        string
	Description string
	IsSystem    bool
	Permissions map[string]bool
	UserCount   int
}

type Customer struct {
	ID        int64
	Name      string
	Phone     string
	Email     string
	Address   string
	Notes     string
	Archived  bool
	CreatedAt string
	UpdatedAt string
	// list helpers
	VehicleCount int
	Balance      Money
}

type Vehicle struct {
	ID            int64
	CustomerID    int64
	CustomerName  string
	CustomerPhone string
	Registration  string
	Make          string
	Model         string
	Year          sql.NullInt64
	VIN           string
	Notes         string
	Archived      bool
	CreatedAt     string
	UpdatedAt     string
	// service reminder
	NextServiceDate string
	NextServiceKm   sql.NullInt64
	ServiceNote     string
	LastOdometer    sql.NullInt64
	LastVisit       string
}

// ServiceDue reports whether the reminder date is today or earlier.
func (v Vehicle) ServiceDue(today string) bool {
	return v.NextServiceDate != "" && v.NextServiceDate <= today
}

func (v Vehicle) Label() string {
	s := v.Registration
	mm := strings.TrimSpace(v.Make + " " + v.Model)
	if mm != "" {
		s += " – " + mm
	}
	return s
}

const (
	JobOpen       = "Open"
	JobInProgress = "In Progress"
	JobWaiting    = "Waiting for Parts"
	JobCompleted  = "Completed"
	JobCancelled  = "Cancelled"
)

var JobStatuses = []string{JobOpen, JobInProgress, JobWaiting, JobCompleted, JobCancelled}

func ValidJobStatus(s string) bool {
	for _, x := range JobStatuses {
		if x == s {
			return true
		}
	}
	return false
}

type Job struct {
	ID              int64
	JobNumber       string
	CustomerID      int64
	CustomerName    string
	CustomerPhone   string
	VehicleID       sql.NullInt64
	VehicleReg      string
	VehicleDesc     string
	JobDate         string
	Odometer        sql.NullInt64
	ReportedIssue   string
	InspectionNotes string
	WorkPerformed   string
	InternalNotes   string
	AssignedUserID  sql.NullInt64
	AssignedName    string
	Status          string
	CompletedDate   string
	CreatedBy       int64
	CreatedAt       string
	UpdatedAt       string
	// computed
	ChargesTotal Money
	PartsTotal   Money
	InvoiceID    sql.NullInt64
	InvoiceNo    string
}

func (j Job) Total() Money { return j.ChargesTotal + j.PartsTotal }

type ChargeCategory struct {
	ID           int64
	Name         string
	DefaultPrice Money
	Unit         string
	ManualPrice  bool // price may be entered per job without override permission
	IsCustom     bool // description must be entered per line
	Active       bool
	SortOrder    int
}

type JobCharge struct {
	ID              int64
	JobID           int64
	CategoryID      sql.NullInt64
	CategoryName    string
	Description     string
	Quantity        Qty
	Unit            string
	UnitPrice       Money
	LineTotal       Money
	PriceOverridden bool
	Notes           string
	CreatedAt       string
}

type JobPart struct {
	ID              int64
	JobID           int64
	PartID          int64
	PartCode        string
	Description     string
	Unit            string
	Quantity        Qty
	UnitPrice       Money
	UnitCost        Money
	LineTotal       Money
	PriceOverridden bool
	Notes           string
	CreatedAt       string
	InStock         Qty
}

type Part struct {
	ID           int64
	Code         string
	Name         string
	Description  string
	Category     string
	Compatible   string
	Unit         string
	SalePrice    Money
	PurchaseCost Money
	QtyOnHand    Qty
	ReorderLevel Qty
	Location     string
	Active       bool
	CreatedAt    string
	UpdatedAt    string
}

func (p Part) Low() bool { return p.Active && p.QtyOnHand <= p.ReorderLevel }

const (
	MoveOpening         = "opening"
	MoveJobIssue        = "job_issue"
	MoveJobReturn       = "job_return"
	MovePurchaseReceipt = "purchase_receipt"
	MovePurchaseReverse = "purchase_reversal"
	MoveAdjustment      = "adjustment"
)

var MovementTypes = []string{MoveOpening, MoveJobIssue, MoveJobReturn, MovePurchaseReceipt, MovePurchaseReverse, MoveAdjustment}

func MovementLabel(t string) string {
	switch t {
	case MoveOpening:
		return "Opening stock"
	case MoveJobIssue:
		return "Issued to job"
	case MoveJobReturn:
		return "Returned from job"
	case MovePurchaseReceipt:
		return "Purchase received"
	case MovePurchaseReverse:
		return "Purchase reversed"
	case MoveAdjustment:
		return "Adjustment"
	}
	return t
}

type StockMovement struct {
	ID               int64
	PartID           int64
	PartCode         string
	PartName         string
	Type             string
	Quantity         Qty
	BalanceAfter     Qty
	UnitCost         sql.NullInt64
	JobID            sql.NullInt64
	JobNumber        string
	PurchaseID       sql.NullInt64
	PurchaseNumber   string
	Reason           string
	NegativeOverride bool
	UserName         string
	CreatedAt        string
}

func (m StockMovement) TypeLabel() string { return MovementLabel(m.Type) }

type Supplier struct {
	ID            int64
	Name          string
	ContactPerson string
	Phone         string
	Email         string
	Address       string
	Notes         string
	Active        bool
	CreatedAt     string
	UpdatedAt     string
}

const (
	PurchaseDraft     = "Draft"
	PurchaseReceived  = "Received"
	PurchaseCancelled = "Cancelled"
)

type Purchase struct {
	ID             int64
	PurchaseNumber string
	SupplierID     int64
	SupplierName   string
	PurchaseDate   string
	Reference      string
	Notes          string
	Status         string
	Total          Money
	ReceivedAt     string
	ReceivedBy     string
	CancelledAt    string
	CancelReason   string
	CreatedAt      string
	ItemCount      int
}

type PurchaseItem struct {
	ID         int64
	PurchaseID int64
	PartID     int64
	PartCode   string
	PartName   string
	Unit       string
	Quantity   Qty
	UnitCost   Money
	LineTotal  Money
}

const (
	InvoiceIssued = "Issued"
	InvoiceVoid   = "Void"

	PayUnpaid  = "Unpaid"
	PayPartial = "Partially Paid"
	PayPaid    = "Paid"
)

func PaymentStatus(total, paid Money) string {
	switch {
	case paid >= total:
		return PayPaid
	case paid <= 0:
		return PayUnpaid
	default:
		return PayPartial
	}
}

type Invoice struct {
	ID              int64
	InvoiceNumber   string
	JobID           int64
	JobNumber       string
	CustomerID      int64
	VehicleID       sql.NullInt64
	InvoiceDate     string
	CustomerName    string
	CustomerPhone   string
	CustomerAddress string
	CustomerEmail   string
	VehicleReg      string
	VehicleDesc     string
	Odometer        sql.NullInt64
	Subtotal        Money
	Discount        Money
	DiscountNote    string
	TaxName         string
	TaxRateBP       int64
	TaxAmount       Money
	Total           Money
	AmountPaid      Money
	PaymentStatus   string
	Status          string
	VoidReason      string
	VoidedAt        string
	VoidedBy        string
	ReplacesID      sql.NullInt64
	ReplacesNumber  string
	Notes           string
	CreatedBy       string
	CreatedAt       string
}

func (i Invoice) Balance() Money { return i.Total - i.AmountPaid }
func (i Invoice) IsVoid() bool   { return i.Status == InvoiceVoid }

func (i Invoice) TaxRateLabel() string {
	whole := i.TaxRateBP / 100
	frac := i.TaxRateBP % 100
	s := itoa(whole)
	if frac != 0 {
		f := itoa(frac + 100)[1:]
		s += "." + strings.TrimRight(f, "0")
	}
	return s + "%"
}

func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var b [24]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

type InvoiceLine struct {
	ID          int64
	InvoiceID   int64
	LineType    string // charge | part
	PartID      sql.NullInt64
	PartCode    string
	Description string
	Notes       string
	Quantity    Qty
	Unit        string
	UnitPrice   Money
	UnitCost    Money
	LineTotal   Money
	SortOrder   int
}

var PaymentMethods = []string{"Cash", "Card", "Bank Transfer", "Cheque", "Other"}

type Payment struct {
	ID            int64
	ReceiptNumber string
	InvoiceID     int64
	InvoiceNumber string
	CustomerName  string
	PaymentDate   string
	Amount        Money
	Method        string
	Reference     string
	Notes         string
	Overpayment   bool
	ReceivedBy    string
	Status        string // Valid | Void
	VoidReason    string
	VoidedAt      string
	CreatedAt     string
}

type AuditEntry struct {
	ID         int64
	UserID     sql.NullInt64
	Username   string
	Action     string
	EntityType string
	EntityID   sql.NullInt64
	Details    string
	IP         string
	CreatedAt  string
}

// Settings holds editable business configuration.
type Settings struct {
	BusinessName     string
	Address          string
	Phone            string
	Email            string
	RegistrationNo   string
	CurrencyCode     string
	CurrencySymbol   string
	CurrencyDecimals int
	SymbolAfter      bool
	TaxEnabled       bool
	TaxName          string
	TaxRateBP        int64
	TaxNumber        string
	DiscountEnabled  bool
	InvoicePrefix    string
	InvoiceNext      int64
	InvoicePad       int
	ReceiptPrefix    string
	JobPrefix        string
	PurchasePrefix   string
	InvoiceFooter    string
	DateFormat       string
}

func (s Settings) TaxRateLabel() string {
	return Invoice{TaxRateBP: s.TaxRateBP}.TaxRateLabel()
}

// ---------- appointments ----------

const (
	ApptScheduled = "Scheduled"
	ApptCheckedIn = "Checked In"
	ApptCancelled = "Cancelled"
	ApptNoShow    = "No Show"
)

var ApptStatuses = []string{ApptScheduled, ApptCheckedIn, ApptCancelled, ApptNoShow}

func ValidApptStatus(s string) bool {
	for _, x := range ApptStatuses {
		if x == s {
			return true
		}
	}
	return false
}

// TimeOfDayLayout is used for appointment and timesheet times ("HH:MM", 24-hour, local).
const TimeOfDayLayout = "15:04"

func ValidTimeOfDay(s string) bool {
	_, err := time.Parse(TimeOfDayLayout, s)
	return err == nil && len(s) == 5
}

// Minutes converts "HH:MM" to minutes after midnight (-1 if invalid).
func Minutes(s string) int {
	t, err := time.Parse(TimeOfDayLayout, s)
	if err != nil {
		return -1
	}
	return t.Hour()*60 + t.Minute()
}

// ClockTime formats minutes after midnight as "HH:MM" (wrapping past midnight).
func ClockTime(m int) string {
	m = ((m % 1440) + 1440) % 1440
	return time.Date(2000, 1, 1, m/60, m%60, 0, 0, time.UTC).Format(TimeOfDayLayout)
}

type Appointment struct {
	ID             int64
	ApptDate       string
	StartTime      string
	DurationMin    int
	CustomerID     sql.NullInt64
	CustomerName   string
	VehicleID      sql.NullInt64
	ContactName    string
	ContactPhone   string
	VehicleReg     string
	VehicleDesc    string
	Service        string
	Notes          string
	AssignedUserID sql.NullInt64
	AssignedName   string
	Status         string
	JobID          sql.NullInt64
	JobNumber      string
	CancelReason   string
	CreatedBy      string
	CreatedAt      string
	UpdatedAt      string
}

func (a Appointment) EndTime() string { return ClockTime(Minutes(a.StartTime) + a.DurationMin) }

// Name is the customer record name, or the contact name for people not yet registered.
func (a Appointment) Name() string {
	if a.CustomerName != "" {
		return a.CustomerName
	}
	return a.ContactName
}

func (a Appointment) Open() bool { return a.Status == ApptScheduled }

// ---------- staff time ----------

type Timesheet struct {
	ID        int64
	UserID    int64
	UserName  string
	WorkDate  string
	TimeIn    string
	TimeOut   sql.NullString
	BreakMin  int
	Notes     string
	CreatedAt string
	UpdatedAt string
}

// WorkedMin returns minutes worked, excluding breaks; 0 while still clocked in.
// A time out earlier than the time in is treated as an overnight shift.
func (t Timesheet) WorkedMin() int {
	if !t.TimeOut.Valid {
		return 0
	}
	d := Minutes(t.TimeOut.String) - Minutes(t.TimeIn)
	if d < 0 {
		d += 1440
	}
	d -= t.BreakMin
	if d < 0 {
		return 0
	}
	return d
}

func (t Timesheet) Out() string {
	if t.TimeOut.Valid {
		return t.TimeOut.String
	}
	return ""
}

// HoursLabel formats minutes as "7h 30m".
func HoursLabel(min int) string {
	h, m := min/60, min%60
	switch {
	case min <= 0:
		return "0h"
	case h == 0:
		return itoa(int64(m)) + "m"
	case m == 0:
		return itoa(int64(h)) + "h"
	}
	return itoa(int64(h)) + "h " + itoa(int64(m)) + "m"
}

// ---------- expenses ----------

var ExpenseCategories = []string{
	"Rent", "Electricity & water", "Telephone & internet", "Salaries & wages", "Tools & equipment",
	"Workshop consumables", "Repairs & maintenance", "Transport & fuel", "Licences & insurance",
	"Bank charges", "Office & stationery", "Other",
}

func ValidExpenseCategory(s string) bool {
	for _, x := range ExpenseCategories {
		if x == s {
			return true
		}
	}
	return false
}

type Expense struct {
	ID          int64
	ExpenseDate string
	Category    string
	Description string
	Amount      Money
	Method      string
	PaidTo      string
	Reference   string
	Notes       string
	Status      string // Valid | Void
	VoidReason  string
	VoidedAt    string
	CreatedBy   string
	CreatedAt   string
}

func (e Expense) IsVoid() bool { return e.Status == "Void" }
