package controllers

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"truerp/models"
	"truerp/utils"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

func seedParties(t *testing.T, db *gorm.DB, userID uuid.UUID) {
	t.Helper()
	parties := []models.Party{
		{ID: uuid.New(), UserID: userID, Name: "Alpha Traders", PartyType: "customer", Category: "Retail", Phone: "9000000001"},
		{ID: uuid.New(), UserID: userID, Name: "Beta Stores", PartyType: "customer", Category: "Wholesale", Phone: "9000000002"},
		{ID: uuid.New(), UserID: userID, Name: "Gamma Mart", PartyType: "customer", Category: "Retail", Phone: "9000000003"},
		{ID: uuid.New(), UserID: userID, Name: "Delta Shop", PartyType: "customer", Category: "Wholesale", Phone: "9000000004"},
		{ID: uuid.New(), UserID: userID, Name: "Epsilon Bazaar", PartyType: "customer", Category: "Retail", Phone: "9000000005"},
	}
	for _, p := range parties {
		if err := db.Create(&p).Error; err != nil {
			t.Fatalf("create party %s: %v", p.Name, err)
		}
	}
}

type partyListEnvelope struct {
	Parties    []models.Party `json:"parties"`
	Total      int64          `json:"total"`
	Page       int            `json:"page"`
	PerPage    int            `json:"per_page"`
	Categories []string       `json:"categories"`
}

func TestGetPartiesPaginatedReturnsEnvelope(t *testing.T) {
	db := setupPaymentListTestDB(t)
	userID := uuid.New()
	seedParties(t, db, userID)

	c, rec := newListContext("/parties", "?page=2&per_page=2&party_type=customer")
	c.Set("user_id", userID)
	GetParties(c)

	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body partyListEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Total != 5 || body.Page != 2 || body.PerPage != 2 || len(body.Parties) != 2 {
		t.Fatalf("unexpected envelope: total=%d page=%d per_page=%d rows=%d",
			body.Total, body.Page, body.PerPage, len(body.Parties))
	}
	if len(body.Categories) != 2 {
		t.Fatalf("categories = %v, want 2 distinct", body.Categories)
	}
}

func TestGetPartiesPaginatedSearchAndCategory(t *testing.T) {
	db := setupPaymentListTestDB(t)
	userID := uuid.New()
	seedParties(t, db, userID)

	// Search matches one row by name.
	c, rec := newListContext("/parties", "?page=1&per_page=10&party_type=customer&search=gamma")
	c.Set("user_id", userID)
	GetParties(c)

	var body partyListEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Total != 1 || len(body.Parties) != 1 {
		t.Fatalf("name search: total=%d rows=%d, want 1/1", body.Total, len(body.Parties))
	}

	// Category filter narrows to wholesale rows.
	c, rec = newListContext("/parties", "?page=1&per_page=10&party_type=customer&category=Wholesale")
	c.Set("user_id", userID)
	GetParties(c)
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Total != 2 || len(body.Parties) != 2 {
		t.Fatalf("category filter: total=%d rows=%d, want 2/2", body.Total, len(body.Parties))
	}
}

func TestGetPartiesPerPageZeroReturnsAll(t *testing.T) {
	db := setupPaymentListTestDB(t)
	userID := uuid.New()
	seedParties(t, db, userID)

	c, rec := newListContext("/parties", "?page=1&per_page=0&party_type=customer")
	c.Set("user_id", userID)
	GetParties(c)

	var body partyListEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Total != 5 || len(body.Parties) != 5 {
		t.Fatalf("per_page=0: total=%d rows=%d, want 5/5", body.Total, len(body.Parties))
	}
}

func TestGetPartiesLegacyArrayWithoutParams(t *testing.T) {
	db := setupPaymentListTestDB(t)
	userID := uuid.New()
	seedParties(t, db, userID)

	c, rec := newListContext("/parties", "?party_type=customer")
	c.Set("user_id", userID)
	GetParties(c)

	var rows []models.Party
	if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
		t.Fatalf("decode legacy array: %v", err)
	}
	if len(rows) != 5 {
		t.Fatalf("legacy rows = %d, want 5", len(rows))
	}
}

func newJSONPostContext(path string, body interface{}) (*gin.Context, *httptest.ResponseRecorder) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	b, _ := json.Marshal(body)
	c.Request = httptest.NewRequest("POST", path, bytes.NewReader(b))
	c.Request.Header.Set("Content-Type", "application/json")
	return c, rec
}

func countParties(t *testing.T, db *gorm.DB, userID uuid.UUID) int64 {
	t.Helper()
	var n int64
	if err := db.Model(&models.Party{}).Where("user_id = ?", userID).Count(&n).Error; err != nil {
		t.Fatalf("count parties: %v", err)
	}
	return n
}

func TestBulkDeletePartiesAllMatching(t *testing.T) {
	db := setupPaymentListTestDB(t)
	userID := uuid.New()
	seedParties(t, db, userID)
	if err := utils.EnsureDefaultVendor(db, userID); err != nil {
		t.Fatalf("seed default vendor: %v", err)
	}

	// Filtered delete: only the Retail rows go away.
	c, rec := newJSONPostContext("/parties/bulk/delete", map[string]interface{}{
		"all_matching": true,
		"filters":      map[string]string{"category": "Retail", "party_type": "customer"},
	})
	c.Set("user_id", userID)
	BulkDeleteParties(c)
	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if n := countParties(t, db, userID); n != 3 {
		t.Fatalf("remaining = %d, want 3 (2 wholesale + default vendor)", n)
	}

	// Unfiltered delete clears the rest but preserves the default vendor.
	c, rec = newJSONPostContext("/parties/bulk/delete", map[string]interface{}{
		"all_matching": true,
		"filters":      map[string]string{},
	})
	c.Set("user_id", userID)
	BulkDeleteParties(c)
	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if n := countParties(t, db, userID); n != 1 {
		t.Fatalf("remaining = %d, want 1 (default vendor)", n)
	}
	var vendor models.Party
	if err := db.Where("user_id = ?", userID).First(&vendor).Error; err != nil {
		t.Fatalf("find remaining party: %v", err)
	}
	if vendor.Name != utils.DefaultVendorName {
		t.Fatalf("remaining party = %q, want %q", vendor.Name, utils.DefaultVendorName)
	}
}

func TestBulkDeletePartiesByIDs(t *testing.T) {
	db := setupPaymentListTestDB(t)
	userID := uuid.New()
	seedParties(t, db, userID)

	var first models.Party
	if err := db.Where("user_id = ?", userID).First(&first).Error; err != nil {
		t.Fatalf("find party: %v", err)
	}

	c, rec := newJSONPostContext("/parties/bulk/delete", map[string]interface{}{
		"ids": []uuid.UUID{first.ID},
	})
	c.Set("user_id", userID)
	BulkDeleteParties(c)
	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if n := countParties(t, db, userID); n != 4 {
		t.Fatalf("remaining = %d, want 4", n)
	}
}

func TestBulkUpdatePartyCategoryAllMatching(t *testing.T) {
	db := setupPaymentListTestDB(t)
	userID := uuid.New()
	seedParties(t, db, userID)

	c, rec := newJSONPostContext("/parties/bulk/update-category", map[string]interface{}{
		"all_matching": true,
		"category":     "VIP",
		"filters":      map[string]string{"search": "alpha", "party_type": "customer"},
	})
	c.Set("user_id", userID)
	BulkUpdatePartyCategory(c)
	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var updated int64
	if err := db.Model(&models.Party{}).Where("user_id = ? AND category = ?", userID, "VIP").Count(&updated).Error; err != nil {
		t.Fatalf("count updated: %v", err)
	}
	if updated != 1 {
		t.Fatalf("updated = %d, want 1", updated)
	}
}
