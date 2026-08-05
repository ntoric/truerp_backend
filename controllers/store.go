package controllers

import (
	"fmt"
	"net/http"
	"strings"
	"truerp/models"
	"truerp/utils"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

type storeInput struct {
	Name        string `json:"name"`
	Code        string `json:"code"`
	Description string `json:"description"`
	Address     string `json:"address"`
	City        string `json:"city"`
	State       string `json:"state"`
	Pincode     string `json:"pincode"`
	Phone       string `json:"phone"`
	Email       string `json:"email"`
	IsActive    *bool  `json:"is_active"`
}

type storeUserInput struct {
	Name     string `json:"name"`
	Email    string `json:"email"`
	Password string `json:"password"`
	Phone    string `json:"phone"`
	Role     string `json:"role"`
}

func storeValidationError(c *gin.Context, fields map[string]string) {
	c.JSON(http.StatusBadRequest, gin.H{
		"error":  utils.FirstFieldMessage(fields),
		"fields": fields,
	})
}

func storeUserResponse(u models.User) gin.H {
	resp := userPublicResponse(u)
	if u.StoreID != nil {
		resp["store_id"] = u.StoreID
	}
	return resp
}

func createStoreOwnerUser(tx *gorm.DB, storeName string) (models.User, error) {
	ownerID := uuid.New()
	email := fmt.Sprintf("store-owner-%s@internal.local", ownerID.String())
	hashed, err := bcrypt.GenerateFromPassword([]byte(uuid.New().String()), bcrypt.DefaultCost)
	if err != nil {
		return models.User{}, err
	}
	owner := models.User{
		ID:           ownerID,
		Name:         storeName + " Owner",
		Email:        email,
		Password:     string(hashed),
		Role:         "owner",
		IsStoreOwner: true,
		IsActive:     true,
	}
	if err := tx.Create(&owner).Error; err != nil {
		return models.User{}, err
	}
	if err := EnsureDefaultChartOfAccounts(tx, owner.ID); err != nil {
		return models.User{}, err
	}
	business := models.Business{
		ID:     uuid.New(),
		UserID: owner.ID,
		Name:   storeName,
	}
	if err := tx.Create(&business).Error; err != nil {
		return models.User{}, err
	}
	utils.EnsureDefaultRoles(tx, owner.ID)
	if err := utils.EnsureDefaultCategories(tx, owner.ID); err != nil {
		return models.User{}, err
	}
	return owner, nil
}

func createStoreRecord(tx *gorm.DB, form utils.NormalizedStoreForm, ownerID uuid.UUID, isActive *bool) (models.Store, error) {
	baseCode := form.Code
	if baseCode == "" {
		baseCode = utils.NormalizeStoreCode("", form.Name)
	}
	code := utils.UniqueStoreCode(tx, baseCode)
	store := models.Store{
		ID:          uuid.New(),
		Name:        form.Name,
		Code:        code,
		Description: form.Description,
		Address:     form.Address,
		City:        form.City,
		State:       form.State,
		Pincode:     form.Pincode,
		Phone:       form.Phone,
		Email:       form.Email,
		OwnerUserID: ownerID,
		IsActive:    true,
	}
	if isActive != nil {
		store.IsActive = *isActive
	}
	if err := tx.Create(&store).Error; err != nil {
		return models.Store{}, err
	}
	if err := tx.Model(&models.User{}).Where("id = ?", ownerID).Updates(map[string]interface{}{
		"store_id":       store.ID,
		"is_store_owner": true,
	}).Error; err != nil {
		return models.Store{}, err
	}
	return store, nil
}

// EnsureStoresMigrated creates Store rows for existing owner/super_admin businesses.
func EnsureStoresMigrated() {
	var owners []models.User
	if err := utils.DB.Where("role IN ? AND is_store_owner = ?", []string{"owner", "super_admin"}, false).Find(&owners).Error; err != nil {
		return
	}
	for _, owner := range owners {
		var existing models.Store
		if err := utils.DB.Where("owner_user_id = ?", owner.ID).First(&existing).Error; err == nil {
			if owner.StoreID == nil {
				utils.DB.Model(&owner).Update("store_id", existing.ID)
			}
			continue
		}

		var business models.Business
		name := owner.Name + "'s Store"
		if err := utils.DB.Where("user_id = ?", owner.ID).First(&business).Error; err == nil && strings.TrimSpace(business.Name) != "" {
			name = business.Name
		}
		code := utils.UniqueStoreCode(utils.DB, utils.NormalizeStoreCode("", name))
		store := models.Store{
			ID:          uuid.New(),
			Name:        name,
			Code:        code,
			Address:     business.Address,
			City:        business.City,
			State:       business.State,
			Pincode:     business.Pincode,
			Phone:       business.Phone,
			Email:       business.Email,
			OwnerUserID: owner.ID,
			IsActive:    true,
		}
		if err := utils.DB.Create(&store).Error; err != nil {
			continue
		}
		utils.DB.Model(&owner).Updates(map[string]interface{}{
			"store_id": store.ID,
		})

		// Assign orphan non-super-admin users without a store to the first migrated store.
		if utils.IsSuperAdminRole(owner.Role) {
			utils.DB.Model(&models.User{}).
				Where("store_id IS NULL AND role NOT IN ? AND is_store_owner = ?", []string{"owner", "super_admin"}, false).
				Update("store_id", store.ID)
		}
	}
}

func ListStores(c *gin.Context) {
	actor, err := loadActor(c)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "User not found"})
		return
	}
	if !utils.IsSuperAdminRole(actor.Role) {
		c.JSON(http.StatusForbidden, gin.H{"error": "Super admin access required"})
		return
	}

	var stores []models.Store
	if err := utils.DB.Order("name ASC").Find(&stores).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list stores"})
		return
	}
	out := make([]gin.H, 0, len(stores))
	for _, s := range stores {
		item := gin.H(utils.StorePublicJSON(s))
		var userCount int64
		utils.DB.Model(&models.User{}).Where("store_id = ? AND is_store_owner = ?", s.ID, false).Count(&userCount)
		item["user_count"] = userCount
		out = append(out, item)
	}
	c.JSON(http.StatusOK, out)
}

func GetStore(c *gin.Context) {
	actor, err := loadActor(c)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "User not found"})
		return
	}
	if !utils.IsSuperAdminRole(actor.Role) {
		c.JSON(http.StatusForbidden, gin.H{"error": "Super admin access required"})
		return
	}

	storeID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid store id"})
		return
	}
	store, err := utils.FindStoreByID(utils.DB, storeID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Store not found"})
		return
	}
	c.JSON(http.StatusOK, utils.StorePublicJSON(store))
}

func CreateStore(c *gin.Context) {
	actor, err := loadActor(c)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "User not found"})
		return
	}
	if !utils.IsSuperAdminRole(actor.Role) {
		c.JSON(http.StatusForbidden, gin.H{"error": "Super admin access required"})
		return
	}

	var input storeInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request data", "fields": gin.H{"_form": "Invalid request data"}})
		return
	}
	form, fieldErrs := utils.ValidateStoreForm(utils.StoreFormInput{
		Name: input.Name, Code: input.Code, Description: input.Description,
		Address: input.Address, City: input.City, State: input.State,
		Pincode: input.Pincode, Phone: input.Phone, Email: input.Email,
	}, true)
	if len(fieldErrs) > 0 {
		storeValidationError(c, fieldErrs)
		return
	}
	if form.Code != "" {
		var clash models.Store
		if err := utils.DB.Where("code = ?", form.Code).First(&clash).Error; err == nil {
			storeValidationError(c, map[string]string{"code": "Store code already in use"})
			return
		}
	}

	var store models.Store
	err = utils.DB.Transaction(func(tx *gorm.DB) error {
		owner, ownerErr := createStoreOwnerUser(tx, form.Name)
		if ownerErr != nil {
			return ownerErr
		}
		created, createErr := createStoreRecord(tx, form, owner.ID, input.IsActive)
		if createErr != nil {
			return createErr
		}
		store = created
		return nil
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create store"})
		return
	}

	CreateAuditLog(
		actor.ID,
		actor.Name,
		"create",
		"store",
		&store.ID,
		store.Name,
		"Created store",
		c.ClientIP(),
		c.GetHeader("User-Agent"),
		utils.StorePublicJSON(store),
		"success",
		"",
	)

	c.JSON(http.StatusCreated, utils.StorePublicJSON(store))
}

func UpdateStore(c *gin.Context) {
	actor, err := loadActor(c)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "User not found"})
		return
	}
	if !utils.IsSuperAdminRole(actor.Role) {
		c.JSON(http.StatusForbidden, gin.H{"error": "Super admin access required"})
		return
	}

	storeID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid store id"})
		return
	}
	var store models.Store
	if err := utils.DB.First(&store, "id = ?", storeID).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Store not found"})
		return
	}

	var input storeInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request data", "fields": gin.H{"_form": "Invalid request data"}})
		return
	}
	form, fieldErrs := utils.ValidateStoreForm(utils.StoreFormInput{
		Name: input.Name, Code: input.Code, Description: input.Description,
		Address: input.Address, City: input.City, State: input.State,
		Pincode: input.Pincode, Phone: input.Phone, Email: input.Email,
	}, true)
	if len(fieldErrs) > 0 {
		storeValidationError(c, fieldErrs)
		return
	}
	if form.Code != "" {
		var clash models.Store
		if err := utils.DB.Where("code = ? AND id <> ?", form.Code, store.ID).First(&clash).Error; err == nil {
			storeValidationError(c, map[string]string{"code": "Store code already in use"})
			return
		}
	}

	updates := map[string]interface{}{
		"name":        form.Name,
		"description": form.Description,
		"address":     form.Address,
		"city":        form.City,
		"state":       form.State,
		"pincode":     form.Pincode,
		"phone":       form.Phone,
		"email":       form.Email,
	}
	if form.Code != "" {
		updates["code"] = form.Code
	}
	if input.IsActive != nil {
		updates["is_active"] = *input.IsActive
	}

	if err := utils.DB.Model(&store).Updates(updates).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update store"})
		return
	}
	utils.DB.First(&store, "id = ?", storeID)

	// Keep business name in sync for store-scoped settings.
	utils.DB.Model(&models.Business{}).Where("user_id = ?", store.OwnerUserID).Update("name", store.Name)

	c.JSON(http.StatusOK, utils.StorePublicJSON(store))
}

func DeleteStore(c *gin.Context) {
	actor, err := loadActor(c)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "User not found"})
		return
	}
	if !utils.IsSuperAdminRole(actor.Role) {
		c.JSON(http.StatusForbidden, gin.H{"error": "Super admin access required"})
		return
	}

	storeID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid store id"})
		return
	}
	var store models.Store
	if err := utils.DB.First(&store, "id = ?", storeID).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Store not found"})
		return
	}

	var userCount int64
	utils.DB.Model(&models.User{}).Where("store_id = ? AND is_store_owner = ?", store.ID, false).Count(&userCount)
	if userCount > 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Remove or reassign store users before deleting the store"})
		return
	}

	if err := utils.DB.Delete(&store).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete store"})
		return
	}

	CreateAuditLog(
		actor.ID,
		actor.Name,
		"delete",
		"store",
		&store.ID,
		store.Name,
		"Deleted store",
		c.ClientIP(),
		c.GetHeader("User-Agent"),
		nil,
		"success",
		"",
	)

	c.JSON(http.StatusOK, gin.H{"message": "Store deleted successfully"})
}

// ResetStore wipes operational data for a store (scoped to OwnerUserID) while
// preserving the store record, users, roles, and business profile. Defaults
// (chart of accounts, categories) are re-seeded afterward.
func ResetStore(c *gin.Context) {
	actor, err := loadActor(c)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "User not found"})
		return
	}
	if !utils.IsSuperAdminRole(actor.Role) {
		c.JSON(http.StatusForbidden, gin.H{"error": "Super admin access required"})
		return
	}

	storeID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid store id"})
		return
	}
	var store models.Store
	if err := utils.DB.First(&store, "id = ?", storeID).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Store not found"})
		return
	}

	ownerID := store.OwnerUserID
	err = utils.DB.Transaction(func(tx *gorm.DB) error {
		if err := wipeStoreOperationalData(tx, ownerID); err != nil {
			return err
		}
		if err := EnsureDefaultChartOfAccounts(tx, ownerID); err != nil {
			return err
		}
		utils.EnsureDefaultRoles(tx, ownerID)
		return utils.EnsureDefaultCategories(tx, ownerID)
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to reset store data", "details": err.Error()})
		return
	}

	CreateAuditLog(
		actor.ID,
		actor.Name,
		"reset",
		"store",
		&store.ID,
		store.Name,
		"Reset store operational data",
		c.ClientIP(),
		c.GetHeader("User-Agent"),
		nil,
		"success",
		"",
	)

	c.JSON(http.StatusOK, gin.H{
		"message":  "Store data reset successfully",
		"store_id": store.ID,
	})
}

func hardDeleteByUser(tx *gorm.DB, model interface{}, userID uuid.UUID) error {
	return tx.Unscoped().Where("user_id = ?", userID).Delete(model).Error
}

func pluckIDsByUser(tx *gorm.DB, model interface{}, userID uuid.UUID) ([]uuid.UUID, error) {
	var ids []uuid.UUID
	if err := tx.Unscoped().Model(model).Where("user_id = ?", userID).Pluck("id", &ids).Error; err != nil {
		return nil, err
	}
	return ids, nil
}

func hardDeleteByFK(tx *gorm.DB, model interface{}, fkColumn string, parentIDs []uuid.UUID) error {
	if len(parentIDs) == 0 {
		return nil
	}
	return tx.Unscoped().Where(fkColumn+" IN ?", parentIDs).Delete(model).Error
}

// wipeStoreOperationalData permanently deletes tenant operational rows for ownerID.
// Preserves: Store, Users, Business, Roles, UserRoles, DeveloperSettings.
func wipeStoreOperationalData(tx *gorm.DB, ownerID uuid.UUID) error {
	// Child rows without user_id — delete via parent IDs first.
	invoiceIDs, err := pluckIDsByUser(tx, &models.Invoice{}, ownerID)
	if err != nil {
		return err
	}
	if err := hardDeleteByFK(tx, &models.InvoiceItem{}, "invoice_id", invoiceIDs); err != nil {
		return err
	}
	if err := hardDeleteByFK(tx, &models.InvoiceStatusHistory{}, "invoice_id", invoiceIDs); err != nil {
		return err
	}

	expenseIDs, err := pluckIDsByUser(tx, &models.Expense{}, ownerID)
	if err != nil {
		return err
	}
	if err := hardDeleteByFK(tx, &models.ExpenseItem{}, "expense_id", expenseIDs); err != nil {
		return err
	}

	sessionIDs, err := pluckIDsByUser(tx, &models.POSSession{}, ownerID)
	if err != nil {
		return err
	}
	if err := hardDeleteByFK(tx, &models.CashMovement{}, "session_id", sessionIDs); err != nil {
		return err
	}

	transferIDs, err := pluckIDsByUser(tx, &models.StockTransfer{}, ownerID)
	if err != nil {
		return err
	}
	if err := hardDeleteByFK(tx, &models.StockTransferItem{}, "transfer_id", transferIDs); err != nil {
		return err
	}

	poIDs, err := pluckIDsByUser(tx, &models.PurchaseOrder{}, ownerID)
	if err != nil {
		return err
	}
	if err := hardDeleteByFK(tx, &models.PurchaseOrderItem{}, "order_id", poIDs); err != nil {
		return err
	}

	receiptIDs, err := pluckIDsByUser(tx, &models.PurchaseReceipt{}, ownerID)
	if err != nil {
		return err
	}
	if err := hardDeleteByFK(tx, &models.PurchaseReceiptItem{}, "receipt_id", receiptIDs); err != nil {
		return err
	}

	billIDs, err := pluckIDsByUser(tx, &models.PurchaseBill{}, ownerID)
	if err != nil {
		return err
	}
	if err := hardDeleteByFK(tx, &models.PurchaseBillItem{}, "bill_id", billIDs); err != nil {
		return err
	}

	journalIDs, err := pluckIDsByUser(tx, &models.JournalEntry{}, ownerID)
	if err != nil {
		return err
	}
	if err := hardDeleteByFK(tx, &models.JournalEntryLine{}, "entry_id", journalIDs); err != nil {
		return err
	}

	creditNoteIDs, err := pluckIDsByUser(tx, &models.CreditNote{}, ownerID)
	if err != nil {
		return err
	}
	if err := hardDeleteByFK(tx, &models.CreditNoteItem{}, "credit_note_id", creditNoteIDs); err != nil {
		return err
	}

	debitNoteIDs, err := pluckIDsByUser(tx, &models.DebitNote{}, ownerID)
	if err != nil {
		return err
	}
	if err := hardDeleteByFK(tx, &models.DebitNoteItem{}, "debit_note_id", debitNoteIDs); err != nil {
		return err
	}

	salesReturnIDs, err := pluckIDsByUser(tx, &models.SalesReturn{}, ownerID)
	if err != nil {
		return err
	}
	if err := hardDeleteByFK(tx, &models.SalesReturnItem{}, "return_id", salesReturnIDs); err != nil {
		return err
	}

	purchaseReturnIDs, err := pluckIDsByUser(tx, &models.PurchaseReturn{}, ownerID)
	if err != nil {
		return err
	}
	if err := hardDeleteByFK(tx, &models.PurchaseReturnItem{}, "return_id", purchaseReturnIDs); err != nil {
		return err
	}

	challanIDs, err := pluckIDsByUser(tx, &models.DeliveryChallan{}, ownerID)
	if err != nil {
		return err
	}
	if err := hardDeleteByFK(tx, &models.DeliveryChallanItem{}, "challan_id", challanIDs); err != nil {
		return err
	}

	statementIDs, err := pluckIDsByUser(tx, &models.CustomerStatement{}, ownerID)
	if err != nil {
		return err
	}
	if err := hardDeleteByFK(tx, &models.StatementTransaction{}, "statement_id", statementIDs); err != nil {
		return err
	}

	// Parent / user-scoped operational tables (order: dependents before references).
	userScoped := []interface{}{
		&models.InvoiceStatusHistory{},
		&models.Payment{},
		&models.PaymentOut{},
		&models.Invoice{},
		&models.Expense{},
		&models.CreditNote{},
		&models.DebitNote{},
		&models.SalesReturn{},
		&models.PurchaseReturn{},
		&models.DeliveryChallan{},
		&models.PurchaseBill{},
		&models.PurchaseReceipt{},
		&models.PurchaseOrder{},
		&models.CustomerStatement{},
		&models.LoyaltyTransaction{},
		&models.LoyaltySettings{},
		&models.CashTransaction{},
		&models.PaymentMethodAccountMap{},
		&models.BankReconciliation{},
		&models.Ledger{},
		&models.JournalEntry{},
		&models.BankAccount{},
		&models.Account{},
		&models.POSSession{},
		&models.POSDraft{},
		&models.StockTransfer{},
		&models.StockEntry{},
		&models.InventoryStock{},
		&models.Product{},
		&models.Warehouse{},
		&models.Party{},
		&models.Category{},
		&models.ExpenseCategory{},
		&models.StaffAdvancePayment{},
		&models.StaffDeduction{},
		&models.Payroll{},
		&models.Attendance{},
		&models.Staff{},
		&models.InvoiceSettings{},
		&models.PrintSettings{},
		&models.WeighingScaleSettings{},
		&models.Reminder{},
		&models.Notification{},
		&models.NotificationTemplate{},
		&models.NotificationPreference{},
		&models.CAReportSharing{},
		&models.Draft{},
		&models.OfflineQueue{},
		&models.MediaFile{},
		&models.TaxPeriod{},
		&models.InputTaxCredit{},
		&models.GSTFilingStatus{},
		&models.GSTR1Data{},
		&models.GSTR3BData{},
		&models.CustomerPortalAccess{},
		&models.CustomerPortalSettings{},
		&models.SupportTicket{},
		&models.SavedInvoiceTemplate{},
		&models.InvoiceCustomFieldDefinition{},
		&models.AuditLog{},
	}

	for _, model := range userScoped {
		if err := hardDeleteByUser(tx, model, ownerID); err != nil {
			return err
		}
	}

	return nil
}

func ListStoreUsers(c *gin.Context) {
	actor, err := loadActor(c)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "User not found"})
		return
	}
	if !utils.IsSuperAdminRole(actor.Role) {
		c.JSON(http.StatusForbidden, gin.H{"error": "Super admin access required"})
		return
	}

	storeID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid store id"})
		return
	}
	if _, err := utils.FindStoreByID(utils.DB, storeID); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Store not found"})
		return
	}

	var users []models.User
	if err := utils.DB.Where("store_id = ? AND is_store_owner = ?", storeID, false).
		Order("created_at ASC").Find(&users).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list store users"})
		return
	}
	out := make([]gin.H, 0, len(users))
	for _, u := range users {
		out = append(out, storeUserResponse(u))
	}
	c.JSON(http.StatusOK, out)
}

func CreateStoreUser(c *gin.Context) {
	actor, err := loadActor(c)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "User not found"})
		return
	}
	if !utils.IsSuperAdminRole(actor.Role) {
		c.JSON(http.StatusForbidden, gin.H{"error": "Super admin access required"})
		return
	}

	storeID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid store id"})
		return
	}
	store, err := utils.FindStoreByID(utils.DB, storeID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Store not found"})
		return
	}

	var input storeUserInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request data", "fields": gin.H{"_form": "Invalid request data"}})
		return
	}
	form, fieldErrs := utils.ValidateStoreUserForm(utils.StoreUserFormInput{
		Name: input.Name, Email: input.Email, Password: input.Password, Phone: input.Phone, Role: input.Role,
	})
	if len(fieldErrs) > 0 {
		storeValidationError(c, fieldErrs)
		return
	}

	var existing models.User
	if err := utils.DB.Where("email = ?", form.Email).First(&existing).Error; err == nil {
		c.JSON(http.StatusConflict, gin.H{
			"error":  "Email already registered",
			"fields": gin.H{"email": "Email already registered"},
		})
		return
	}

	hashed, err := bcrypt.GenerateFromPassword([]byte(form.Password), bcrypt.DefaultCost)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to hash password"})
		return
	}

	user := models.User{
		ID:       uuid.New(),
		Name:     form.Name,
		Email:    form.Email,
		Password: string(hashed),
		Phone:    form.Phone,
		Role:     form.Role,
		StoreID:  &store.ID,
		IsActive: true,
	}
	if err := utils.DB.Create(&user).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create store user"})
		return
	}

	CreateAuditLog(
		actor.ID,
		actor.Name,
		"create",
		"user",
		&user.ID,
		user.Email,
		"Created store user for "+store.Name,
		c.ClientIP(),
		c.GetHeader("User-Agent"),
		nil,
		"success",
		"",
	)

	c.JSON(http.StatusCreated, storeUserResponse(user))
}

func MyStores(c *gin.Context) {
	actor, err := loadActor(c)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "User not found"})
		return
	}

	if utils.IsSuperAdminRole(actor.Role) {
		var stores []models.Store
		utils.DB.Where("is_active = ?", true).Order("name ASC").Find(&stores)
		out := make([]gin.H, 0, len(stores))
		for _, s := range stores {
			out = append(out, gin.H(utils.StorePublicJSON(s)))
		}
		c.JSON(http.StatusOK, gin.H{"stores": out, "can_switch": true})
		return
	}

	if actor.StoreID == nil {
		c.JSON(http.StatusOK, gin.H{"stores": []gin.H{}, "can_switch": false})
		return
	}
	store, err := utils.FindStoreByID(utils.DB, *actor.StoreID)
	if err != nil || !store.IsActive {
		c.JSON(http.StatusOK, gin.H{"stores": []gin.H{}, "can_switch": false})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"stores":     []gin.H{gin.H(utils.StorePublicJSON(store))},
		"can_switch": false,
	})
}
