package controllers

import (
	"testing"
	"truerp/models"
	"truerp/utils"

	"github.com/google/uuid"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func openPartyImportTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&models.Party{}); err != nil {
		t.Fatalf("migrate parties: %v", err)
	}
	return db
}

func TestImportPartiesRowsDeduplicatesByNameBalanceAndPhone(t *testing.T) {
	db := openPartyImportTestDB(t)
	previousDB := utils.DB
	utils.DB = db
	t.Cleanup(func() { utils.DB = previousDB })

	userID := uuid.New()
	content := []byte(`Name,GST,Address,State,Pincode,Mob No.,Bal.,Party Category
Same Party,,,,,1111111111,100,
Same Party,,,,,1111111111,200,
Same Party,,,,,3333333333,100,
Same Party,,,,,3333333333,100,
Other Party,,,,,4444444444,0,
`)

	imported, errs, err := importPartiesRows(userID, content, nil, "", nil)
	if err != nil {
		t.Fatalf("import parties: %v", err)
	}
	if len(errs) != 0 {
		t.Fatalf("import errors = %v, want none", errs)
	}
	if imported != 4 {
		t.Fatalf("imported = %d, want 4", imported)
	}

	var parties []models.Party
	if err := db.Where("user_id = ? AND name = ?", userID, "Same Party").Order("phone, balance").Find(&parties).Error; err != nil {
		t.Fatalf("load parties: %v", err)
	}
	if len(parties) != 3 ||
		parties[0].Phone != "1111111111" || parties[0].Balance != 100 ||
		parties[1].Phone != "1111111111" || parties[1].Balance != 200 ||
		parties[2].Phone != "3333333333" || parties[2].Balance != 100 {
		t.Fatalf("same-name parties = %+v, want three distinct phone/balance combinations", parties)
	}

	imported, errs, err = importPartiesRows(userID, content, nil, "", nil)
	if err != nil {
		t.Fatalf("re-import parties: %v", err)
	}
	if len(errs) != 0 {
		t.Fatalf("re-import errors = %v, want none", errs)
	}
	if imported != 0 {
		t.Fatalf("re-imported = %d, want 0", imported)
	}

	var count int64
	if err := db.Model(&models.Party{}).Where("user_id = ?", userID).Count(&count).Error; err != nil {
		t.Fatalf("count parties: %v", err)
	}
	if count != 4 {
		t.Fatalf("party count = %d, want 4", count)
	}
}
