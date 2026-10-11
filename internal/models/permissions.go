package models

type Permission struct {
	Code        string
	Group       string
	Description string
}

// AllPermissions is the canonical permission list. New permissions are inserted on startup.
var AllPermissions = []Permission{
	{"dashboard.view", "General", "View dashboard"},
	{"customers.view", "Customers", "View customers and vehicles"},
	{"customers.edit", "Customers", "Create, edit and archive customers and vehicles"},
	{"jobs.view_all", "Jobs", "View all jobs"},
	{"jobs.view_assigned", "Jobs", "View jobs assigned to self"},
	{"jobs.create", "Jobs", "Open new jobs"},
	{"jobs.edit", "Jobs", "Edit job details, customer/vehicle and assignment"},
	{"jobs.notes", "Jobs", "Edit inspection notes, work performed and internal notes"},
	{"jobs.status", "Jobs", "Change job status"},
	{"jobs.charges", "Jobs", "Add, edit and remove labour/extra charges on jobs"},
	{"jobs.parts", "Jobs", "Add, change and return parts on jobs"},
	{"jobs.price_override", "Jobs", "Enter manual prices that differ from default prices"},
	{"invoices.view", "Invoices", "View invoices and payments"},
	{"invoices.create", "Invoices", "Create invoices from completed jobs"},
	{"invoices.void", "Invoices", "Void invoices for correction"},
	{"payments.record", "Invoices", "Record payments and print receipts"},
	{"payments.void", "Invoices", "Void payments"},
	{"payments.overpay", "Invoices", "Authorize payments that exceed the balance"},
	{"parts.view", "Inventory", "View parts, stock and movements"},
	{"parts.edit", "Inventory", "Create, edit and deactivate parts"},
	{"stock.adjust", "Inventory", "Record stock adjustments"},
	{"stock.negative", "Inventory", "Authorize stock going below zero"},
	{"suppliers.view", "Purchasing", "View suppliers"},
	{"suppliers.edit", "Purchasing", "Create, edit and deactivate suppliers"},
	{"purchases.view", "Purchasing", "View purchases"},
	{"purchases.edit", "Purchasing", "Create and edit draft purchases"},
	{"purchases.receive", "Purchasing", "Receive and cancel purchases"},
	{"reports.view", "Reports", "View reports (limited to areas the user can see)"},
	{"reports.profit", "Reports", "View purchase costs, stock value and margins"},
	{"settings.manage", "Administration", "Manage business settings and charge categories"},
	{"users.manage", "Administration", "Manage users, roles and permissions"},
	{"audit.view", "Administration", "View the audit log"},
}

func ValidPermission(code string) bool {
	for _, p := range AllPermissions {
		if p.Code == code {
			return true
		}
	}
	return false
}

// DefaultRoles maps role name -> description and permissions. "*" means all.
var DefaultRoles = []struct {
	Name        string
	Description string
	System      bool
	Perms       []string
}{
	{"Administrator", "Full access to all settings, users, financial and inventory actions", true, []string{"*"}},
	{"Manager", "Jobs, customers, invoices, payments, inventory, purchases and reports", false, []string{
		"dashboard.view", "customers.view", "customers.edit",
		"jobs.view_all", "jobs.create", "jobs.edit", "jobs.notes", "jobs.status", "jobs.charges", "jobs.parts", "jobs.price_override",
		"invoices.view", "invoices.create", "invoices.void", "payments.record", "payments.void",
		"parts.view", "parts.edit", "stock.adjust", "suppliers.view", "suppliers.edit",
		"purchases.view", "purchases.edit", "purchases.receive", "reports.view", "reports.profit",
	}},
	{"Front desk", "Customers, vehicles, jobs, invoices and payments", false, []string{
		"dashboard.view", "customers.view", "customers.edit",
		"jobs.view_all", "jobs.create", "jobs.edit", "jobs.notes", "jobs.status", "jobs.charges", "jobs.parts",
		"invoices.view", "invoices.create", "payments.record", "parts.view",
	}},
	{"Mechanic", "Assigned jobs, job notes, charges and parts", false, []string{
		"dashboard.view", "jobs.view_assigned", "jobs.notes", "jobs.status", "jobs.charges", "jobs.parts", "parts.view",
	}},
	{"Inventory clerk", "Parts, suppliers, purchases and stock movements", false, []string{
		"dashboard.view", "parts.view", "parts.edit", "stock.adjust",
		"suppliers.view", "suppliers.edit", "purchases.view", "purchases.edit", "purchases.receive", "reports.view",
	}},
}

// Actor identifies who performs an action (for permission checks and audit).
type Actor struct {
	ID       int64
	Username string
	FullName string
	IP       string
	Perms    map[string]bool
}

func (a Actor) Can(perm string) bool { return a.Perms[perm] }
