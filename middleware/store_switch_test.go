package middleware

import (
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

func openStoreSwitchTestDB(t *testing.T) *gorm.DB {
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

func createSwitchTestStore(t *testing.T, db *gorm.DB, name string) models.Store {
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

func TestAuthRequiredStoreSwitch(t *testing.T) {
	db := openStoreSwitchTestDB(t)
	gin.SetMode(gin.TestMode)

	primary := createSwitchTestStore(t, db, "home")
	extra := createSwitchTestStore(t, db, "branch")
	other := createSwitchTestStore(t, db, "other")
	user := models.User{
		ID:       uuid.New(),
		Name:     "Multi Store Admin",
		Email:    "admin-" + uuid.NewString() + "@test.local",
		Password: "x",
		Role:     "admin",
		StoreID:  &primary.ID,
		IsActive: true,
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := db.Create(&models.UserStore{ID: uuid.New(), UserID: user.ID, StoreID: extra.ID}).Error; err != nil {
		t.Fatalf("assign extra store: %v", err)
	}

	token, err := utils.GenerateToken(user.ID, user.Name, user.Email, user.Role, user.StoreID)
	if err != nil {
		t.Fatalf("token: %v", err)
	}

	run := func(headerStoreID string) (int, uuid.UUID, uuid.UUID) {
		var gotScope, gotStore uuid.UUID
		r := gin.New()
		r.Use(AuthRequired())
		r.GET("/probe", func(c *gin.Context) {
			gotScope = c.MustGet("user_id").(uuid.UUID)
			gotStore = c.MustGet("store_id").(uuid.UUID)
			c.Status(http.StatusOK)
		})
		req := httptest.NewRequest(http.MethodGet, "/probe", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		if headerStoreID != "" {
			req.Header.Set("X-Store-ID", headerStoreID)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w.Code, gotScope, gotStore
	}

	// No header -> primary store scope.
	code, scope, store := run("")
	if code != http.StatusOK || scope != primary.OwnerUserID || store != primary.ID {
		t.Fatalf("default scope wrong: code=%d scope=%v store=%v", code, scope, store)
	}

	// Assigned extra store -> switches into it.
	code, scope, store = run(extra.ID.String())
	if code != http.StatusOK || scope != extra.OwnerUserID || store != extra.ID {
		t.Fatalf("switch scope wrong: code=%d scope=%v store=%v", code, scope, store)
	}

	// Unassigned store -> falls back to primary.
	code, scope, store = run(other.ID.String())
	if code != http.StatusOK || scope != primary.OwnerUserID || store != primary.ID {
		t.Fatalf("unassigned store should fall back: code=%d scope=%v store=%v", code, scope, store)
	}
}
