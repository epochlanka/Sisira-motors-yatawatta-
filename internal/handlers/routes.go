package handlers

import (
	"io/fs"
	"net/http"

	mw "garagems/internal/middleware"
	"garagems/web"
)

func (a *App) Routes() http.Handler {
	mux := http.NewServeMux()
	R := mw.Require

	static, _ := fs.Sub(web.FS, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", cacheStatic(http.FileServerFS(static))))

	// Public
	mux.HandleFunc("GET /login", a.loginForm)
	mux.HandleFunc("POST /login", a.loginSubmit)

	// Authenticated
	auth := http.NewServeMux()
	auth.HandleFunc("POST /logout", a.logout)
	auth.HandleFunc("GET /account/password", a.passwordForm)
	auth.HandleFunc("POST /account/password", a.passwordSubmit)
	auth.HandleFunc("GET /{$}", a.dashboard)

	// Customers & vehicles
	auth.HandleFunc("GET /customers", R(a.customersList, "customers.view"))
	auth.HandleFunc("GET /customers/new", R(a.customerNew, "customers.edit"))
	auth.HandleFunc("POST /customers", R(a.customerCreate, "customers.edit"))
	auth.HandleFunc("GET /customers/{id}", R(a.customerView, "customers.view"))
	auth.HandleFunc("GET /customers/{id}/edit", R(a.customerEdit, "customers.edit"))
	auth.HandleFunc("POST /customers/{id}", R(a.customerUpdate, "customers.edit"))
	auth.HandleFunc("POST /customers/{id}/archive", R(a.customerArchive, "customers.edit"))
	auth.HandleFunc("GET /customers/{id}/vehicles/new", R(a.vehicleNew, "customers.edit"))
	auth.HandleFunc("POST /customers/{id}/vehicles", R(a.vehicleCreate, "customers.edit"))
	auth.HandleFunc("GET /vehicles", R(a.vehiclesList, "customers.view"))
	auth.HandleFunc("GET /vehicles/{id}", R(a.vehicleView, "customers.view"))
	auth.HandleFunc("GET /vehicles/{id}/edit", R(a.vehicleEdit, "customers.edit"))
	auth.HandleFunc("POST /vehicles/{id}", R(a.vehicleUpdate, "customers.edit"))
	auth.HandleFunc("POST /vehicles/{id}/archive", R(a.vehicleArchive, "customers.edit"))
	auth.HandleFunc("POST /vehicles/{id}/service", R(a.vehicleSetService, "customers.edit", "jobs.edit"))
	auth.HandleFunc("GET /vehicles/service-due", R(a.vehiclesServiceDue, "customers.view"))

	// Appointments
	auth.HandleFunc("GET /appointments", R(a.appointmentsList, "appointments.view"))
	auth.HandleFunc("GET /appointments/new", R(a.appointmentNew, "appointments.edit"))
	auth.HandleFunc("POST /appointments", R(a.appointmentCreate, "appointments.edit"))
	auth.HandleFunc("GET /appointments/{id}", R(a.appointmentView, "appointments.view"))
	auth.HandleFunc("GET /appointments/{id}/edit", R(a.appointmentEdit, "appointments.edit"))
	auth.HandleFunc("POST /appointments/{id}", R(a.appointmentUpdate, "appointments.edit"))
	auth.HandleFunc("POST /appointments/{id}/close", R(a.appointmentClose, "appointments.edit"))
	auth.HandleFunc("POST /appointments/{id}/reinstate", R(a.appointmentReinstate, "appointments.edit"))
	auth.HandleFunc("POST /appointments/{id}/check-in", R(a.appointmentCheckIn, "appointments.edit"))

	// Jobs
	auth.HandleFunc("GET /jobs", R(a.jobsList, "jobs.view_all", "jobs.view_assigned"))
	auth.HandleFunc("GET /jobs/new", R(a.jobNew, "jobs.create"))
	auth.HandleFunc("POST /jobs", R(a.jobCreate, "jobs.create"))
	auth.HandleFunc("GET /jobs/{id}", R(a.jobView, "jobs.view_all", "jobs.view_assigned"))
	auth.HandleFunc("GET /jobs/{id}/edit", R(a.jobEdit, "jobs.edit", "jobs.notes"))
	auth.HandleFunc("POST /jobs/{id}", R(a.jobUpdate, "jobs.edit", "jobs.notes"))
	auth.HandleFunc("POST /jobs/{id}/status", R(a.jobStatus, "jobs.status"))
	auth.HandleFunc("POST /jobs/{id}/charges", R(a.jobChargeAdd, "jobs.charges"))
	auth.HandleFunc("POST /jobs/{id}/charges/{cid}", R(a.jobChargeUpdate, "jobs.charges"))
	auth.HandleFunc("POST /jobs/{id}/charges/{cid}/delete", R(a.jobChargeDelete, "jobs.charges"))
	auth.HandleFunc("POST /jobs/{id}/parts", R(a.jobPartAdd, "jobs.parts"))
	auth.HandleFunc("POST /jobs/{id}/parts/{pid}", R(a.jobPartUpdate, "jobs.parts"))
	auth.HandleFunc("POST /jobs/{id}/parts/{pid}/delete", R(a.jobPartDelete, "jobs.parts"))
	auth.HandleFunc("POST /jobs/{id}/invoice", R(a.invoiceCreate, "invoices.create"))

	// Invoices & payments
	auth.HandleFunc("GET /invoices", R(a.invoicesList, "invoices.view"))
	auth.HandleFunc("GET /invoices/{id}", R(a.invoiceView, "invoices.view"))
	auth.HandleFunc("GET /invoices/{id}/print", R(a.invoicePrint, "invoices.view"))
	auth.HandleFunc("POST /invoices/{id}/void", R(a.invoiceVoid, "invoices.void"))
	auth.HandleFunc("POST /invoices/{id}/payments", R(a.paymentCreate, "payments.record"))
	auth.HandleFunc("GET /payments", R(a.paymentsList, "invoices.view"))
	auth.HandleFunc("GET /payments/{id}/receipt", R(a.receiptPrint, "invoices.view"))
	auth.HandleFunc("POST /payments/{id}/void", R(a.paymentVoid, "payments.void"))

	// Parts & stock
	auth.HandleFunc("GET /parts", R(a.partsList, "parts.view"))
	auth.HandleFunc("GET /parts/new", R(a.partNew, "parts.edit"))
	auth.HandleFunc("POST /parts", R(a.partCreate, "parts.edit"))
	auth.HandleFunc("GET /parts/{id}", R(a.partView, "parts.view"))
	auth.HandleFunc("GET /parts/{id}/edit", R(a.partEdit, "parts.edit"))
	auth.HandleFunc("POST /parts/{id}", R(a.partUpdate, "parts.edit"))
	auth.HandleFunc("POST /parts/{id}/active", R(a.partSetActive, "parts.edit"))
	auth.HandleFunc("POST /parts/{id}/adjust", R(a.partAdjust, "stock.adjust"))
	auth.HandleFunc("GET /stock-movements", R(a.movementsList, "parts.view"))

	// Suppliers & purchases
	auth.HandleFunc("GET /suppliers", R(a.suppliersList, "suppliers.view"))
	auth.HandleFunc("GET /suppliers/new", R(a.supplierNew, "suppliers.edit"))
	auth.HandleFunc("POST /suppliers", R(a.supplierCreate, "suppliers.edit"))
	auth.HandleFunc("GET /suppliers/{id}", R(a.supplierView, "suppliers.view"))
	auth.HandleFunc("GET /suppliers/{id}/edit", R(a.supplierEdit, "suppliers.edit"))
	auth.HandleFunc("POST /suppliers/{id}", R(a.supplierUpdate, "suppliers.edit"))
	auth.HandleFunc("POST /suppliers/{id}/active", R(a.supplierSetActive, "suppliers.edit"))
	auth.HandleFunc("GET /purchases", R(a.purchasesList, "purchases.view"))
	auth.HandleFunc("GET /purchases/new", R(a.purchaseNew, "purchases.edit"))
	auth.HandleFunc("POST /purchases", R(a.purchaseCreate, "purchases.edit"))
	auth.HandleFunc("GET /purchases/{id}", R(a.purchaseView, "purchases.view"))
	auth.HandleFunc("POST /purchases/{id}", R(a.purchaseUpdate, "purchases.edit"))
	auth.HandleFunc("POST /purchases/{id}/items", R(a.purchaseItemAdd, "purchases.edit"))
	auth.HandleFunc("POST /purchases/{id}/items/{iid}/delete", R(a.purchaseItemDelete, "purchases.edit"))
	auth.HandleFunc("POST /purchases/{id}/receive", R(a.purchaseReceive, "purchases.receive"))
	auth.HandleFunc("POST /purchases/{id}/cancel", R(a.purchaseCancel, "purchases.receive"))

	// Expenses
	auth.HandleFunc("GET /expenses", R(a.expensesList, "expenses.view"))
	auth.HandleFunc("GET /expenses/new", R(a.expenseNew, "expenses.edit"))
	auth.HandleFunc("POST /expenses", R(a.expenseCreate, "expenses.edit"))
	auth.HandleFunc("GET /expenses/{id}", R(a.expenseView, "expenses.view"))
	auth.HandleFunc("POST /expenses/{id}/void", R(a.expenseVoid, "expenses.void"))

	// Staff & timesheets
	auth.HandleFunc("GET /staff", R(a.staffList, "staff.view"))
	auth.HandleFunc("GET /staff/timesheets", R(a.timesheetsList, "staff.view", "timesheet.self"))
	auth.HandleFunc("GET /staff/timesheets/new", R(a.timesheetNew, "staff.timesheets"))
	auth.HandleFunc("POST /staff/timesheets", R(a.timesheetCreate, "staff.timesheets"))
	auth.HandleFunc("GET /staff/timesheets/{id}", R(a.timesheetEdit, "staff.timesheets"))
	auth.HandleFunc("POST /staff/timesheets/{id}", R(a.timesheetUpdate, "staff.timesheets"))
	auth.HandleFunc("POST /timesheet/clock-in", R(a.clockIn, "timesheet.self"))
	auth.HandleFunc("POST /timesheet/clock-out", R(a.clockOut, "timesheet.self"))

	// Reports
	auth.HandleFunc("GET /reports", R(a.reportsIndex, "reports.view"))
	auth.HandleFunc("GET /reports/jobs", R(a.reportJobs, "reports.view"))
	auth.HandleFunc("GET /reports/sales", R(a.reportSales, "reports.view"))
	auth.HandleFunc("GET /reports/outstanding", R(a.reportOutstanding, "reports.view"))
	auth.HandleFunc("GET /reports/parts-sold", R(a.reportPartsSold, "reports.view"))
	auth.HandleFunc("GET /reports/stock", R(a.reportStock, "reports.view"))
	auth.HandleFunc("GET /reports/purchases", R(a.reportPurchases, "reports.view"))
	auth.HandleFunc("GET /reports/movements", R(a.reportMovements, "reports.view"))
	auth.HandleFunc("GET /reports/services", R(a.reportServices, "reports.view"))
	auth.HandleFunc("GET /reports/staff", R(a.reportStaff, "reports.view"))
	auth.HandleFunc("GET /reports/expenses", R(a.reportExpenses, "reports.view"))
	auth.HandleFunc("GET /reports/profit", R(a.reportProfit, "reports.view"))

	// Settings & administration
	auth.HandleFunc("GET /settings", R(a.settingsForm, "settings.manage"))
	auth.HandleFunc("POST /settings", R(a.settingsSave, "settings.manage"))
	auth.HandleFunc("GET /settings/charges", R(a.chargesList, "settings.manage"))
	auth.HandleFunc("POST /settings/charges", R(a.chargeCreate, "settings.manage"))
	auth.HandleFunc("POST /settings/charges/{id}", R(a.chargeUpdate, "settings.manage"))
	auth.HandleFunc("POST /settings/charges/{id}/move", R(a.chargeMove, "settings.manage"))
	auth.HandleFunc("GET /settings/users", R(a.usersList, "users.manage"))
	auth.HandleFunc("GET /settings/users/new", R(a.userNew, "users.manage"))
	auth.HandleFunc("POST /settings/users", R(a.userCreate, "users.manage"))
	auth.HandleFunc("GET /settings/users/{id}", R(a.userEdit, "users.manage"))
	auth.HandleFunc("POST /settings/users/{id}", R(a.userUpdate, "users.manage"))
	auth.HandleFunc("POST /settings/users/{id}/password", R(a.userResetPassword, "users.manage"))
	auth.HandleFunc("GET /settings/roles", R(a.rolesForm, "users.manage"))
	auth.HandleFunc("POST /settings/roles", R(a.rolesSave, "users.manage"))
	auth.HandleFunc("POST /settings/roles/new", R(a.roleCreate, "users.manage"))
	auth.HandleFunc("GET /settings/audit", R(a.auditList, "audit.view"))
	auth.HandleFunc("GET /settings/backup", R(a.backupDownload, "settings.manage"))

	authed := a.Auth.RequireLogin(mw.NoStore(auth))
	mux.Handle("/", authed)

	var h http.Handler = mux
	h = a.Auth.CSRF(h)
	h = a.Auth.Load(h)
	h = mw.SecurityHeaders(h)
	h = mw.Logging(h)
	h = mw.Recover(h)
	return h
}

func cacheStatic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=3600")
		next.ServeHTTP(w, r)
	})
}
