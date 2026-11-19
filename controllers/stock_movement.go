package controllers

import (
	"errors"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
	"truerp/models"
	"truerp/utils"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// StockMovementRow is one ledger movement for a product, with the product's
// running stock position after the movement, replayed at weighted average
// cost from the start of the ledger.
type StockMovementRow struct {
	ID              string     `json:"id"`
	Date            string     `json:"date"` // YYYY-MM-DD
	ProductID       uuid.UUID  `json:"product_id"`
	ProductName     string     `json:"product_name"`
	SKU             string     `json:"sku"`
	ItemCode        string     `json:"item_code"`
	Unit            string     `json:"unit"`
	OutletID        uuid.UUID  `json:"outlet_id"`
	OutletName      string     `json:"outlet_name"`
	TransactionType string     `json:"transaction_type"` // purchase, sale, sales_return, purchase_return, opening, adjustment, transfer, untracked
	TypeLabel       string     `json:"type_label"`
	DocumentNo      string     `json:"document_no"`
	ReferenceType   string     `json:"reference_type"`
	ReferenceID     *uuid.UUID `json:"reference_id,omitempty"`
	BatchNo         string     `json:"batch_no"`
	Notes           string     `json:"notes"`
	QtyIn           float64    `json:"qty_in"`
	QtyOut          float64    `json:"qty_out"`
	Stock           float64    `json:"stock"`          // product stock after this movement
	UnitCost        float64    `json:"unit_cost"`      // cost per unit of this transaction
	StockValue      float64    `json:"stock_value"`    // product stock value after this movement
	Total           float64    `json:"total"`          // transaction value: sale amount for sales/sales returns, cost otherwise
	SalePrice       float64    `json:"sale_price"`     // net unit sale price (sales and sales returns only)
	Profit          float64    `json:"profit"`         // (sale price − cost) × qty; negative reversal for sales returns
	HasSalePrice    bool       `json:"has_sale_price"` // true for sales / sales returns
	CostEstimated   bool       `json:"cost_estimated"` // cost fell back to product purchase price
}

type StockMovementSummary struct {
	OpeningQty   float64 `json:"opening_qty"`
	OpeningValue float64 `json:"opening_value"`
	InQty        float64 `json:"in_qty"`
	OutQty       float64 `json:"out_qty"`
	ClosingQty   float64 `json:"closing_qty"`
	ClosingValue float64 `json:"closing_value"`
	PurchaseCost float64 `json:"purchase_cost"` // cost of purchase inflows in range
	SalesAmount  float64 `json:"sales_amount"`  // net sales less sales returns
	COGS         float64 `json:"cogs"`          // cost of goods sold less returns
	Profit       float64 `json:"profit"`
	Count        int     `json:"count"`
}

// StockMovementResponse is one server-side page of movements.
type StockMovementResponse struct {
	StartDate string             `json:"start_date"`
	EndDate   string             `json:"end_date"`
	Data      []StockMovementRow `json:"data"`
	Total     int                `json:"total"`
	Page      int                `json:"page"`
	PerPage   int                `json:"per_page"`
}

type StockMovementSummaryResponse struct {
	StartDate string               `json:"start_date"`
	EndDate   string               `json:"end_date"`
	Summary   StockMovementSummary `json:"summary"`
	// OpeningClosingFiltered is true when a type/search filter narrows the
	// rows, so opening/closing describe the product scope, not the rows.
	OpeningClosingFiltered bool `json:"opening_closing_filtered"`
}

type movementLedgerEntry struct {
	ID            uuid.UUID
	ProductID     *uuid.UUID
	OutletID      uuid.UUID
	EntryType     string
	Quantity      float64
	CostPrice     float64
	BatchNo       string
	ReferenceID   uuid.UUID
	ReferenceType string
	Notes         string
	EntryDate     time.Time
	CreatedAt     time.Time
}

const untrackedEntryType = "untracked"

var movementLedgerColumns = []string{"id", "product_id", "outlet_id", "entry_type", "quantity", "cost_price",
	"batch_no", "reference_id", "reference_type", "notes", "entry_date", "created_at"}

// movementTransactionType maps a raw ledger entry to the movement type shown
// on the page. 'return' entries are split by direction: stock coming back in
// is a sales return, stock going out is a purchase return.
func movementTransactionType(entryType, refType string, qty float64) string {
	if entryType == "return" {
		if refType == "sales_return" || qty > 0 {
			return "sales_return"
		}
		return "purchase_return"
	}
	return entryType
}

var movementTypeLabels = map[string]string{
	"purchase":         "Purchase",
	"sale":             "Sale",
	"sales_return":     "Sales Return",
	"purchase_return":  "Purchase Return",
	"opening":          "Opening Stock",
	"adjustment":       "Adjustment",
	"transfer":         "Transfer",
	untrackedEntryType: "Untracked Stock",
}

func movementTypeLabel(t string) string {
	if l, ok := movementTypeLabels[t]; ok {
		return l
	}
	if t == "" {
		return "Other"
	}
	return strings.ToUpper(t[:1]) + strings.ReplaceAll(t[1:], "_", " ")
}

// ---------------------------------------------------------------------------
// Filters and scope
// ---------------------------------------------------------------------------

type movementFilters struct {
	From, To           string
	ProductID          *uuid.UUID
	OutletID           *uuid.UUID
	Category           string
	ItemName, ItemCode string
	Unit               string
	Type               string // movement type, or in / out
	Search             string // lowercased
	SortDesc           bool
}

func parseMovementFilters(c *gin.Context) (movementFilters, error) {
	clean := func(key string) string {
		v := strings.TrimSpace(c.Query(key))
		if v == "all" {
			return ""
		}
		return v
	}
	now := time.Now()
	f := movementFilters{
		From:     clean("from_date"),
		To:       clean("to_date"),
		Category: clean("category"),
		ItemName: clean("item_name"),
		ItemCode: clean("item_code"),
		Unit:     clean("unit"),
		Type:     clean("type"),
		Search:   strings.ToLower(clean("search")),
		SortDesc: c.DefaultQuery("sort", "desc") != "asc",
	}
	if f.From == "" {
		f.From = now.Format("2006-01") + "-01"
	}
	if f.To == "" {
		f.To = now.Format("2006-01-02")
	}
	if _, err := time.Parse("2006-01-02", f.From); err != nil {
		return f, errors.New("Invalid from_date (expected YYYY-MM-DD)")
	}
	if _, err := time.Parse("2006-01-02", f.To); err != nil {
		return f, errors.New("Invalid to_date (expected YYYY-MM-DD)")
	}
	if f.To < f.From {
		return f, errors.New("to_date must be on or after from_date")
	}
	if v := clean("product_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			return f, errors.New("Invalid product_id")
		}
		f.ProductID = &id
	}
	if v := clean("outlet_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			return f, errors.New("Invalid outlet_id")
		}
		f.OutletID = &id
	}
	return f, nil
}

type movementScope struct {
	userID uuid.UUID
	f      movementFilters
}

// products is the user's products matching the product-level filters
// (item, category, item name, item code / SKU, unit), deleted ones included
// so their history still shows.
func (s movementScope) products() *gorm.DB {
	q := utils.DB.Model(&models.Product{}).Unscoped().Where("user_id = ?", s.userID)
	if s.f.ProductID != nil {
		q = q.Where("id = ?", *s.f.ProductID)
	}
	if s.f.Category != "" {
		q = q.Where("category = ?", s.f.Category)
	}
	if s.f.ItemName != "" {
		if compact := utils.CompactLikePattern(s.f.ItemName); compact != "" {
			q = q.Where(utils.SQLCompactLike("name"), compact)
		}
	}
	if s.f.ItemCode != "" {
		like := "%" + strings.ToLower(s.f.ItemCode) + "%"
		q = q.Where("LOWER(item_code) LIKE ? OR LOWER(sku) LIKE ?", like, like)
	}
	if s.f.Unit != "" {
		q = q.Where("LOWER(unit) = ?", strings.ToLower(s.f.Unit))
	}
	return q
}

func (s movementScope) productMeta(ids []uuid.UUID) (map[uuid.UUID]models.Product, error) {
	var products []models.Product
	q := s.products().Select("id", "name", "sku", "item_code", "unit", "category", "purchase_price")
	if ids != nil {
		if len(ids) == 0 {
			return map[uuid.UUID]models.Product{}, nil
		}
		q = q.Where("id IN ?", ids)
	}
	if err := q.Find(&products).Error; err != nil {
		return nil, err
	}
	meta := make(map[uuid.UUID]models.Product, len(products))
	for _, p := range products {
		meta[p.ID] = p
	}
	return meta, nil
}

// ledger is every stock-moving (approved, non-reservation) entry for the
// products in scope, optionally limited to specific products.
func (s movementScope) ledger(ids []uuid.UUID) *gorm.DB {
	q := utils.DB.Model(&models.StockEntry{}).
		Where("stock_entries.user_id = ? AND stock_entries.product_id IS NOT NULL", s.userID).
		Where("stock_entries.entry_type NOT IN ?", snapshotExcludedEntryTypes).
		Where("stock_entries.approval_status = 'approved' OR stock_entries.approval_status = '' OR stock_entries.approval_status IS NULL").
		Where("stock_entries.product_id IN (?)", s.products().Select("id"))
	if ids != nil {
		q = q.Where("stock_entries.product_id IN ?", ids)
	}
	if s.f.OutletID != nil {
		q = q.Where("stock_entries.outlet_id = ?", *s.f.OutletID)
	}
	return q
}

// pageRows narrows the ledger to the rows the page lists: the date range,
// the movement type and the free-text search — the SQL mirror of
// movementMatches.
func (s movementScope) pageRows() *gorm.DB {
	q := s.ledger(nil).
		Where(utils.SQLDateGTE("stock_entries.entry_date"), s.f.From).
		Where(utils.SQLDateLTE("stock_entries.entry_date"), s.f.To)

	const salesReturn = "stock_entries.entry_type = 'return' AND (COALESCE(stock_entries.reference_type, '') = 'sales_return' OR stock_entries.quantity > 0)"
	switch s.f.Type {
	case "":
	case "in":
		q = q.Where("stock_entries.quantity > 0")
	case "out":
		q = q.Where("stock_entries.quantity < 0")
	case "sales_return":
		q = q.Where(salesReturn)
	case "purchase_return":
		q = q.Where("stock_entries.entry_type = 'return' AND NOT (" + salesReturn + ")")
	case untrackedEntryType:
		q = q.Where("1 = 0") // synthetic rows only
	default:
		q = q.Where("stock_entries.entry_type = ?", s.f.Type)
	}

	if s.f.Search != "" {
		like := "%" + s.f.Search + "%"
		q = q.Where(
			"LOWER(stock_entries.batch_no) LIKE ? OR LOWER(stock_entries.notes) LIKE ?"+
				" OR stock_entries.product_id IN (SELECT id FROM products WHERE user_id = ? AND (LOWER(name) LIKE ? OR LOWER(sku) LIKE ? OR LOWER(item_code) LIKE ?))"+
				" OR (stock_entries.reference_type = 'invoice' AND stock_entries.reference_id IN (SELECT id FROM invoices WHERE user_id = ? AND LOWER(invoice_number) LIKE ?))"+
				" OR (stock_entries.reference_type = 'purchase_bill' AND stock_entries.reference_id IN (SELECT id FROM purchase_bills WHERE user_id = ? AND LOWER(bill_number) LIKE ?))"+
				" OR (stock_entries.reference_type = 'sales_return' AND stock_entries.reference_id IN (SELECT id FROM sales_returns WHERE user_id = ? AND LOWER(return_number) LIKE ?))",
			like, like,
			s.userID, like, like, like,
			s.userID, like,
			s.userID, like,
			s.userID, like,
		)
	}
	return q
}

// baselines returns synthetic inflows for live stock not explained by the
// ledger (migrated stock, direct writes), anchored at the stock row's
// creation date and valued at its average cost — the same reconciliation the
// stock snapshots use, so balances end at the real on-hand quantity. Only
// anchors on or before the range end are returned.
func (s movementScope) baselines(ids []uuid.UUID) []movementLedgerEntry {
	var nets []struct {
		ProductID uuid.UUID
		OutletID  uuid.UUID
		Net       float64
	}
	s.ledger(ids).
		Select("stock_entries.product_id, stock_entries.outlet_id, COALESCE(SUM(stock_entries.quantity), 0) AS net").
		Group("stock_entries.product_id, stock_entries.outlet_id").Scan(&nets)
	ledgerNet := make(map[snapshotKey]float64, len(nets))
	for _, n := range nets {
		ledgerNet[snapshotKey{ProductID: n.ProductID, OutletID: n.OutletID}] = n.Net
	}

	var stocks []models.InventoryStock
	sq := utils.DB.Where("user_id = ?", s.userID).
		Where("product_id IN (?)", s.products().Select("id"))
	if ids != nil {
		sq = sq.Where("product_id IN ?", ids)
	}
	if s.f.OutletID != nil {
		sq = sq.Where("outlet_id = ?", *s.f.OutletID)
	}
	sq.Select("product_id", "outlet_id", "quantity", "average_cost", "created_at").Find(&stocks)

	type agg struct {
		Qty, Value float64
		FirstAt    time.Time
	}
	aggs := make(map[snapshotKey]*agg)
	for _, st := range stocks {
		k := snapshotKey{ProductID: st.ProductID, OutletID: st.OutletID}
		a := aggs[k]
		if a == nil {
			a = &agg{FirstAt: st.CreatedAt}
			aggs[k] = a
		}
		a.Qty += st.Quantity
		a.Value += st.Quantity * st.AverageCost
		if st.CreatedAt.Before(a.FirstAt) {
			a.FirstAt = st.CreatedAt
		}
	}

	out := make([]movementLedgerEntry, 0)
	for k, a := range aggs {
		base := a.Qty - ledgerNet[k]
		if math.Abs(base) < 1e-9 || a.FirstAt.Format("2006-01-02") > s.f.To {
			continue
		}
		cost := 0.0
		if a.Qty > 0 {
			cost = a.Value / a.Qty
		}
		pid := k.ProductID
		out = append(out, movementLedgerEntry{
			ProductID: &pid,
			OutletID:  k.OutletID,
			EntryType: untrackedEntryType,
			Quantity:  base,
			CostPrice: cost,
			Notes:     "Stock on hand without a matching ledger entry (e.g. imported stock)",
			EntryDate: a.FirstAt,
			CreatedAt: a.FirstAt,
		})
	}
	return out
}

func untrackedRowID(e movementLedgerEntry) string {
	return "untracked-" + e.ProductID.String() + "-" + e.OutletID.String()
}

func movementRowKey(e movementLedgerEntry) string {
	if e.EntryType == untrackedEntryType {
		return untrackedRowID(e)
	}
	return e.ID.String()
}

// movementLess is the chronological replay/listing order: by calendar day,
// untracked baselines first within a day, then creation time, then id. The
// paged SQL query orders the same way.
func movementLess(a, b movementLedgerEntry) bool {
	da, db := a.EntryDate.Format("2006-01-02"), b.EntryDate.Format("2006-01-02")
	if da != db {
		return da < db
	}
	ua, ub := a.EntryType == untrackedEntryType, b.EntryType == untrackedEntryType
	if ua != ub {
		return ua
	}
	if ua {
		if a.ProductID.String() != b.ProductID.String() {
			return a.ProductID.String() < b.ProductID.String()
		}
		return a.OutletID.String() < b.OutletID.String()
	}
	if !a.CreatedAt.Equal(b.CreatedAt) {
		return a.CreatedAt.Before(b.CreatedAt)
	}
	return a.ID.String() < b.ID.String()
}

// ---------------------------------------------------------------------------
// Reference lookups and replay
// ---------------------------------------------------------------------------

type saleKey struct{ InvoiceID, ProductID uuid.UUID }

type movementRefs struct {
	docNumbers  map[uuid.UUID]string
	salePrices  map[saleKey]float64
	outletNames map[uuid.UUID]string
}

// loadMovementRefs resolves document numbers and net sale prices for the
// given entries, and warehouse names for the user.
func loadMovementRefs(userID uuid.UUID, entries []movementLedgerEntry) movementRefs {
	refs := movementRefs{
		docNumbers:  map[uuid.UUID]string{},
		salePrices:  map[saleKey]float64{},
		outletNames: map[uuid.UUID]string{},
	}
	invoiceIDs, billIDs, returnIDs := []uuid.UUID{}, []uuid.UUID{}, []uuid.UUID{}
	seen := map[uuid.UUID]bool{}
	for _, e := range entries {
		if e.ReferenceID == uuid.Nil || seen[e.ReferenceID] {
			continue
		}
		seen[e.ReferenceID] = true
		switch e.ReferenceType {
		case "invoice":
			invoiceIDs = append(invoiceIDs, e.ReferenceID)
		case "purchase_bill":
			billIDs = append(billIDs, e.ReferenceID)
		case "sales_return":
			returnIDs = append(returnIDs, e.ReferenceID)
		}
	}

	type docNo struct {
		ID     uuid.UUID
		Number string
	}
	lookup := func(model interface{}, column string, ids []uuid.UUID) {
		for _, chunk := range chunkUUIDs(ids, 500) {
			var rows []docNo
			utils.DB.Model(model).Unscoped().Where("id IN ?", chunk).
				Select("id, " + column + " AS number").Scan(&rows)
			for _, r := range rows {
				refs.docNumbers[r.ID] = r.Number
			}
		}
	}
	lookup(&models.Invoice{}, "invoice_number", invoiceIDs)
	lookup(&models.PurchaseBill{}, "bill_number", billIDs)
	lookup(&models.SalesReturn{}, "return_number", returnIDs)

	// Net unit sale price per (invoice, product): unit price after the item
	// discount, tax-exclusive — the same basis as the billwise profit report.
	for _, chunk := range chunkUUIDs(invoiceIDs, 500) {
		var items []models.InvoiceItem
		utils.DB.Where("invoice_id IN ? AND product_id IS NOT NULL", chunk).
			Select("invoice_id", "product_id", "quantity", "unit_price", "discount").Find(&items)
		amt := map[saleKey][2]float64{}
		for _, it := range items {
			k := saleKey{it.InvoiceID, *it.ProductID}
			a := amt[k]
			a[0] += it.Quantity * it.UnitPrice * (1 - it.Discount/100)
			a[1] += it.Quantity
			amt[k] = a
		}
		for k, a := range amt {
			if a[1] > 0 {
				refs.salePrices[k] = a[0] / a[1]
			}
		}
	}

	var warehouses []models.Warehouse
	utils.DB.Unscoped().Where("user_id = ?", userID).Select("id", "name").Find(&warehouses)
	for _, w := range warehouses {
		refs.outletNames[w.ID] = w.Name
	}
	return refs
}

type movementPosition struct {
	Qty, Value, Avg float64
}

// replayMovements walks entries chronologically per product at weighted
// average cost. emit is called for each entry on or after `from` with its
// computed row; onOpening is called once per product with its position just
// before its first in-range entry (or at the end, for products with none).
// It returns each product's final position.
func replayMovements(
	entries []movementLedgerEntry,
	meta map[uuid.UUID]models.Product,
	refs movementRefs,
	from string,
	emit func(e movementLedgerEntry, row StockMovementRow),
	onOpening func(p movementPosition),
) map[uuid.UUID]*movementPosition {
	sort.SliceStable(entries, func(i, j int) bool { return movementLess(entries[i], entries[j]) })

	positions := make(map[uuid.UUID]*movementPosition)
	openingTaken := make(map[uuid.UUID]bool)
	takeOpening := func(pid uuid.UUID, p *movementPosition) {
		if openingTaken[pid] {
			return
		}
		openingTaken[pid] = true
		if onOpening != nil {
			onOpening(*p)
		}
	}

	for _, e := range entries {
		pid := *e.ProductID
		pm, ok := meta[pid]
		if !ok {
			continue
		}
		p := positions[pid]
		if p == nil {
			p = &movementPosition{}
			positions[pid] = p
		}
		day := e.EntryDate.Format("2006-01-02")
		inRange := day >= from
		if inRange {
			takeOpening(pid, p)
		}

		txType := e.EntryType
		if txType != untrackedEntryType {
			txType = movementTransactionType(e.EntryType, e.ReferenceType, e.Quantity)
		}
		qty := e.Quantity
		absQty := math.Abs(qty)

		// Unit cost: purchase cost for costed inflows, else running average.
		estimated := false
		unitCost := p.Avg
		costed := snapshotCostedEntryTypes[e.EntryType] || e.EntryType == untrackedEntryType
		if qty > 0 && costed && e.CostPrice > 0 {
			unitCost = e.CostPrice
		}
		if unitCost <= 0 && pm.PurchasePrice > 0 {
			unitCost = pm.PurchasePrice
			estimated = true
		}

		// Inflows add at their cost; outflows (negative qty) remove at average cost.
		p.Qty += qty
		p.Value += qty * unitCost
		switch {
		case p.Qty > 1e-9:
			p.Avg = p.Value / p.Qty
		case math.Abs(p.Qty) <= 1e-9:
			p.Qty, p.Value = 0, 0
			if unitCost > 0 {
				p.Avg = unitCost // keep the last cost for later outflows
			}
		case unitCost > 0:
			// Negative stock: keep valuing at the last known cost.
			p.Avg = unitCost
			p.Value = p.Qty * unitCost
		}

		if !inRange {
			continue
		}

		row := StockMovementRow{
			ID:              movementRowKey(e),
			Date:            day,
			ProductID:       pid,
			ProductName:     pm.Name,
			SKU:             pm.SKU,
			ItemCode:        pm.ItemCode,
			Unit:            pm.Unit,
			OutletID:        e.OutletID,
			OutletName:      refs.outletNames[e.OutletID],
			TransactionType: txType,
			TypeLabel:       movementTypeLabel(txType),
			ReferenceType:   e.ReferenceType,
			BatchNo:         e.BatchNo,
			Notes:           e.Notes,
			Stock:           p.Qty,
			UnitCost:        unitCost,
			StockValue:      p.Value,
			CostEstimated:   estimated,
		}
		if e.ReferenceID != uuid.Nil {
			ref := e.ReferenceID
			row.ReferenceID = &ref
			row.DocumentNo = refs.docNumbers[ref]
		}
		if qty > 0 {
			row.QtyIn = qty
		} else {
			row.QtyOut = absQty
		}

		switch txType {
		case "sale":
			price := e.CostPrice
			if sp, ok := refs.salePrices[saleKey{e.ReferenceID, pid}]; ok {
				price = sp
			}
			row.HasSalePrice = true
			row.SalePrice = price
			row.Total = absQty * price
			row.Profit = absQty * (price - unitCost)
		case "sales_return":
			row.HasSalePrice = true
			row.SalePrice = e.CostPrice
			row.Total = absQty * e.CostPrice
			row.Profit = -absQty * (e.CostPrice - unitCost)
		default:
			row.Total = absQty * unitCost
		}
		emit(e, row)
	}

	// Products with no in-range movement still have an opening position.
	for pid, p := range positions {
		takeOpening(pid, p)
	}
	return positions
}

// movementMatches applies the row-level filters (type, search) in Go — the
// mirror of movementScope.pageRows for computed rows.
func movementMatches(row StockMovementRow, typeFilter, search string) bool {
	switch typeFilter {
	case "":
	case "in":
		if row.QtyIn <= 0 {
			return false
		}
	case "out":
		if row.QtyOut <= 0 {
			return false
		}
	default:
		if row.TransactionType != typeFilter {
			return false
		}
	}
	if search != "" {
		hay := strings.ToLower(row.ProductName + " " + row.SKU + " " + row.ItemCode + " " + row.DocumentNo + " " + row.BatchNo + " " + row.Notes)
		if !strings.Contains(hay, search) {
			return false
		}
	}
	return true
}

func loadLedgerEntries(q *gorm.DB) ([]movementLedgerEntry, error) {
	var entries []movementLedgerEntry
	cols := make([]string, len(movementLedgerColumns))
	for i, c := range movementLedgerColumns {
		cols[i] = "stock_entries." + c
	}
	err := q.Select(cols).Find(&entries).Error
	return entries, err
}

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

// GetStockMovements returns one server-side page of the stock ledger with
// running stock, weighted-average cost, stock value, sale price and profit.
//
// Rows are paged in SQL (LIMIT/OFFSET) with the type and search filters
// applied in the query. Untracked-stock baselines — which have no ledger row —
// are merged into the paged order by position. Running positions are then
// replayed only for the products on the page.
//
// Query params: from_date, to_date (YYYY-MM-DD, default current month),
// product_id, outlet_id, category, item_name, item_code (matches item code
// or SKU), unit, type (movement type or in/out), search,
// sort (asc|desc, default desc), page, per_page (default 50; <=0 returns all).
func GetStockMovements(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	f, err := parseMovementFilters(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	scope := movementScope{userID: userID, f: f}

	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}
	perPage, _ := strconv.Atoi(c.DefaultQuery("per_page", "50"))
	resp := StockMovementResponse{StartDate: f.From, EndDate: f.To, Data: []StockMovementRow{}, Page: page, PerPage: perPage}

	// Ledger rows matching the filters.
	var dbTotal int64
	if err := scope.pageRows().Count(&dbTotal).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to count stock movements"})
		return
	}

	// Untracked baselines listed in range that pass the row filters.
	allBaselines := scope.baselines(nil)
	baselineMeta := map[uuid.UUID]models.Product{}
	if len(allBaselines) > 0 {
		ids := make([]uuid.UUID, 0, len(allBaselines))
		for _, b := range allBaselines {
			ids = append(ids, *b.ProductID)
		}
		if baselineMeta, err = scope.productMeta(ids); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load products"})
			return
		}
	}
	listed := make([]movementLedgerEntry, 0)
	for _, b := range allBaselines {
		if b.EntryDate.Format("2006-01-02") < f.From {
			continue
		}
		m := baselineMeta[*b.ProductID]
		probe := StockMovementRow{
			TransactionType: untrackedEntryType,
			ProductName:     m.Name, SKU: m.SKU, ItemCode: m.ItemCode, Notes: b.Notes,
		}
		if b.Quantity > 0 {
			probe.QtyIn = b.Quantity
		} else {
			probe.QtyOut = -b.Quantity
		}
		if movementMatches(probe, f.Type, f.Search) {
			listed = append(listed, b)
		}
	}
	sort.SliceStable(listed, func(i, j int) bool { return movementLess(listed[i], listed[j]) })

	total := int(dbTotal) + len(listed)
	resp.Total = total

	// The requested window, expressed in ascending (chronological) positions.
	ascStart, ascEnd := 0, total
	if perPage > 0 {
		offset := (page - 1) * perPage
		if f.SortDesc {
			ascEnd = total - offset
			ascStart = ascEnd - perPage
		} else {
			ascStart = offset
			ascEnd = offset + perPage
		}
		if ascStart < 0 {
			ascStart = 0
		}
		if ascEnd > total {
			ascEnd = total
		}
	}
	if ascStart >= ascEnd {
		c.JSON(http.StatusOK, resp)
		return
	}

	// Ascending position of each listed baseline: baselines sort first within
	// their day, so it is its index plus the ledger rows on earlier days.
	baselinePos := make([]int, len(listed))
	rowsBefore := map[string]int{}
	for i, b := range listed {
		day := b.EntryDate.Format("2006-01-02")
		n, ok := rowsBefore[day]
		if !ok {
			var cnt int64
			scope.pageRows().Where("NOT ("+utils.SQLDateGTE("stock_entries.entry_date")+")", day).Count(&cnt)
			n = int(cnt)
			rowsBefore[day] = n
		}
		baselinePos[i] = i + n
	}
	baselinesBefore := func(pos int) int {
		return sort.SearchInts(baselinePos, pos)
	}
	dbStart := ascStart - baselinesBefore(ascStart)
	dbEnd := ascEnd - baselinesBefore(ascEnd)

	// Fetch exactly the ledger rows in the window. Descending pages query in
	// descending order so the newest pages stay cheap.
	var dbRows []movementLedgerEntry
	if dbEnd > dbStart {
		dayExpr := utils.SQLDateExpr("stock_entries.entry_date")
		q := scope.pageRows()
		if f.SortDesc {
			q = q.Order(dayExpr + " DESC").Order("stock_entries.created_at DESC").Order("stock_entries.id DESC").
				Offset(int(dbTotal) - dbEnd)
		} else {
			q = q.Order(dayExpr + " ASC").Order("stock_entries.created_at ASC").Order("stock_entries.id ASC").
				Offset(dbStart)
		}
		if dbRows, err = loadLedgerEntries(q.Limit(dbEnd - dbStart)); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load stock movements"})
			return
		}
		if f.SortDesc {
			for i, j := 0, len(dbRows)-1; i < j; i, j = i+1, j-1 {
				dbRows[i], dbRows[j] = dbRows[j], dbRows[i]
			}
		}
	}

	// Merge into ascending page order.
	pageEntries := make([]movementLedgerEntry, 0, ascEnd-ascStart)
	bi := baselinesBefore(ascStart)
	di := 0
	for pos := ascStart; pos < ascEnd; pos++ {
		if bi < len(listed) && baselinePos[bi] == pos {
			pageEntries = append(pageEntries, listed[bi])
			bi++
		} else if di < len(dbRows) {
			pageEntries = append(pageEntries, dbRows[di])
			di++
		}
	}

	// Replay the full history of just the products on this page.
	productSet := map[uuid.UUID]bool{}
	lastDay := f.From
	for _, e := range pageEntries {
		productSet[*e.ProductID] = true
		if d := e.EntryDate.Format("2006-01-02"); d > lastDay {
			lastDay = d
		}
	}
	productIDs := make([]uuid.UUID, 0, len(productSet))
	for id := range productSet {
		productIDs = append(productIDs, id)
	}
	meta, err := scope.productMeta(productIDs)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load products"})
		return
	}
	history, err := loadLedgerEntries(scope.ledger(productIDs).
		Where(utils.SQLDateLTE("stock_entries.entry_date"), lastDay))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load stock history"})
		return
	}
	for _, b := range allBaselines {
		if productSet[*b.ProductID] {
			history = append(history, b)
		}
	}

	refs := loadMovementRefs(userID, pageEntries)
	wanted := make(map[string]bool, len(pageEntries))
	for _, e := range pageEntries {
		wanted[movementRowKey(e)] = true
	}
	computed := make(map[string]StockMovementRow, len(pageEntries))
	replayMovements(history, meta, refs, f.From, func(e movementLedgerEntry, row StockMovementRow) {
		if k := movementRowKey(e); wanted[k] {
			computed[k] = row
		}
	}, nil)

	rows := make([]StockMovementRow, 0, len(pageEntries))
	for _, e := range pageEntries {
		if row, ok := computed[movementRowKey(e)]; ok {
			rows = append(rows, row)
		}
	}
	if f.SortDesc {
		for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
			rows[i], rows[j] = rows[j], rows[i]
		}
	}
	resp.Data = rows
	c.JSON(http.StatusOK, resp)
}

// GetStockMovementSummary returns period totals for the same filters as
// GetStockMovements: opening/closing position, in/out quantities, purchase
// cost, net sales, COGS and profit. COGS and stock values depend on the full
// weighted-average history, so this replays every product in scope — the
// page requests it once per filter change rather than per page.
func GetStockMovementSummary(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	f, err := parseMovementFilters(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	scope := movementScope{userID: userID, f: f}
	resp := StockMovementSummaryResponse{
		StartDate:              f.From,
		EndDate:                f.To,
		OpeningClosingFiltered: f.Type != "" || f.Search != "",
	}

	meta, err := scope.productMeta(nil)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load products"})
		return
	}
	if len(meta) == 0 {
		c.JSON(http.StatusOK, resp)
		return
	}
	entries, err := loadLedgerEntries(scope.ledger(nil).
		Where(utils.SQLDateLTE("stock_entries.entry_date"), f.To))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load stock entries"})
		return
	}
	entries = append(entries, scope.baselines(nil)...)

	inRange := make([]movementLedgerEntry, 0)
	for _, e := range entries {
		if e.EntryDate.Format("2006-01-02") >= f.From {
			inRange = append(inRange, e)
		}
	}
	refs := loadMovementRefs(userID, inRange)

	s := &resp.Summary
	positions := replayMovements(entries, meta, refs, f.From, func(e movementLedgerEntry, row StockMovementRow) {
		if !movementMatches(row, f.Type, f.Search) {
			return
		}
		s.Count++
		s.InQty += row.QtyIn
		s.OutQty += row.QtyOut
		qty := row.QtyIn + row.QtyOut
		switch row.TransactionType {
		case "purchase":
			s.PurchaseCost += row.Total
		case "sale":
			s.SalesAmount += row.Total
			s.COGS += qty * row.UnitCost
			s.Profit += row.Profit
		case "sales_return":
			s.SalesAmount -= row.Total
			s.COGS -= qty * row.UnitCost
			s.Profit += row.Profit
		}
	}, func(p movementPosition) {
		s.OpeningQty += p.Qty
		s.OpeningValue += p.Value
	})
	for _, p := range positions {
		s.ClosingQty += p.Qty
		s.ClosingValue += p.Value
	}
	c.JSON(http.StatusOK, resp)
}

func chunkUUIDs(ids []uuid.UUID, size int) [][]uuid.UUID {
	var out [][]uuid.UUID
	for len(ids) > 0 {
		n := size
		if len(ids) < n {
			n = len(ids)
		}
		out = append(out, ids[:n])
		ids = ids[n:]
	}
	return out
}
