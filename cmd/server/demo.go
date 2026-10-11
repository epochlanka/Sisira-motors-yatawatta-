package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"garagems/internal/models"
	"garagems/internal/repository"
	"garagems/internal/service"
)

// seedDemo loads optional sample data. It only runs on a database with no customers,
// parts or suppliers, and requires an administrator account to attribute records to.
func seedDemo(db *sql.DB) error {
	ctx := context.Background()
	var n int
	db.QueryRow("SELECT (SELECT COUNT(*) FROM customers) + (SELECT COUNT(*) FROM parts) + (SELECT COUNT(*) FROM suppliers)").Scan(&n)
	if n > 0 {
		return errors.New("seed-demo only runs on an empty database (customers, parts and suppliers must be empty)")
	}
	var adminID int64
	var username string
	err := db.QueryRow("SELECT u.id, u.username FROM users u JOIN roles r ON r.id = u.role_id WHERE r.is_system = 1 AND u.active = 1 ORDER BY u.id LIMIT 1").Scan(&adminID, &username)
	if err != nil {
		return errors.New("create an administrator first (create-admin)")
	}
	perms := map[string]bool{}
	for _, p := range models.AllPermissions {
		perms[p.Code] = true
	}
	a := models.Actor{ID: adminID, Username: username, IP: "seed-demo", Perms: perms}
	svc := service.New(db)
	set, err := repository.GetSettings(ctx, db)
	if err != nil {
		return err
	}
	models.SetCurrency(models.CurrencyFormat{Symbol: set.CurrencySymbol, Decimals: set.CurrencyDecimals})
	m := func(s string) models.Money { v, _ := models.ParseMoney(s); return v }
	q := func(s string) models.Qty { v, _ := models.ParseQty(s); return v }

	for _, s := range []models.Supplier{
		{Name: "Demo Auto Parts Wholesale", ContactPerson: "Sales desk", Phone: "011 000 0001"},
		{Name: "Demo Lubricants Distributor", ContactPerson: "Orders", Phone: "011 000 0002"},
	} {
		if _, err := repository.CreateSupplier(ctx, db, s); err != nil {
			return err
		}
	}
	parts := []struct {
		p   models.Part
		qty string
	}{
		{models.Part{Code: "OF-001", Name: "Oil filter (small car)", Category: "Filters", Unit: "pcs", SalePrice: m("1850"), PurchaseCost: m("1200"), ReorderLevel: q("5"), Location: "A1"}, "12"},
		{models.Part{Code: "AF-010", Name: "Air filter", Category: "Filters", Unit: "pcs", SalePrice: m("2400"), PurchaseCost: m("1600"), ReorderLevel: q("4"), Location: "A2"}, "6"},
		{models.Part{Code: "EO-5W30", Name: "Engine oil 5W-30", Category: "Lubricants", Unit: "L", SalePrice: m("3200"), PurchaseCost: m("2500"), ReorderLevel: q("10"), Location: "B1"}, "40"},
		{models.Part{Code: "BP-F100", Name: "Front brake pads (set)", Category: "Brakes", Unit: "set", SalePrice: m("6500"), PurchaseCost: m("4800"), ReorderLevel: q("2"), Location: "C3"}, "3"},
		{models.Part{Code: "SP-NGK7", Name: "Spark plug", Category: "Ignition", Unit: "pcs", SalePrice: m("950"), PurchaseCost: m("600"), ReorderLevel: q("8"), Location: "A4"}, "4"},
		{models.Part{Code: "GR-500", Name: "Grease 500g", Category: "Lubricants", Unit: "pcs", SalePrice: m("1100"), PurchaseCost: m("750"), ReorderLevel: q("3"), Location: "B2"}, "10"},
	}
	partIDs := map[string]int64{}
	for _, x := range parts {
		id, err := svc.CreatePart(ctx, a, x.p, q(x.qty))
		if err != nil {
			return fmt.Errorf("part %s: %w", x.p.Code, err)
		}
		partIDs[x.p.Code] = id
	}
	custs := []struct {
		c models.Customer
		v models.Vehicle
	}{
		{models.Customer{Name: "Demo Customer One", Phone: "077 000 0001", Address: "Demo Road 1"}, models.Vehicle{Registration: "DEMO-1001", Make: "Toyota", Model: "Corolla", Year: sql.NullInt64{Int64: 2012, Valid: true}}},
		{models.Customer{Name: "Demo Customer Two", Phone: "077 000 0002"}, models.Vehicle{Registration: "DEMO-2002", Make: "Honda", Model: "Fit", Year: sql.NullInt64{Int64: 2015, Valid: true}}},
	}
	var firstC, firstV int64
	for i, x := range custs {
		cid, err := repository.CreateCustomer(ctx, db, x.c)
		if err != nil {
			return err
		}
		x.v.CustomerID = cid
		vid, err := repository.CreateVehicle(ctx, db, x.v)
		if err != nil {
			return err
		}
		if i == 0 {
			firstC, firstV = cid, vid
		}
	}
	jobID, err := svc.CreateJob(ctx, a, models.Job{CustomerID: firstC, VehicleID: sql.NullInt64{Int64: firstV, Valid: true}, JobDate: models.Today(),
		Odometer: sql.NullInt64{Int64: 85400, Valid: true}, ReportedIssue: "Full service and brake noise (demo job)"}, set.JobPrefix)
	if err != nil {
		return err
	}
	cats, err := repository.ListChargeCategories(ctx, db, true)
	if err != nil || len(cats) == 0 {
		return errors.New("no charge categories")
	}
	price := m("2500")
	if err := svc.AddJobCharge(ctx, a, jobID, service.ChargeInput{CategoryID: cats[0].ID, Quantity: q("1.5"), UnitPrice: &price}); err != nil {
		return err
	}
	if err := svc.AddJobPart(ctx, a, jobID, service.PartInput{PartID: partIDs["OF-001"], Quantity: q("1")}); err != nil {
		return err
	}
	if err := svc.AddJobPart(ctx, a, jobID, service.PartInput{PartID: partIDs["EO-5W30"], Quantity: q("3.5")}); err != nil {
		return err
	}
	if err := svc.ChangeJobStatus(ctx, a, jobID, models.JobInProgress); err != nil {
		return err
	}
	fmt.Println("Demo data loaded: 2 suppliers, 6 parts, 2 customers with vehicles, 1 job in progress.")
	return nil
}
