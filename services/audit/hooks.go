package audit

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"

	"truerp/models"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// auditedTables maps an audited table to the entity_type written in the log.
// Covers the modules requested for audit: products/categories, parties,
// inventory, purchases, sales, expenses, cash & bank, profit distribution,
// HR & payroll, users and stores.
var auditedTables = map[string]string{
	// Products & categories
	"products":           "product",
	"product_images":     "product_image",
	"product_variants":   "product_variant",
	"serial_numbers":     "serial_number",
	"categories":         "product_category",
	"expense_categories": "expense_category",
	// Parties
	"parties": "party",
	// Inventory
	"warehouses":              "warehouse",
	"inventory_stocks":        "inventory_stock",
	"stock_entries":           "stock_entry",
	"stock_transfers":         "stock_transfer",
	"stock_transfer_items":    "stock_transfer_item",
	"stock_opening_overrides": "inventory_opening",
	// Purchases
	"purchase_bills":         "purchase_bill",
	"purchase_bill_items":    "purchase_bill_item",
	"purchase_orders":        "purchase_order",
	"purchase_order_items":   "purchase_order_item",
	"purchase_receipts":      "purchase_receipt",
	"purchase_receipt_items": "purchase_receipt_item",
	"purchase_returns":       "purchase_return",
	"purchase_return_items":  "purchase_return_item",
	// Sales
	"invoices":               "sale_invoice",
	"invoice_items":          "sale_invoice_item",
	"sales_returns":          "sales_return",
	"sales_return_items":     "sales_return_item",
	"payments":               "payment_in",
	"payment_outs":           "payment_out",
	"quotations":             "quotation",
	"quotation_items":        "quotation_item",
	"quotation_versions":     "quotation_version",
	"credit_notes":           "credit_note",
	"credit_note_items":      "credit_note_item",
	"debit_notes":            "debit_note",
	"debit_note_items":       "debit_note_item",
	"delivery_challans":      "delivery_challan",
	"delivery_challan_items": "delivery_challan_item",
	// Expenses
	"expenses":      "expense",
	"expense_items": "expense_item",
	// Cash & bank
	"cash_transactions":           "cash_bank_transaction",
	"bank_accounts":               "bank_account",
	"cash_movements":              "cash_movement",
	"bank_reconciliations":        "bank_reconciliation",
	"payment_method_account_maps": "payment_method_account_map",
	// Profit distribution
	"profit_distributions": "profit_distribution",
	"partners":             "partner",
	// HR & payroll
	"staff":                  "staff",
	"attendances":            "attendance",
	"payrolls":               "payroll",
	"payroll_payments":       "payroll_payment",
	"staff_deductions":       "staff_deduction",
	"staff_advance_payments": "staff_advance_payment",
	"staff_extra_amounts":    "staff_extra_amount",
	// Users & stores
	"users":      "user",
	"user_roles": "user_role",
	"roles":      "role",
	"stores":     "store",
	"businesses": "business",
}

// nameColumnPriority lists columns checked, in order, to derive a
// human-readable entity_name for a log row.
var nameColumnPriority = []string{
	"name", "invoice_number", "bill_number", "payment_number", "order_number",
	"receipt_number", "challan_number", "quotation_number", "return_number",
	"credit_note_number", "debit_note_number", "payroll_number", "reference",
	"title", "item_code", "sku", "code", "email", "phone", "date", "id",
}

// maskedSubstrings marks snapshot columns whose values must never be stored.
var maskedSubstrings = []string{"password", "secret", "token", "api_key", "private_key"}

// maxSnapshotRows bounds how many rows of a bulk statement are snapshotted.
const maxSnapshotRows = 1000

const instancePrevRows = "audit:prev_rows"

// RegisterHooks installs create/update/delete capture callbacks on db.
func RegisterHooks(db *gorm.DB) {
	cb := db.Callback()
	cb.Create().After("gorm:create").Register("audit:after_create", afterCreate)
	cb.Update().Before("gorm:update").Register("audit:before_mutate", beforeMutate)
	cb.Update().After("gorm:update").Register("audit:after_update", afterUpdate)
	cb.Delete().Before("gorm:delete").Register("audit:before_mutate", beforeMutate)
	cb.Delete().After("gorm:delete").Register("audit:after_delete", afterDelete)
}

// beforeMutate snapshots the rows a statement is about to change so the
// after-callback can record them as previous_data.
func beforeMutate(db *gorm.DB) {
	stmt := db.Statement
	if stmt == nil || stmt.Error != nil {
		return
	}
	if _, ok := auditedTables[stmt.Table]; !ok {
		return
	}
	if rows := snapshotRows(db); rows != nil {
		stmt.InstanceSet(instancePrevRows, rows)
	}
}

// snapshotRows selects the rows targeted by the statement's WHERE clause plus
// any primary-key identity carried by the destination/model value.
func snapshotRows(db *gorm.DB) []map[string]interface{} {
	stmt := db.Statement
	tx := db.Session(&gorm.Session{NewDB: true, SkipHooks: true}).Table(stmt.Table)

	hasCond := false
	if wc, ok := stmt.Clauses["WHERE"]; ok {
		if w, ok := wc.Expression.(clause.Where); ok && len(w.Exprs) > 0 {
			tx.Statement.AddClause(clause.Where{Exprs: w.Exprs})
			hasCond = true
		}
	}
	if pkCol, ids := pkIdentities(stmt); pkCol != "" && len(ids) > 0 {
		tx = tx.Where(pkCol+" IN ?", ids)
		hasCond = true
	}
	if !hasCond {
		// Never snapshot an unscoped whole-table write.
		return nil
	}
	var rows []map[string]interface{}
	if err := tx.Limit(maxSnapshotRows).Find(&rows).Error; err != nil {
		return nil
	}
	for i := range rows {
		rows[i] = normalizeRow(rows[i])
	}
	return rows
}

// pkIdentities returns the (single-column) primary key column and the identity
// values present in the statement destination or model.
func pkIdentities(stmt *gorm.Statement) (string, []interface{}) {
	s := stmt.Schema
	if s == nil || len(s.PrimaryFields) != 1 {
		return "", nil
	}
	field := s.PrimaryFields[0]
	var values []interface{}
	var collect func(v reflect.Value)
	collect = func(v reflect.Value) {
		for v.IsValid() && (v.Kind() == reflect.Ptr || v.Kind() == reflect.Interface) {
			if v.IsNil() {
				return
			}
			v = v.Elem()
		}
		if !v.IsValid() {
			return
		}
		switch v.Kind() {
		case reflect.Slice, reflect.Array:
			for i := 0; i < v.Len(); i++ {
				collect(v.Index(i))
			}
		case reflect.Map:
			for _, key := range []string{field.DBName, field.Name, "id", "ID"} {
				kv := reflect.ValueOf(key)
				if kv.Type().ConvertibleTo(v.Type().Key()) {
					if val := v.MapIndex(kv.Convert(v.Type().Key())); val.IsValid() && val.CanInterface() {
						if iv := val.Interface(); iv != nil {
							values = append(values, iv)
						}
						return
					}
				}
			}
		case reflect.Struct:
			if val, zero := field.ValueOf(stmt.Context, v); !zero && val != nil {
				values = append(values, val)
			}
		}
	}
	if stmt.ReflectValue.IsValid() {
		collect(stmt.ReflectValue)
	}
	if len(values) == 0 && stmt.Model != nil {
		collect(reflect.ValueOf(stmt.Model))
	}
	return field.DBName, values
}

func afterCreate(db *gorm.DB) {
	emitChanges(db, "create")
}

func afterUpdate(db *gorm.DB) {
	emitChanges(db, "update")
}

func afterDelete(db *gorm.DB) {
	emitChanges(db, "delete")
}

func emitChanges(db *gorm.DB, action string) {
	stmt := db.Statement
	if stmt == nil || stmt.Error != nil {
		return
	}
	entityType, ok := auditedTables[stmt.Table]
	if !ok {
		return
	}
	actor := resolveActor(stmt.Context)

	var prevRows []map[string]interface{}
	if v, ok := stmt.InstanceGet(instancePrevRows); ok {
		prevRows, _ = v.([]map[string]interface{})
	}

	switch action {
	case "create":
		newRows := snapshotRows(db)
		if len(newRows) == 0 {
			newRows = destRows(stmt)
		}
		for _, row := range newRows {
			emit(actor, entityType, action, row, nil, row, nil)
		}
	case "update":
		newRows := snapshotRows(db)
		prevByID := indexByID(prevRows)
		newByID := indexByID(newRows)
		for id, prev := range prevByID {
			emit(actor, entityType, action, rowOr(prev, newByID[id]), prev, newByID[id], diffRows(prev, newByID[id]))
		}
		for id, newRow := range newByID {
			if _, seen := prevByID[id]; !seen {
				emit(actor, entityType, action, newRow, nil, newRow, nil)
			}
		}
		if len(prevRows) == 0 && len(newRows) == 0 {
			emit(actor, entityType, action, fallbackRef(stmt), nil, nil, nil)
		}
	case "delete":
		for _, prev := range prevRows {
			emit(actor, entityType, action, prev, prev, nil, nil)
		}
		if len(prevRows) == 0 {
			emit(actor, entityType, action, fallbackRef(stmt), nil, nil, nil)
		}
	}
}

func fallbackRef(stmt *gorm.Statement) map[string]interface{} {
	ref := map[string]interface{}{}
	if _, ids := pkIdentities(stmt); len(ids) > 0 {
		ref["id"] = asString(ids[0])
	}
	return ref
}

// emit builds and enqueues a single audit log entry.
func emit(actor *Actor, entityType, action string, ref, prev, newRow map[string]interface{}, changes map[string]interface{}) {
	mask(prev)
	mask(newRow)

	entry := models.AuditLog{
		Action:      action,
		EntityType:  entityType,
		EntityName:  entityName(ref),
		Status:      "success",
		CreatedAt:   time.Now(),
		Description: describe(action, entityType, ref),
	}
	if id := uuidFromAny(ref["id"]); id != nil {
		entry.EntityID = id
	}
	if uid := uuidFromAny(ref["user_id"]); uid != nil {
		entry.UserID = *uid
	} else if uid := uuidFromAny(ref["owner_user_id"]); uid != nil {
		entry.UserID = *uid
	}
	if prev != nil {
		entry.PreviousData = toJSON(prev)
	}
	if newRow != nil {
		entry.NewData = toJSON(newRow)
	}
	if len(changes) > 0 {
		entry.Changes = toJSON(changes)
	}
	if actor != nil {
		entry.ChangedByUserID = &actor.UserID
		entry.ChangedByName = actor.UserName
		entry.UserName = actor.UserName
		entry.IPAddress = actor.IPAddress
		entry.UserAgent = actor.UserAgent
		entry.RequestID = actor.RequestID
		if entry.UserID == uuid.Nil {
			entry.UserID = actor.ScopeID
		}
	}
	if entry.UserID == uuid.Nil {
		// Rows without a user_id column (e.g. users) outside an authenticated
		// request: attribute the log to the entity itself where possible.
		if uid := uuidFromAny(ref["id"]); uid != nil {
			entry.UserID = *uid
		}
	}
	Enqueue(entry)
}

func describe(action, entityType string, ref map[string]interface{}) string {
	verb := map[string]string{"create": "Created", "update": "Updated", "delete": "Deleted"}[action]
	label := strings.ReplaceAll(entityType, "_", " ")
	if name := entityName(ref); name != "" {
		return fmt.Sprintf("%s %s: %s", verb, label, name)
	}
	return fmt.Sprintf("%s %s", verb, label)
}

func entityName(row map[string]interface{}) string {
	for _, col := range nameColumnPriority {
		if v, ok := row[col]; ok {
			if s := asString(v); s != "" {
				return s
			}
		}
	}
	return ""
}

func indexByID(rows []map[string]interface{}) map[string]map[string]interface{} {
	out := make(map[string]map[string]interface{}, len(rows))
	for _, r := range rows {
		key := asString(r["id"])
		if key == "" {
			key = fmt.Sprintf("%v", r)
		}
		out[key] = r
	}
	return out
}

// diffRows returns the fields whose values differ between prev and next.
func diffRows(prev, next map[string]interface{}) map[string]interface{} {
	if prev == nil || next == nil {
		return nil
	}
	diff := map[string]interface{}{}
	keys := map[string]bool{}
	for k := range prev {
		keys[k] = true
	}
	for k := range next {
		keys[k] = true
	}
	for k := range keys {
		ov, nv := prev[k], next[k]
		if !valuesEqual(ov, nv) {
			diff[k] = map[string]interface{}{"old": normalizeValue(ov), "new": normalizeValue(nv)}
		}
	}
	if len(diff) == 0 {
		return nil
	}
	return diff
}

func valuesEqual(a, b interface{}) bool {
	return asString(a) == asString(b)
}

// normalizeRow converts driver-native values into JSON-friendly ones and
// redacts sensitive columns.
func normalizeRow(row map[string]interface{}) map[string]interface{} {
	for k, v := range row {
		row[k] = normalizeValue(v)
	}
	mask(row)
	return row
}

func normalizeValue(v interface{}) interface{} {
	if b, ok := v.([]byte); ok {
		return string(b)
	}
	return v
}

// mask redacts sensitive fields in-place.
func mask(row map[string]interface{}) {
	for k := range row {
		lk := strings.ToLower(k)
		for _, sub := range maskedSubstrings {
			if strings.Contains(lk, sub) {
				row[k] = "[REDACTED]"
				break
			}
		}
	}
}

func rowOr(a, b map[string]interface{}) map[string]interface{} {
	if a != nil {
		return a
	}
	return b
}

func asString(v interface{}) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case []byte:
		return string(t)
	case uuid.UUID:
		return t.String()
	default:
		return fmt.Sprintf("%v", t)
	}
}

func uuidFromAny(v interface{}) *uuid.UUID {
	switch t := v.(type) {
	case uuid.UUID:
		if t == uuid.Nil {
			return nil
		}
		return &t
	case *uuid.UUID:
		return t
	case string:
		if id, err := uuid.Parse(t); err == nil {
			return &id
		}
	case []byte:
		if id, err := uuid.Parse(string(t)); err == nil {
			return &id
		}
	}
	return nil
}

func toJSON(v interface{}) string {
	if v == nil {
		return ""
	}
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

// destRows extracts new-row payloads directly from the statement destination
// when a DB read-back isn't possible (e.g. map-based creates).
func destRows(stmt *gorm.Statement) []map[string]interface{} {
	if !stmt.ReflectValue.IsValid() {
		return nil
	}
	var rows []map[string]interface{}
	var collect func(v reflect.Value)
	collect = func(v reflect.Value) {
		for v.IsValid() && (v.Kind() == reflect.Ptr || v.Kind() == reflect.Interface) {
			if v.IsNil() {
				return
			}
			v = v.Elem()
		}
		if !v.IsValid() {
			return
		}
		switch v.Kind() {
		case reflect.Slice, reflect.Array:
			for i := 0; i < v.Len() && i < maxSnapshotRows; i++ {
				collect(v.Index(i))
			}
		default:
			if v.CanInterface() {
				if b, err := json.Marshal(v.Interface()); err == nil {
					var m map[string]interface{}
					if json.Unmarshal(b, &m) == nil && m != nil {
						rows = append(rows, normalizeRow(m))
					}
				}
			}
		}
	}
	collect(stmt.ReflectValue)
	return rows
}
