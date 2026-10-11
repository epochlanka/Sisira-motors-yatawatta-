package handlers

import (
	"net/http"
	"strconv"

	mw "garagems/internal/middleware"
	"garagems/internal/models"
	"garagems/internal/repository"
	"garagems/internal/service"
)

var expenseSorts = map[string]string{
	"date":     "e.expense_date, e.id",
	"category": "e.category",
	"amount":   "e.amount",
	"paid_to":  "e.paid_to COLLATE NOCASE",
}

func expenseURL(id int64) string { return "/expenses/" + strconv.FormatInt(id, 10) }

func expenseFilter(r *http.Request, l *List) repository.ExpenseFilter {
	f := repository.ExpenseFilter{Q: l.Get("q"), Category: l.Get("category"), Method: l.Get("method"),
		From: dateParam(r, "from", ""), To: dateParam(r, "to", ""), Status: l.Get("status")}
	if !models.ValidExpenseCategory(f.Category) {
		f.Category = ""
	}
	return f
}

func (a *App) expensesList(w http.ResponseWriter, r *http.Request) {
	l := newList(r, expenseSorts, "date", "desc")
	f := expenseFilter(r, l)
	if !l.Values.Has("from") && !l.Values.Has("to") {
		f.From, f.To = monthStart(), models.Today()
		l.Values.Set("from", f.From)
		l.Values.Set("to", f.To)
	}
	list, total, sum, err := repository.ListExpenses(r.Context(), a.DB, f, l.Opts())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	l.Total = total
	p := a.page(r, "Expenses", "expenses")
	p.D["list"], p.D["expenses"], p.D["sum"], p.D["categories"], p.D["methods"] = l, list, sum, models.ExpenseCategories, models.PaymentMethods
	a.render(w, r, "expenses_list", p)
}

func (a *App) expenseFormPage(r *http.Request, e models.Expense) *Page {
	p := a.page(r, "Record expense", "expenses")
	p.D["e"], p.D["categories"], p.D["methods"] = e, models.ExpenseCategories, models.PaymentMethods
	return p
}

func (a *App) expenseNew(w http.ResponseWriter, r *http.Request) {
	a.render(w, r, "expense_form", a.expenseFormPage(r, models.Expense{ExpenseDate: models.Today(), Method: "Cash"}))
}

func (a *App) expenseCreate(w http.ResponseWriter, r *http.Request) {
	v := V{}
	e := models.Expense{ExpenseDate: val(r, "expense_date"), Category: val(r, "category"), Description: val(r, "description"),
		Method: val(r, "method"), PaidTo: val(r, "paid_to"), Reference: val(r, "reference"), Notes: val(r, "notes")}
	v.Date("expense_date", e.ExpenseDate, "Date")
	if e.ExpenseDate > models.Today() {
		v.set("expense_date", "The date cannot be in the future.")
	}
	if !models.ValidExpenseCategory(e.Category) {
		v.set("category", "Choose a category.")
	}
	v.Required("description", e.Description, "Description")
	v.MaxLen("description", e.Description, 200, "Description")
	e.Amount = v.Money(r, "amount", "Amount", true)
	if _, bad := v["amount"]; !bad && e.Amount <= 0 {
		v.set("amount", "Amount must be more than zero.")
	}
	valid := false
	for _, m := range models.PaymentMethods {
		valid = valid || m == e.Method
	}
	if !valid {
		v.set("method", "Choose how it was paid.")
	}
	v.MaxLen("paid_to", e.PaidTo, 120, "Paid to")
	v.MaxLen("reference", e.Reference, 80, "Reference")
	v.MaxLen("notes", e.Notes, 1000, "Notes")
	if len(v) == 0 {
		id, err := a.Svc.CreateExpense(r.Context(), mw.ActorFrom(r), e)
		if err == nil {
			to := expenseURL(id)
			if checked(r, "another") {
				to = "/expenses/new"
			}
			a.redirectFlash(w, r, to, "success", "Expense of "+e.Amount.String()+" recorded.")
			return
		}
		msg, ok := service.IsUserError(err)
		if !ok {
			a.serverError(w, r, err)
			return
		}
		v["_"] = msg
	}
	p := a.expenseFormPage(r, e)
	p.Errors = v
	if m := v["_"]; m != "" {
		p.FlashKind, p.FlashMsg = "error", m
	}
	a.render(w, r, "expense_form", p, http.StatusUnprocessableEntity)
}

func (a *App) expenseView(w http.ResponseWriter, r *http.Request) {
	e, err := repository.GetExpense(r.Context(), a.DB, pathID(r, "id"))
	if err != nil {
		a.handleErr(w, r, err)
		return
	}
	p := a.page(r, "Expense", "expenses")
	p.D["e"] = e
	a.render(w, r, "expense_view", p)
}

func (a *App) expenseVoid(w http.ResponseWriter, r *http.Request) {
	id := pathID(r, "id")
	err := a.Svc.VoidExpense(r.Context(), mw.ActorFrom(r), id, val(r, "reason"))
	a.done(w, r, expenseURL(id), err, "Expense voided. It is kept in the history but no longer counted.")
}
