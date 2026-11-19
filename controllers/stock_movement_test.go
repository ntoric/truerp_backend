package controllers

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"truerp/models"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func callMovementHandler(t *testing.T, h gin.HandlerFunc, userID uuid.UUID, query string, out interface{}) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/inventory/movements?"+query, nil)
	c.Set("user_id", userID)
	h(c)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
		t.Fatalf("decode: %v", err)
	}
}

func runStockMovements(t *testing.T, userID uuid.UUID, query string) StockMovementResponse {
	t.Helper()
	var resp StockMovementResponse
	callMovementHandler(t, GetStockMovements, userID, query, &resp)
	return resp
}

func runStockMovementSummary(t *testing.T, userID uuid.UUID, query string) StockMovementSummaryResponse {
	t.Helper()
	var resp StockMovementSummaryResponse
	callMovementHandler(t, GetStockMovementSummary, userID, query, &resp)
	return resp
}

func approxEq(a, b float64) bool { return math.Abs(a-b) < 1e-6 }

// Purchases re-rate the weighted average; sales go out at average cost with
// profit against the invoice's discounted price; a sales return comes back
// at average cost and reverses profit. Reservation noise is ignored and the
// opening position reflects movements before the range.
func TestStockMovementsWeightedAverageReplay(t *testing.T) {
	db := setupProfitLossTestDB(t)
	if err := db.AutoMigrate(&models.Warehouse{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	userID := uuid.New()
	productID := uuid.New()
	outletID := uuid.New()
	invoiceID := uuid.New()
	billID := uuid.New()

	db.Create(&models.Product{ID: productID, UserID: userID, Name: "Widget", SKU: "W1", Unit: "pcs", PurchasePrice: 10})
	db.Create(&models.Warehouse{ID: outletID, UserID: userID, Name: "Main", Code: "MAIN"})
	db.Create(&models.PurchaseBill{ID: billID, UserID: userID, BillNumber: "PB-7", PartyID: uuid.New(), BillDate: day(2026, 10, 2)})
	db.Create(&models.Invoice{ID: invoiceID, UserID: userID, InvoiceNumber: "INV-9", PartyID: uuid.New(), Date: day(2026, 10, 3)})
	pid := productID
	db.Create(&models.InvoiceItem{ID: uuid.New(), InvoiceID: invoiceID, ProductID: &pid, Quantity: 5, UnitPrice: 40, Discount: 10})

	entries := []models.StockEntry{
		// Before range: opening 10 @ 10.
		{EntryType: "opening", Quantity: 10, CostPrice: 10, EntryDate: day(2026, 9, 20)},
		// In range.
		{EntryType: "purchase", Quantity: 10, CostPrice: 20, EntryDate: day(2026, 10, 2), ReferenceID: billID, ReferenceType: "purchase_bill"},
		{EntryType: "reservation", Quantity: -3, EntryDate: day(2026, 10, 2)},
		{EntryType: "sale", Quantity: -5, CostPrice: 40, EntryDate: day(2026, 10, 3), ReferenceID: invoiceID, ReferenceType: "invoice"},
		{EntryType: "return", Quantity: 1, CostPrice: 36, EntryDate: day(2026, 10, 4), ReferenceType: "sales_return"},
		// Pending entries never move stock.
		{EntryType: "purchase", Quantity: 100, CostPrice: 1, EntryDate: day(2026, 10, 4), ApprovalStatus: "pending"},
	}
	for i := range entries {
		e := entries[i]
		e.ID = uuid.New()
		e.UserID = userID
		e.ProductID = &pid
		e.OutletID = outletID
		e.ItemName = "Widget"
		if e.ApprovalStatus == "" {
			e.ApprovalStatus = "approved"
		}
		if err := db.Create(&e).Error; err != nil {
			t.Fatalf("entry: %v", err)
		}
	}
	// Live stock matches the ledger (10 + 10 - 5 + 1 = 16) — no baseline.
	db.Create(&models.InventoryStock{ID: uuid.New(), UserID: userID, ProductID: productID, OutletID: outletID, Quantity: 16, AverageCost: 15, CreatedAt: day(2026, 9, 20)})

	resp := runStockMovements(t, userID, "from_date=2026-10-01&to_date=2026-10-31&sort=asc&per_page=0")
	s := runStockMovementSummary(t, userID, "from_date=2026-10-01&to_date=2026-10-31").Summary
	if !approxEq(s.OpeningQty, 10) || !approxEq(s.OpeningValue, 100) {
		t.Fatalf("opening = %v/%v, want 10/100", s.OpeningQty, s.OpeningValue)
	}
	if len(resp.Data) != 3 {
		t.Fatalf("rows = %d, want 3: %+v", len(resp.Data), resp.Data)
	}

	pur, sale, ret := resp.Data[0], resp.Data[1], resp.Data[2]
	if pur.TransactionType != "purchase" || pur.DocumentNo != "PB-7" || !approxEq(pur.Stock, 20) || !approxEq(pur.StockValue, 300) || !approxEq(pur.UnitCost, 20) {
		t.Fatalf("purchase row = %+v", pur)
	}
	// Avg 15; sale price 40 less 10% = 36; profit 5 × 21 = 105.
	if sale.DocumentNo != "INV-9" || !approxEq(sale.UnitCost, 15) || !approxEq(sale.SalePrice, 36) ||
		!approxEq(sale.Total, 180) || !approxEq(sale.Profit, 105) || !approxEq(sale.Stock, 15) || !approxEq(sale.StockValue, 225) {
		t.Fatalf("sale row = %+v", sale)
	}
	if ret.TransactionType != "sales_return" || !approxEq(ret.UnitCost, 15) || !approxEq(ret.Profit, -21) || !approxEq(ret.Stock, 16) || !approxEq(ret.StockValue, 240) {
		t.Fatalf("return row = %+v", ret)
	}
	if !approxEq(s.ClosingQty, 16) || !approxEq(s.ClosingValue, 240) || !approxEq(s.InQty, 11) || !approxEq(s.OutQty, 5) {
		t.Fatalf("summary = %+v", s)
	}
	if !approxEq(s.SalesAmount, 144) || !approxEq(s.COGS, 60) || !approxEq(s.Profit, 84) || !approxEq(s.PurchaseCost, 200) {
		t.Fatalf("summary money = %+v", s)
	}

	// Type filter narrows rows but the running stock stays correct.
	sales := runStockMovements(t, userID, "from_date=2026-10-01&to_date=2026-10-31&type=sale&per_page=0")
	salesSummary := runStockMovementSummary(t, userID, "from_date=2026-10-01&to_date=2026-10-31&type=sale")
	if len(sales.Data) != 1 || !approxEq(sales.Data[0].Stock, 15) || !salesSummary.OpeningClosingFiltered || salesSummary.Summary.Count != 1 {
		t.Fatalf("filtered = %+v", sales)
	}

	// Product-level filters: item name, item code/SKU, unit (case-insensitive).
	for q, want := range map[string]int{
		"item_name=widg": 3, "item_name=gadget": 0,
		"item_code=w1": 3, "item_code=zz": 0,
		"unit=PCS": 3, "unit=kg": 0,
	} {
		got := runStockMovements(t, userID, "from_date=2026-10-01&to_date=2026-10-31&per_page=0&"+q)
		if len(got.Data) != want {
			t.Fatalf("%s: rows = %d, want %d", q, len(got.Data), want)
		}
	}
}

// Stock with no ledger entry surfaces as an untracked inflow so balances end
// at the real on-hand quantity.
func TestStockMovementsUntrackedBaseline(t *testing.T) {
	db := setupProfitLossTestDB(t)
	if err := db.AutoMigrate(&models.Warehouse{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	userID := uuid.New()
	productID := uuid.New()
	outletID := uuid.New()
	pid := productID
	db.Create(&models.Product{ID: productID, UserID: userID, Name: "Imported", PurchasePrice: 5})
	db.Create(&models.InventoryStock{ID: uuid.New(), UserID: userID, ProductID: productID, OutletID: outletID, Quantity: 46, AverageCost: 20, CreatedAt: day(2026, 10, 1)})
	db.Create(&models.StockEntry{ID: uuid.New(), UserID: userID, ProductID: &pid, OutletID: outletID, ItemName: "Imported",
		EntryType: "sale", Quantity: -4, CostPrice: 30, ApprovalStatus: "approved", EntryDate: day(2026, 10, 5)})

	resp := runStockMovements(t, userID, "from_date=2026-10-01&to_date=2026-10-31&sort=asc&per_page=0")
	if len(resp.Data) != 2 || resp.Data[0].TransactionType != "untracked" || !approxEq(resp.Data[0].QtyIn, 50) {
		t.Fatalf("rows = %+v", resp.Data)
	}
	sum := runStockMovementSummary(t, userID, "from_date=2026-10-01&to_date=2026-10-31").Summary
	if !approxEq(sum.ClosingQty, 46) || !approxEq(sum.ClosingValue, 920) || !approxEq(resp.Data[1].Profit, 40) {
		t.Fatalf("summary = %+v, sale = %+v", sum, resp.Data[1])
	}
}

// Server-side pages, in either sort order and at any page size, must
// concatenate to exactly the full listing — including untracked baselines
// merged between ledger rows — with identical running positions.
func TestStockMovementsServerPagination(t *testing.T) {
	db := setupProfitLossTestDB(t)
	if err := db.AutoMigrate(&models.Warehouse{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	userID := uuid.New()
	outletID := uuid.New()
	a, b := uuid.New(), uuid.New()
	db.Create(&models.Product{ID: a, UserID: userID, Name: "Alpha", SKU: "A", PurchasePrice: 10})
	db.Create(&models.Product{ID: b, UserID: userID, Name: "Beta", SKU: "B", PurchasePrice: 10})

	add := func(pid uuid.UUID, typ string, qty, cost float64, d int, ref string) {
		p := pid
		e := models.StockEntry{ID: uuid.New(), UserID: userID, ProductID: &p, OutletID: outletID, ItemName: "x",
			EntryType: typ, Quantity: qty, CostPrice: cost, ApprovalStatus: "approved", EntryDate: day(2026, 10, d), ReferenceType: ref}
		if err := db.Create(&e).Error; err != nil {
			t.Fatalf("entry: %v", err)
		}
	}
	add(a, "opening", 20, 10, 1, "")
	add(a, "sale", -3, 25, 2, "")
	add(b, "purchase", 5, 12, 3, "")
	add(a, "purchase", 10, 16, 5, "")
	add(b, "sale", -2, 30, 5, "")
	add(a, "sale", -4, 25, 6, "")
	add(a, "return", -1, 16, 7, "") // purchase return
	add(b, "sale", -1, 30, 8, "")
	// Beta has 30 imported units with no ledger entry, anchored on Oct 5 —
	// the baseline row must slot in before Oct 5's ledger rows.
	db.Create(&models.InventoryStock{ID: uuid.New(), UserID: userID, ProductID: a, OutletID: outletID, Quantity: 22, AverageCost: 14, CreatedAt: day(2026, 10, 1)})
	db.Create(&models.InventoryStock{ID: uuid.New(), UserID: userID, ProductID: b, OutletID: outletID, Quantity: 32, AverageCost: 12, CreatedAt: day(2026, 10, 5)})

	base := "from_date=2026-10-01&to_date=2026-10-31"
	for _, sortDir := range []string{"asc", "desc"} {
		full := runStockMovements(t, userID, base+"&per_page=0&sort="+sortDir)
		if full.Total != 9 || len(full.Data) != 9 {
			t.Fatalf("%s full: total %d rows %d", sortDir, full.Total, len(full.Data))
		}
		for perPage := 1; perPage <= 10; perPage++ {
			var got []StockMovementRow
			for page := 1; ; page++ {
				r := runStockMovements(t, userID, base+"&sort="+sortDir+"&per_page="+strconv.Itoa(perPage)+"&page="+strconv.Itoa(page))
				if r.Total != 9 {
					t.Fatalf("total = %d", r.Total)
				}
				if len(r.Data) == 0 {
					break
				}
				got = append(got, r.Data...)
			}
			if len(got) != len(full.Data) {
				t.Fatalf("%s per_page=%d: %d rows, want %d", sortDir, perPage, len(got), len(full.Data))
			}
			for i := range got {
				if got[i].ID != full.Data[i].ID || !approxEq(got[i].Stock, full.Data[i].Stock) || !approxEq(got[i].StockValue, full.Data[i].StockValue) {
					t.Fatalf("%s per_page=%d row %d: got %+v want %+v", sortDir, perPage, i, got[i], full.Data[i])
				}
			}
		}
	}

	asc := runStockMovements(t, userID, base+"&per_page=0&sort=asc").Data
	if asc[3].TransactionType != "untracked" || asc[3].ProductID != b || !approxEq(asc[3].Stock, 35) {
		t.Fatalf("baseline row = %+v", asc[3])
	}
	last := asc[len(asc)-1]
	if last.ProductID != b || !approxEq(last.Stock, 32) {
		t.Fatalf("beta closing row = %+v", last)
	}

	// SQL row filters agree with the summary's Go filters.
	for _, q := range []string{"type=sale", "type=purchase_return", "type=in", "type=out", "type=untracked", "search=beta", "search=imported"} {
		rows := runStockMovements(t, userID, base+"&per_page=2&"+q)
		sum := runStockMovementSummary(t, userID, base+"&"+q)
		if rows.Total != sum.Summary.Count {
			t.Fatalf("%s: paged total %d, summary count %d", q, rows.Total, sum.Summary.Count)
		}
	}
	if n := runStockMovements(t, userID, base+"&type=purchase_return").Total; n != 1 {
		t.Fatalf("purchase returns = %d, want 1", n)
	}
}
