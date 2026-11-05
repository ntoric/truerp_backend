package controllers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"truerp/models"
	"truerp/utils"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func openUserStoresTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	previousDB := utils.DB
	utils.DB = db
	t.Cleanup(func() { utils.DB = previousDB })
	if err := db.AutoMigrate(&models.User{}, &models.Store{}, &models.UserStore{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func createTestStore(t *testing.T, db *gorm.DB, name string) models.Store {
	t.Helper()
	owner := models.User{
		ID:           uuid.New(),
		Name:         name + " Owner",
		Email:        "owner-" + uuid.NewString() + "@test.local",
		Password:     "x",
		Role:         "owner",
		IsStoreOwner: true,
		IsActive:     true,
	}
	if err := db.Create(&owner).Error; err != nil {
		t.Fatalf("create owner: %v", err)
	}
	store := models.Store{
		ID:          uuid.New(),
		Name:        name,
		Code:        name,
		OwnerUserID: owner.ID,
		IsActive:    true,
	}
	if err := db.Create(&store).Error; err != nil {
		t.Fatalf("create store: %v", err)
	}
	return store
}

func createTestUser(t *testing.T, db *gorm.DB, role string, storeID *uuid.UUID) models.User {
	t.Helper()
	u := models.User{
		ID:       uuid.New(),
		Name:     "Test User",
		Email:    "user-" + uuid.NewString() + "@test.local",
		Password: "x",
		Role:     role,
		StoreID:  storeID,
		IsActive: true,
	}
	if err := db.Create(&u).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	return u
}

func TestAssignedStoreIDs(t *testing.T) {
	db := openUserStoresTestDB(t)
	primary := createTestStore(t, db, "primary")
	extra := createTestStore(t, db, "extra")
	user := createTestUser(t, db, "staff", &primary.ID)

	if err := utils.ReplaceExtraStoreAssignments(db, user.ID, []uuid.UUID{extra.ID, primary.ID}); err != nil {
		t.Fatalf("replace extras: %v", err)
	}

	ids := utils.AssignedStoreIDs(db, user.ID, user.StoreID)
	if len(ids) != 2 || ids[0] != primary.ID || ids[1] != extra.ID {
		t.Fatalf("expected [primary extra], got %v", ids)
	}

	// User with no extras -> only primary.
	lone := createTestUser(t, db, "staff", &extra.ID)
	ids = utils.AssignedStoreIDs(db, lone.ID, lone.StoreID)
	if len(ids) != 1 || ids[0] != extra.ID {
		t.Fatalf("expected [extra], got %v", ids)
	}
}

func TestMyStoresMultiStore(t *testing.T) {
	db := openUserStoresTestDB(t)
	primary := createTestStore(t, db, "a-store")
	extra := createTestStore(t, db, "b-store")
	user := createTestUser(t, db, "staff", &primary.ID)
	if err := db.Create(&models.UserStore{ID: uuid.New(), UserID: user.ID, StoreID: extra.ID}).Error; err != nil {
		t.Fatalf("assign extra store: %v", err)
	}

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("actor_user_id", user.ID)
	MyStores(c)

	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Stores    []struct{ ID string `json:"id"` } `json:"stores"`
		CanSwitch bool                            `json:"can_switch"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Stores) != 2 || !resp.CanSwitch {
		t.Fatalf("expected 2 stores and can_switch, got %+v", resp)
	}

	// Single-store user -> can_switch false.
	lone := createTestUser(t, db, "staff", &primary.ID)
	w2 := httptest.NewRecorder()
	c2, _ := gin.CreateTestContext(w2)
	c2.Set("actor_user_id", lone.ID)
	MyStores(c2)
	var resp2 struct {
		Stores    []struct{ ID string } `json:"stores"`
		CanSwitch bool                  `json:"can_switch"`
	}
	if err := json.Unmarshal(w2.Body.Bytes(), &resp2); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp2.Stores) != 1 || resp2.CanSwitch {
		t.Fatalf("expected 1 store without can_switch, got %+v", resp2)
	}
}

func TestUpdateBusinessUserStoreIDs(t *testing.T) {
	db := openUserStoresTestDB(t)
	gin.SetMode(gin.TestMode)

	primary := createTestStore(t, db, "home")
	extra := createTestStore(t, db, "branch")
	admin := createTestUser(t, db, "super_admin", &primary.ID)
	target := createTestUser(t, db, "staff", &primary.ID)

	payload := map[string]interface{}{"store_ids": []string{primary.ID.String(), extra.ID.String()}}
	body, _ := json.Marshal(payload)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("actor_user_id", admin.ID)
	c.Params = gin.Params{{Key: "id", Value: target.ID.String()}}
	req := httptest.NewRequest(http.MethodPut, "/users/"+target.ID.String(), bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	c.Request = req

	UpdateBusinessUser(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}

	var updated models.User
	db.First(&updated, "id = ?", target.ID)
	if updated.StoreID == nil || *updated.StoreID != primary.ID {
		t.Fatalf("primary store not preserved: %+v", updated.StoreID)
	}
	var extras []uuid.UUID
	db.Model(&models.UserStore{}).Where("user_id = ?", target.ID).Pluck("store_id", &extras)
	if len(extras) != 1 || extras[0] != extra.ID {
		t.Fatalf("expected extras=[extra], got %v", extras)
	}

	// Replace with a different set: extras swap, primary moves when dropped.
	payload = map[string]interface{}{"store_ids": []string{extra.ID.String()}}
	body, _ = json.Marshal(payload)
	w = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(w)
	c.Set("actor_user_id", admin.ID)
	c.Params = gin.Params{{Key: "id", Value: target.ID.String()}}
	req = httptest.NewRequest(http.MethodPut, "/users/"+target.ID.String(), bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	c.Request = req

	UpdateBusinessUser(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	db.First(&updated, "id = ?", target.ID)
	if updated.StoreID == nil || *updated.StoreID != extra.ID {
		t.Fatalf("primary should move to remaining store: %+v", updated.StoreID)
	}
	extras = nil
	db.Model(&models.UserStore{}).Where("user_id = ?", target.ID).Pluck("store_id", &extras)
	if len(extras) != 0 {
		t.Fatalf("expected no extras, got %v", extras)
	}
}
