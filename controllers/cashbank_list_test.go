package controllers

import (
	"encoding/json"
	"testing"
	"time"
	"truerp/models"
	"truerp/utils"

	"github.com/google/uuid"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func setupCashTransactionListTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&models.BankAccount{}, &models.CashTransaction{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	previousDB := utils.DB
	utils.DB = db
	t.Cleanup(func() { utils.DB = previousDB })
	return db
}

func seedCashTransactions(t *testing.T, db *gorm.DB, userID uuid.UUID) {
	t.Helper()
	base := time.Now().AddDate(0, 0, -5)
	types := []string{"add", "reduce", "transfer_in", "transfer_out", "expense", "profit_distribution"}
	amounts := []float64{100, 40, 60, 30, 20, 15}
	for i, txnType := range types {
		txn := models.CashTransaction{
			ID:              uuid.New(),
			UserID:          userID,
			TransactionType: txnType,
			Amount:          amounts[i],
			Date:            base.AddDate(0, 0, i),
			Description:     "txn " + txnType,
		}
		if err := db.Create(&txn).Error; err != nil {
			t.Fatalf("create transaction %s: %v", txnType, err)
		}
	}
}

func TestGetCashTransactionsPaginatedReturnsEnvelope(t *testing.T) {
	db := setupCashTransactionListTestDB(t)
	userID := uuid.New()
	seedCashTransactions(t, db, userID)

	c, rec := newListContext("/cash-bank/transactions", "?page=1&per_page=2")
	c.Set("user_id", userID)
	GetCashTransactions(c)

	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body struct {
		Transactions []models.CashTransaction `json:"transactions"`
		Total        int64                    `json:"total"`
		Page         int                      `json:"page"`
		PerPage      int                      `json:"per_page"`
		TotalIn      float64                  `json:"total_in"`
		TotalOut     float64                  `json:"total_out"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Total != 6 || body.Page != 1 || body.PerPage != 2 || len(body.Transactions) != 2 {
		t.Fatalf("unexpected envelope: total=%d page=%d per_page=%d rows=%d",
			body.Total, body.Page, body.PerPage, len(body.Transactions))
	}
	// Aggregates cover the whole filtered set, not just page 1.
	// in = add + transfer_in = 160; out = reduce + transfer_out + expense + profit_distribution = 105.
	if body.TotalIn != 160 || body.TotalOut != 105 {
		t.Fatalf("totals: in=%v out=%v, want 160/105", body.TotalIn, body.TotalOut)
	}
}

func TestGetCashTransactionsFilters(t *testing.T) {
	db := setupCashTransactionListTestDB(t)
	userID := uuid.New()
	seedCashTransactions(t, db, userID)

	// Type filter.
	c, rec := newListContext("/cash-bank/transactions", "?page=1&per_page=10&transaction_type=expense")
	c.Set("user_id", userID)
	GetCashTransactions(c)
	var body struct {
		Transactions []models.CashTransaction `json:"transactions"`
		Total        int64                    `json:"total"`
		TotalIn      float64                  `json:"total_in"`
		TotalOut     float64                  `json:"total_out"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Total != 1 || body.TotalOut != 20 || body.TotalIn != 0 {
		t.Fatalf("type filter: total=%d in=%v out=%v, want 1/0/20", body.Total, body.TotalIn, body.TotalOut)
	}

	// Profit distribution is its own type: filterable and counted as money out,
	// never as an expense or money in.
	c, rec = newListContext("/cash-bank/transactions", "?page=1&per_page=10&transaction_type=profit_distribution")
	c.Set("user_id", userID)
	GetCashTransactions(c)
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Total != 1 || body.TotalOut != 15 || body.TotalIn != 0 {
		t.Fatalf("profit_distribution filter: total=%d in=%v out=%v, want 1/0/15", body.Total, body.TotalIn, body.TotalOut)
	}

	// Day-inclusive end_date: the newest row is dated today; end_date=today must include it.
	today := time.Now().Format("2006-01-02")
	c, rec = newListContext("/cash-bank/transactions", "?page=1&per_page=10&end_date="+today)
	c.Set("user_id", userID)
	GetCashTransactions(c)
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Total != 6 {
		t.Fatalf("end_date inclusive: total=%d, want 6", body.Total)
	}

	// Description search.
	c, rec = newListContext("/cash-bank/transactions", "?page=1&per_page=10&search=transfer_in")
	c.Set("user_id", userID)
	GetCashTransactions(c)
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Total != 1 || len(body.Transactions) != 1 {
		t.Fatalf("search: total=%d rows=%d, want 1/1", body.Total, len(body.Transactions))
	}
}

func TestGetCashTransactionsLegacyArrayWithoutParams(t *testing.T) {
	db := setupCashTransactionListTestDB(t)
	userID := uuid.New()
	seedCashTransactions(t, db, userID)

	c, rec := newListContext("/cash-bank/transactions", "")
	c.Set("user_id", userID)
	GetCashTransactions(c)

	var rows []models.CashTransaction
	if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
		t.Fatalf("decode legacy array: %v", err)
	}
	if len(rows) != 6 {
		t.Fatalf("legacy rows = %d, want 6", len(rows))
	}
}
