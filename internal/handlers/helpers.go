package handlers

import (
	"database/sql"
	"net/http"
	"net/mail"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"garagems/internal/models"
	"garagems/internal/repository"
)

func pathID(r *http.Request, name string) int64 {
	id, err := strconv.ParseInt(r.PathValue(name), 10, 64)
	if err != nil || id <= 0 {
		return 0
	}
	return id
}

func queryID(r *http.Request, name string) int64 {
	id, _ := strconv.ParseInt(r.URL.Query().Get(name), 10, 64)
	if id < 0 {
		return 0
	}
	return id
}

func formID(r *http.Request, name string) int64 {
	id, _ := strconv.ParseInt(strings.TrimSpace(r.PostFormValue(name)), 10, 64)
	if id < 0 {
		return 0
	}
	return id
}

func val(r *http.Request, name string) string { return strings.TrimSpace(r.PostFormValue(name)) }

func checked(r *http.Request, name string) bool {
	v := r.PostFormValue(name)
	return v == "1" || v == "on" || v == "true"
}

// ---------- validation ----------

// V collects field errors keyed by form field name.
type V map[string]string

func (v V) Required(field, value, label string) {
	if strings.TrimSpace(value) == "" {
		v.set(field, label+" is required.")
	}
}

func (v V) MaxLen(field, value string, n int, label string) {
	if utf8.RuneCountInString(value) > n {
		v.set(field, label+" must be at most "+strconv.Itoa(n)+" characters.")
	}
}

func (v V) Email(field, value string) {
	if value == "" {
		return
	}
	if a, err := mail.ParseAddress(value); err != nil || a.Address != value {
		v.set(field, "Enter a valid email address.")
	}
}

func (v V) Date(field, value, label string) {
	if !models.ValidDate(value) {
		v.set(field, label+" must be a valid date (YYYY-MM-DD).")
	}
}

func (v V) set(field, msg string) {
	if _, ok := v[field]; !ok {
		v[field] = msg
	}
}

func (v V) Money(r *http.Request, field, label string, required bool) models.Money {
	s := val(r, field)
	if s == "" {
		if required {
			v.set(field, label+" is required.")
		}
		return 0
	}
	m, err := models.ParseMoney(s)
	if err != nil {
		v.set(field, label+": "+err.Error()+".")
		return 0
	}
	if m < 0 {
		v.set(field, label+" cannot be negative.")
	}
	return m
}

// OptMoney returns nil when the field is blank.
func (v V) OptMoney(r *http.Request, field, label string) *models.Money {
	if val(r, field) == "" {
		return nil
	}
	m := v.Money(r, field, label, true)
	return &m
}

func (v V) Qty(r *http.Request, field, label string, required, allowNegative bool) models.Qty {
	s := val(r, field)
	if s == "" {
		if required {
			v.set(field, label+" is required.")
		}
		return 0
	}
	q, err := models.ParseQty(s)
	if err != nil {
		v.set(field, label+": "+err.Error()+".")
		return 0
	}
	if q < 0 && !allowNegative {
		v.set(field, label+" cannot be negative.")
	}
	return q
}

func (v V) OptInt(r *http.Request, field, label string, min, max int64) sql.NullInt64 {
	s := strings.ReplaceAll(val(r, field), ",", "")
	if s == "" {
		return sql.NullInt64{}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < min || n > max {
		v.set(field, label+" must be a whole number between "+strconv.FormatInt(min, 10)+" and "+strconv.FormatInt(max, 10)+".")
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: n, Valid: true}
}

// ---------- lists ----------

// List handles search/sort/pagination query parameters with a whitelist of sort columns.
type List struct {
	Path    string
	Values  url.Values
	Sort    string
	Dir     string
	Page    int
	PerPage int
	Total   int
	sorts   map[string]string
}

func newList(r *http.Request, sorts map[string]string, defSort, defDir string) *List {
	q := r.URL.Query()
	l := &List{Path: r.URL.Path, Values: q, Sort: q.Get("sort"), Dir: strings.ToLower(q.Get("dir")), PerPage: 25, sorts: sorts}
	if _, ok := sorts[l.Sort]; !ok {
		l.Sort = defSort
	}
	if l.Dir != "asc" && l.Dir != "desc" {
		if q.Get("sort") == l.Sort && l.Sort != defSort {
			l.Dir = "asc"
		} else {
			l.Dir = defDir
		}
	}
	l.Page, _ = strconv.Atoi(q.Get("page"))
	if l.Page < 1 {
		l.Page = 1
	}
	if pp, err := strconv.Atoi(q.Get("per")); err == nil && pp >= 10 && pp <= 200 {
		l.PerPage = pp
	}
	return l
}

func (l *List) Opts() repository.ListOpts {
	order := l.sorts[l.Sort]
	dir := " ASC"
	if l.Dir == "desc" {
		dir = " DESC"
	}
	// Apply direction to each comma-separated column of the whitelisted expression.
	parts := strings.Split(order, ",")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i]) + dir
	}
	return repository.ListOpts{OrderBy: strings.Join(parts, ", "), Limit: l.PerPage, Offset: (l.Page - 1) * l.PerPage}
}

func (l *List) Get(name string) string { return l.Values.Get(name) }

func (l *List) url(mod func(url.Values)) string {
	v := url.Values{}
	for k, vs := range l.Values {
		v[k] = append([]string(nil), vs...)
	}
	mod(v)
	return l.Path + "?" + v.Encode()
}

func (l *List) SortURL(key string) string {
	return l.url(func(v url.Values) {
		dir := "asc"
		if l.Sort == key && l.Dir == "asc" {
			dir = "desc"
		}
		v.Set("sort", key)
		v.Set("dir", dir)
		v.Del("page")
	})
}

func (l *List) SortMark(key string) string {
	if l.Sort != key {
		return ""
	}
	if l.Dir == "asc" {
		return "▲"
	}
	return "▼"
}

func (l *List) AriaSort(key string) string {
	if l.Sort != key {
		return "none"
	}
	if l.Dir == "asc" {
		return "ascending"
	}
	return "descending"
}

func (l *List) Pages() int {
	if l.Total == 0 {
		return 1
	}
	return (l.Total + l.PerPage - 1) / l.PerPage
}

func (l *List) HasPrev() bool { return l.Page > 1 }
func (l *List) HasNext() bool { return l.Page < l.Pages() }
func (l *List) PrevURL() string {
	return l.url(func(v url.Values) { v.Set("page", strconv.Itoa(l.Page-1)) })
}
func (l *List) NextURL() string {
	return l.url(func(v url.Values) { v.Set("page", strconv.Itoa(l.Page+1)) })
}
func (l *List) From() int {
	if l.Total == 0 {
		return 0
	}
	return (l.Page-1)*l.PerPage + 1
}
func (l *List) To() int {
	t := l.Page * l.PerPage
	if t > l.Total {
		return l.Total
	}
	return t
}

// ---------- dates ----------

// dateParam returns a valid YYYY-MM-DD query value or the fallback.
func dateParam(r *http.Request, name, fallback string) string {
	s := strings.TrimSpace(r.URL.Query().Get(name))
	if models.ValidDate(s) {
		return s
	}
	return fallback
}

// utcBounds converts a local date range (inclusive) into UTC timestamp bounds [from, to).
func utcBounds(from, to string) (string, string) {
	loc := models.Location()
	var f, t string
	if d, err := time.ParseInLocation(models.DateLayout, from, loc); err == nil {
		f = d.UTC().Format(models.TimeLayout)
	}
	if d, err := time.ParseInLocation(models.DateLayout, to, loc); err == nil {
		t = d.AddDate(0, 0, 1).UTC().Format(models.TimeLayout)
	}
	return f, t
}

func monthStart() string {
	now := time.Now().In(models.Location())
	return time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location()).Format(models.DateLayout)
}
