package controllers

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"truerp/models"
	"truerp/utils"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

// -----------------------------------------------------------------------------
// Generic CSV importers for the Data Migration tab.
//
// These importers accept a plain CSV (header row first, no myBillBook preamble)
// and create rows one-per-line. They are idempotent: duplicates are skipped by
// name / email / number so re-running with the same file is safe.
//
// All handlers read the "file" multipart field and return:
//   { "imported": <int>, "errors": [string, ...] }
// -----------------------------------------------------------------------------

// readPlainCSV reads an uploaded CSV file and returns (header, rows). The first
// non-empty line is treated as the header. BOM and CRLF are normalized.
func readPlainCSV(content []byte) (header []string, rows [][]string, err error) {
	content = stripBOM(content)
	content = normalizeCRLF(content)
	reader := csv.NewReader(strings.NewReader(string(content)))
	reader.FieldsPerRecord = -1
	reader.LazyQuotes = true
	all, rerr := reader.ReadAll()
	if rerr != nil {
		return nil, nil, fmt.Errorf("failed to parse CSV: %w", rerr)
	}
	if len(all) == 0 {
		return nil, nil, errors.New("CSV file is empty")
	}
	header = all[0]
	for _, row := range all[1:] {
		if len(row) == 0 || strings.TrimSpace(strings.Join(row, "")) == "" {
			continue
		}
		rows = append(rows, row)
	}
	return header, rows, nil
}

func stripBOM(content []byte) []byte {
	if len(content) >= 3 && content[0] == 0xEF && content[1] == 0xBB && content[2] == 0xBF {
		return content[3:]
	}
	return content
}

func normalizeCRLF(content []byte) []byte {
	return []byte(strings.ReplaceAll(string(content), "\r\n", "\n"))
}

// csvVal looks up a column by header name (case-insensitive, trimmed).
func csvVal(record, headers []string, key string) string {
	for i, h := range headers {
		if strings.EqualFold(strings.TrimSpace(h), key) && i < len(record) {
			return strings.TrimSpace(record[i])
		}
	}
	return ""
}

func csvFirst(record, headers []string, keys ...string) string {
	for _, k := range keys {
		if v := csvVal(record, headers, k); v != "" {
			return v
		}
	}
	return ""
}

// importFile reads the "file" multipart field and returns its full content.
func importFile(c *gin.Context) ([]byte, error) {
	file, err := c.FormFile("file")
	if err != nil {
		return nil, errors.New("no file uploaded")
	}
	src, err := file.Open()
	if err != nil {
		return nil, errors.New("failed to open uploaded file")
	}
	defer src.Close()
	content, err := io.ReadAll(src)
	if err != nil {
		return nil, errors.New("failed to read uploaded file")
	}
	return content, nil
}

// -----------------------------------------------------------------------------
// Product categories  —  POST /api/v1/migration/categories/import/csv
//
// Header: Name, Description, Parent Category, Is Active
// "Parent Category" is matched by name (case-insensitive) within this user's
// categories; leave blank for a top-level category.
// -----------------------------------------------------------------------------

func ImportCategoriesCSV(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	content, err := importFile(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	header, rows, err := readPlainCSV(content)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	imported := 0
	var errs []string

	// First pass: create all top-level categories so parent lookups in the
	// second pass can resolve them.
	nameToID := map[string]uuid.UUID{}
	for i, row := range rows {
		rowNum := i + 1
		name := strings.TrimSpace(csvVal(row, header, "Name"))
		if name == "" {
			errs = append(errs, fmt.Sprintf("Row %d: Name is required", rowNum))
			continue
		}

		var existing models.Category
		if err := utils.DB.Where("user_id = ? AND name = ?", userID, name).First(&existing).Error; err == nil {
			nameToID[strings.ToLower(name)] = existing.ID
			continue
		}

		parentName := strings.TrimSpace(csvFirst(row, header, "Parent Category", "Parent"))
		isActive := csvFirst(row, header, "Is Active", "IsActive")
		active := isActive == "" || parseBool(isActive)

		cat := models.Category{
			ID:          uuid.New(),
			UserID:      userID,
			Name:        name,
			Description: csvVal(row, header, "Description"),
			IsActive:    active,
		}
		if parentName != "" {
			if pid, ok := nameToID[strings.ToLower(parentName)]; ok {
				cat.ParentID = &pid
			} else {
				var parent models.Category
				if err := utils.DB.Where("user_id = ? AND name = ?", userID, parentName).First(&parent).Error; err == nil {
					cat.ParentID = &parent.ID
					nameToID[strings.ToLower(parentName)] = parent.ID
				}
			}
		}

		if err := utils.DB.Create(&cat).Error; err != nil {
			errs = append(errs, fmt.Sprintf("Row %d (%s): %v", rowNum, name, err))
			continue
		}
		nameToID[strings.ToLower(name)] = cat.ID
		imported++
	}

	c.JSON(http.StatusOK, gin.H{"imported": imported, "errors": errs})
}

// -----------------------------------------------------------------------------
// Expense categories  —  POST /api/v1/migration/expense-categories/import/csv
//
// Header: Name, Description, Is Active
// -----------------------------------------------------------------------------

func ImportExpenseCategoriesCSV(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	content, err := importFile(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	header, rows, err := readPlainCSV(content)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	imported := 0
	var errs []string
	for i, row := range rows {
		rowNum := i + 1
		name := strings.TrimSpace(csvVal(row, header, "Name"))
		if name == "" {
			errs = append(errs, fmt.Sprintf("Row %d: Name is required", rowNum))
			continue
		}

		var existing models.ExpenseCategory
		if err := utils.DB.Where("user_id = ? AND name = ?", userID, name).First(&existing).Error; err == nil {
			continue
		}

		isActive := csvFirst(row, header, "Is Active", "IsActive")
		active := isActive == "" || parseBool(isActive)

		cat := models.ExpenseCategory{
			ID:          uuid.New(),
			UserID:      userID,
			Name:        name,
			Description: csvVal(row, header, "Description"),
			IsActive:    active,
		}
		if err := utils.DB.Create(&cat).Error; err != nil {
			errs = append(errs, fmt.Sprintf("Row %d (%s): %v", rowNum, name, err))
			continue
		}
		// Best-effort accounting setup (matches CreateExpenseCategory handler).
		_, _ = utils.EnsureExpenseCategoryAccount(utils.DB, userID, cat.Name)
		imported++
	}

	c.JSON(http.StatusOK, gin.H{"imported": imported, "errors": errs})
}

// -----------------------------------------------------------------------------
// Inventory (opening stock)  —  POST /api/v1/migration/inventory/import/csv
//
// Header: Product Name, Product Code (SKU/Item Code), Warehouse, Quantity,
//         Cost Price, Batch No, Mfg Date, Exp Date
//
// Creates an "opening" StockEntry and upserts the InventoryStock row for the
// product + warehouse. Products and warehouses are matched by name/code; the
// default warehouse is used when "Warehouse" is blank.
// -----------------------------------------------------------------------------

func ImportInventoryCSV(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	content, err := importFile(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	header, rows, err := readPlainCSV(content)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	defaultWH := resolveDefaultWarehouseID(userID)

	imported := 0
	var errs []string
	for i, row := range rows {
		rowNum := i + 1
		productName := strings.TrimSpace(csvFirst(row, header, "Product Name", "Name"))
		productCode := strings.TrimSpace(csvFirst(row, header, "Product Code", "SKU", "Item Code", "ItemCode"))
		if productName == "" && productCode == "" {
			errs = append(errs, fmt.Sprintf("Row %d: Product Name or Product Code is required", rowNum))
			continue
		}

		// Resolve product by name, then by code.
		var product models.Product
		perr := utils.DB.Where("user_id = ? AND name = ?", userID, productName).First(&product).Error
		if perr != nil && productCode != "" {
			perr = utils.DB.Where("user_id = ? AND (sku = ? OR item_code = ?)", userID, productCode, productCode).First(&product).Error
		}
		if perr != nil {
			errs = append(errs, fmt.Sprintf("Row %d: product not found (%s / %s)", rowNum, productName, productCode))
			continue
		}

		// Resolve warehouse.
		whName := strings.TrimSpace(csvVal(row, header, "Warehouse"))
		whID := defaultWH
		if whName != "" {
			var wh models.Warehouse
			if err := utils.DB.Where("user_id = ? AND name = ?", userID, whName).First(&wh).Error; err == nil {
				whID = wh.ID
			} else if err := utils.DB.Where("user_id = ? AND code = ?", userID, whName).First(&wh).Error; err == nil {
				whID = wh.ID
			}
		}
		if whID == uuid.Nil {
			errs = append(errs, fmt.Sprintf("Row %d (%s): no warehouse available", rowNum, product.Name))
			continue
		}

		qty := parseFloat(csvFirst(row, header, "Quantity", "Qty"))
		if qty == 0 {
			errs = append(errs, fmt.Sprintf("Row %d (%s): quantity is required", rowNum, product.Name))
			continue
		}
		cost := parseFloat(csvFirst(row, header, "Cost Price", "Cost"))
		batchNo := strings.TrimSpace(csvFirst(row, header, "Batch No", "Batch"))

		var mfgDate, expDate *time.Time
		if v := csvFirst(row, header, "Mfg Date", "MfgDate"); v != "" {
			if t, e := parseImportDate(v); e == nil {
				mfgDate = &t
			}
		}
		if v := csvFirst(row, header, "Exp Date", "ExpDate"); v != "" {
			if t, e := parseImportDate(v); e == nil {
				expDate = &t
			}
		}

		tx := utils.DB.Begin()

		// Upsert inventory stock row.
		var stock models.InventoryStock
		if err := tx.Where("user_id = ? AND product_id = ? AND outlet_id = ? AND batch_no = ?",
			userID, product.ID, whID, batchNo).First(&stock).Error; err == nil {
			stock.Quantity += qty
			stock.InitialQuantity += qty
			stock.AvailableQty += qty
			if cost > 0 && stock.AverageCost == 0 {
				stock.AverageCost = cost
			}
			if err := tx.Save(&stock).Error; err != nil {
				tx.Rollback()
				errs = append(errs, fmt.Sprintf("Row %d (%s): %v", rowNum, product.Name, err))
				continue
			}
		} else if errors.Is(err, gorm.ErrRecordNotFound) {
			stock = models.InventoryStock{
				ID:              uuid.New(),
				UserID:          userID,
				ProductID:       product.ID,
				OutletID:        whID,
				BatchNo:         batchNo,
				MfgDate:         mfgDate,
				ExpDate:         expDate,
				Quantity:        qty,
				InitialQuantity: qty,
				AvailableQty:    qty,
				AverageCost:     cost,
				LastUpdated:     time.Now(),
			}
			if err := tx.Create(&stock).Error; err != nil {
				tx.Rollback()
				errs = append(errs, fmt.Sprintf("Row %d (%s): %v", rowNum, product.Name, err))
				continue
			}
		} else {
			tx.Rollback()
			errs = append(errs, fmt.Sprintf("Row %d (%s): %v", rowNum, product.Name, err))
			continue
		}

		entry := models.StockEntry{
			ID:             uuid.New(),
			UserID:         userID,
			ProductID:      &product.ID,
			ItemName:       product.Name,
			OutletID:       whID,
			EntryType:      "opening",
			Quantity:       qty,
			BalanceQty:      stock.Quantity,
			CostPrice:       cost,
			BatchNo:         batchNo,
			ItemCode:        product.ItemCode,
			MfgDate:         mfgDate,
			ExpDate:         expDate,
			ApprovalStatus: "approved",
			EntryDate:      time.Now(),
		}
		if err := tx.Create(&entry).Error; err != nil {
			tx.Rollback()
			errs = append(errs, fmt.Sprintf("Row %d (%s): %v", rowNum, product.Name, err))
			continue
		}

		if err := tx.Commit().Error; err != nil {
			errs = append(errs, fmt.Sprintf("Row %d (%s): %v", rowNum, product.Name, err))
			continue
		}
		imported++
	}

	c.JSON(http.StatusOK, gin.H{"imported": imported, "errors": errs})
}

// -----------------------------------------------------------------------------
// Cash & Bank — bank accounts  —  POST /api/v1/migration/bank-accounts/import/csv
//
// Header: Account Name, Account Number, Bank Name, IFSC Code, Account Type,
//         Opening Balance, Notes
// The first account created for the user is marked primary (matches the
// CreateBankAccount handler behavior).
// -----------------------------------------------------------------------------

func ImportBankAccountsCSV(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	content, err := importFile(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	header, rows, err := readPlainCSV(content)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Optional default account type applied when the CSV cell is blank.
	defaultAccountType := strings.TrimSpace(c.PostForm("default_account_type"))

	imported := 0
	var errs []string
	for i, row := range rows {
		rowNum := i + 1
		accountName := strings.TrimSpace(csvFirst(row, header, "Account Name", "AccountName"))
		accountNumber := strings.TrimSpace(csvFirst(row, header, "Account Number", "AccountNumber"))
		bankName := strings.TrimSpace(csvFirst(row, header, "Bank Name", "BankName"))
		if accountName == "" || accountNumber == "" || bankName == "" {
			errs = append(errs, fmt.Sprintf("Row %d: Account Name, Account Number and Bank Name are required", rowNum))
			continue
		}

		// Skip duplicates by account number.
		var existing models.BankAccount
		if err := utils.DB.Where("user_id = ? AND account_number = ?", userID, accountNumber).First(&existing).Error; err == nil {
			continue
		}

		accountType := strings.TrimSpace(csvFirst(row, header, "Account Type", "AccountType"))
		if accountType == "" {
			if defaultAccountType != "" {
				accountType = defaultAccountType
			} else {
				accountType = "savings"
			}
		}
		opening := parseFloat(csvFirst(row, header, "Opening Balance", "OpeningBalance"))

		account := models.BankAccount{
			ID:              uuid.New(),
			UserID:          userID,
			AccountName:     accountName,
			AccountNumber:   accountNumber,
			BankName:        bankName,
			IFSCCode:        strings.TrimSpace(csvFirst(row, header, "IFSC Code", "IFSC")),
			AccountType:     accountType,
			OpeningBalance: opening,
			Balance:         opening,
			IsActive:        true,
			Notes:           csvVal(row, header, "Notes"),
		}

		if err := utils.DB.Create(&account).Error; err != nil {
			errs = append(errs, fmt.Sprintf("Row %d (%s): %v", rowNum, accountName, err))
			continue
		}

		// First account becomes primary.
		var count int64
		utils.DB.Model(&models.BankAccount{}).Where("user_id = ?", userID).Count(&count)
		if count == 1 {
			utils.DB.Model(&account).Update("is_primary", true)
		}
		imported++
	}

	c.JSON(http.StatusOK, gin.H{"imported": imported, "errors": errs})
}

// -----------------------------------------------------------------------------
// Cash & Bank — cash transactions  —  POST /api/v1/migration/cash-transactions/import/csv
//
// Header: Date, Type, Account Name, Amount, Description, Reference
// Type: add | reduce  (add = money in, reduce = money out)
// "Account Name" is matched to a BankAccount by name; leave blank for cash in
// hand (no account linkage).
// -----------------------------------------------------------------------------

func ImportCashTransactionsCSV(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	content, err := importFile(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	header, rows, err := readPlainCSV(content)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	imported := 0
	var errs []string
	for i, row := range rows {
		rowNum := i + 1
		dateStr := strings.TrimSpace(csvVal(row, header, "Date"))
		date, derr := parseImportDate(dateStr)
		if derr != nil {
			errs = append(errs, fmt.Sprintf("Row %d: invalid date %q", rowNum, dateStr))
			continue
		}

		typ := strings.ToLower(strings.TrimSpace(csvFirst(row, header, "Type", "Transaction Type")))
		if typ != "add" && typ != "reduce" {
			errs = append(errs, fmt.Sprintf("Row %d: Type must be 'add' or 'reduce'", rowNum))
			continue
		}

		amount := parseFloat(csvVal(row, header, "Amount"))
		if amount <= 0 {
			errs = append(errs, fmt.Sprintf("Row %d: Amount must be greater than 0", rowNum))
			continue
		}

		var accountID *uuid.UUID
		accountName := strings.TrimSpace(csvFirst(row, header, "Account Name", "AccountName"))
		if accountName != "" {
			var account models.BankAccount
			if err := utils.DB.Where("user_id = ? AND account_name = ?", userID, accountName).First(&account).Error; err == nil {
				accountID = &account.ID
				// Update balance.
				if typ == "add" {
					account.Balance += amount
				} else {
					account.Balance -= amount
				}
				utils.DB.Save(&account)
			}
		}

		txn := models.CashTransaction{
			ID:              uuid.New(),
			UserID:          userID,
			AccountID:       accountID,
			TransactionType: typ,
			Amount:          amount,
			Date:            date,
			Description:     csvVal(row, header, "Description"),
			Reference:       csvVal(row, header, "Reference"),
		}
		if err := utils.DB.Create(&txn).Error; err != nil {
			errs = append(errs, fmt.Sprintf("Row %d: %v", rowNum, err))
			continue
		}
		imported++
	}

	c.JSON(http.StatusOK, gin.H{"imported": imported, "errors": errs})
}

// -----------------------------------------------------------------------------
// Users (business users)  —  POST /api/v1/migration/users/import/csv
//
// Header: Name, Email, Password, Phone, Role
// Role: admin | staff (super admins can also create owner). Passwords are
// hashed with bcrypt. Duplicates by email are skipped.
// -----------------------------------------------------------------------------

func ImportUsersCSV(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	content, err := importFile(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	header, rows, err := readPlainCSV(content)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// The new users are attached to the same store as the importing user.
	var storeID *uuid.UUID
	var actor models.User
	if err := utils.DB.First(&actor, "id = ?", userID).Error; err == nil && actor.StoreID != nil {
		storeID = actor.StoreID
	}

	// Optional default role applied when the CSV cell is blank.
	defaultRole := strings.TrimSpace(strings.ToLower(c.PostForm("default_role")))

	imported := 0
	var errs []string
	for i, row := range rows {
		rowNum := i + 1
		name := strings.TrimSpace(csvVal(row, header, "Name"))
		email := strings.TrimSpace(strings.ToLower(csvVal(row, header, "Email")))
		password := strings.TrimSpace(csvVal(row, header, "Password"))
		if name == "" || email == "" || password == "" {
			errs = append(errs, fmt.Sprintf("Row %d: Name, Email and Password are required", rowNum))
			continue
		}

		// Skip duplicates by email.
		var existing models.User
		if err := utils.DB.Where("email = ?", email).First(&existing).Error; err == nil {
			continue
		}

		role := strings.TrimSpace(strings.ToLower(csvVal(row, header, "Role")))
		if role == "" {
			if defaultRole != "" {
				role = defaultRole
			} else {
				role = "staff"
			}
		}

		hashed, herr := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		if herr != nil {
			errs = append(errs, fmt.Sprintf("Row %d (%s): failed to hash password", rowNum, email))
			continue
		}

		user := models.User{
			ID:       uuid.New(),
			Name:     name,
			Email:    email,
			Password: string(hashed),
			Phone:    csvVal(row, header, "Phone"),
			Role:     role,
			StoreID:  storeID,
			IsActive: true,
		}
		if err := utils.DB.Create(&user).Error; err != nil {
			errs = append(errs, fmt.Sprintf("Row %d (%s): %v", rowNum, email, err))
			continue
		}
		imported++
	}

	c.JSON(http.StatusOK, gin.H{"imported": imported, "errors": errs})
}

// -----------------------------------------------------------------------------
// HR & Payroll — staff  —  POST /api/v1/migration/staff/import/csv
//
// Header: Name, Phone, Email, Designation, Department, Joining Date, Salary,
//         Salary Type, Bank Name, Account Number, IFSC Code, Aadhar Number,
//         PAN Number, Notes
// Duplicates are skipped by name + phone.
// -----------------------------------------------------------------------------

func ImportStaffCSV(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	content, err := importFile(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	header, rows, err := readPlainCSV(content)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Optional default salary type applied when the CSV cell is blank.
	defaultSalaryType := strings.TrimSpace(c.PostForm("default_salary_type"))

	imported := 0
	var errs []string
	for i, row := range rows {
		rowNum := i + 1
		name := strings.TrimSpace(csvVal(row, header, "Name"))
		if name == "" {
			errs = append(errs, fmt.Sprintf("Row %d: Name is required", rowNum))
			continue
		}
		phone := strings.TrimSpace(csvVal(row, header, "Phone"))

		// Skip duplicates by name + phone (phone may be empty).
		if phone != "" {
			var existing models.Staff
			if err := utils.DB.Where("user_id = ? AND name = ? AND phone = ?", userID, name, phone).First(&existing).Error; err == nil {
				continue
			}
		} else {
			var existing models.Staff
			if err := utils.DB.Where("user_id = ? AND name = ? AND (phone = '' OR phone IS NULL)", userID, name).First(&existing).Error; err == nil {
				continue
			}
		}

		salaryType := strings.TrimSpace(csvFirst(row, header, "Salary Type", "SalaryType"))
		if salaryType == "" {
			if defaultSalaryType != "" {
				salaryType = defaultSalaryType
			} else {
				salaryType = "monthly"
			}
		}

		var joiningDate *time.Time
		if v := csvFirst(row, header, "Joining Date", "JoiningDate"); v != "" {
			if t, e := parseImportDate(v); e == nil {
				joiningDate = &t
			}
		}

		staff := models.Staff{
			ID:            uuid.New(),
			UserID:        userID,
			Name:          name,
			Phone:         phone,
			Email:         strings.TrimSpace(csvVal(row, header, "Email")),
			Address:       csvVal(row, header, "Address"),
			Designation:   csvVal(row, header, "Designation"),
			Department:    csvVal(row, header, "Department"),
			JoiningDate:   joiningDate,
			Salary:        parseFloat(csvVal(row, header, "Salary")),
			SalaryType:    salaryType,
			BankName:      csvFirst(row, header, "Bank Name", "BankName"),
			AccountNumber: csvFirst(row, header, "Account Number", "AccountNumber"),
			IFSCCode:      csvFirst(row, header, "IFSC Code", "IFSC"),
			AadharNumber:  csvFirst(row, header, "Aadhar Number", "AadharNumber"),
			PANNumber:     csvFirst(row, header, "PAN Number", "PANNumber"),
			IsActive:      true,
			Notes:         csvVal(row, header, "Notes"),
		}
		if err := utils.DB.Create(&staff).Error; err != nil {
			errs = append(errs, fmt.Sprintf("Row %d (%s): %v", rowNum, name, err))
			continue
		}
		imported++
	}

	c.JSON(http.StatusOK, gin.H{"imported": imported, "errors": errs})
}

// -----------------------------------------------------------------------------
// Stock Summary Report  —  POST /api/v1/migration/stock-summary/import/csv
//
// Parses a myBillBook-style "Stock Summary Report" CSV (with a preamble before
// the header row) and:
//
//  1. Creates product categories from the "Item Category Name" column (skips
//     duplicates by name).
//  2. Creates products from the "Name" column (skips duplicates by name).
//     PLU is auto-assigned ascending from 1; SKU is generated from the name.
//     Item Code, purchase/sale price, MRP, and unit are populated from the
//     first row seen for each product.
//  3. Creates/updates InventoryStock rows per batch. Each unique (product,
//     batch_no) pair becomes one stock row in the default warehouse. Stock
//     quantity is set from the "Stock Quantity" column (which includes the
//     unit, e.g. "33.0 PCS").
//
// Returns:
//   {
//     "imported": <int>,            // rows processed
//     "categories_created": <int>,
//     "products_created": <int>,
//     "stock_updated": <int>,
//     "errors": [string, ...]
//   }
// -----------------------------------------------------------------------------

func ImportStockSummaryCSV(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	content, err := importFile(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// The stock summary report has a preamble (company name, phone, report
	// title, date, total stock value) before the CSV header. We need to find
	// the header row (the one starting with "Name").
	header, rows, err := parseStockSummaryCSV(content)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	defaultWH := resolveDefaultWarehouseID(userID)

	// Optional default category applied when the CSV cell is blank.
	defaultCategory := strings.TrimSpace(c.PostForm("default_category"))

	categoriesCreated := 0
	productsCreated := 0
	stockUpdated := 0
	imported := 0
	var errs []string

	// Caches to avoid repeated DB lookups within this import.
	categoryCache := map[string]uuid.UUID{}    // lower(name) -> ID
	productCache := map[string]*models.Product{} // lower(name) -> product

	// Resolve the default category up front so it can be reused for every
	// row that has no category in the CSV.
	if defaultCategory != "" {
		var cat models.Category
		if err := utils.DB.Where("user_id = ? AND name = ?", userID, defaultCategory).First(&cat).Error; err == nil {
			categoryCache[strings.ToLower(defaultCategory)] = cat.ID
		} else {
			newCat := models.Category{
				ID:       uuid.New(),
				UserID:   userID,
				Name:     defaultCategory,
				IsActive: true,
			}
			if err := utils.DB.Create(&newCat).Error; err != nil {
				errs = append(errs, fmt.Sprintf("failed to create default category %q: %v", defaultCategory, err))
			} else {
				categoryCache[strings.ToLower(defaultCategory)] = newCat.ID
				categoriesCreated++
			}
		}
	}

	for i, row := range rows {
		rowNum := i + 1
		name := strings.TrimSpace(csvFirst(row, header, "Name", "Product Name", "Item Name"))
		if name == "" {
			errs = append(errs, fmt.Sprintf("Row %d: Name is required", rowNum))
			continue
		}

		// --- 1. Resolve / create category ---
		categoryName := strings.TrimSpace(csvFirst(row, header, "Item Category Name", "Category Name", "Category"))
		if categoryName == "" {
			categoryName = defaultCategory
		}
		if categoryName != "" {
			if _, ok := categoryCache[strings.ToLower(categoryName)]; !ok {
				var cat models.Category
				if err := utils.DB.Where("user_id = ? AND name = ?", userID, categoryName).First(&cat).Error; err == nil {
					categoryCache[strings.ToLower(categoryName)] = cat.ID
				} else {
					newCat := models.Category{
						ID:       uuid.New(),
						UserID:   userID,
						Name:     categoryName,
						IsActive: true,
					}
					if err := utils.DB.Create(&newCat).Error; err != nil {
						errs = append(errs, fmt.Sprintf("Row %d: failed to create category %q: %v", rowNum, categoryName, err))
					} else {
						categoryCache[strings.ToLower(categoryName)] = newCat.ID
						categoriesCreated++
					}
				}
			}
		}

		// --- 2. Resolve / create product ---
		// Read the batch number early so we can enable batching on the product.
		batchNo := strings.TrimSpace(csvFirst(row, header, "Batch No.", "Batch No", "Batch"))
		hasBatch := batchNo != ""

		product, ok := productCache[strings.ToLower(name)]
		if !ok {
			var existing models.Product
			if err := utils.DB.Where("user_id = ? AND name = ?", userID, name).First(&existing).Error; err == nil {
				product = &existing
				productCache[strings.ToLower(name)] = product
			} else {
				// Parse values from the first row we see for this product.
				itemCode := strings.TrimSpace(csvFirst(row, header, "Item Code", "ItemCode"))
				purchasePrice := parseFloat(csvFirst(row, header, "Purchase Price", "PurchasePrice"))
				sellingPrice := parseFloat(csvFirst(row, header, "Selling Price", "Sale Price", "SalePrice"))
				mrp := parseFloat(csvFirst(row, header, "MRP"))
				if mrp == 0 {
					mrp = sellingPrice
				}
				unit := parseStockUnit(csvFirst(row, header, "Stock Quantity", "StockQuantity"))
				if unit == "" {
					unit = "PCS"
				}

				// Auto-generate PLU and SKU.
				plu, pluErr := utils.NextProductPLU(userID)
				if pluErr != nil {
					errs = append(errs, fmt.Sprintf("Row %d (%s): failed to generate PLU: %v", rowNum, name, pluErr))
					continue
				}
				sku := utils.GenerateUniqueProductSKU(name)

				newProduct := models.Product{
					ID:            uuid.New(),
					UserID:        userID,
					Name:          name,
					SKU:           sku,
					PLU:           plu,
					ItemCode:      itemCode,
					Category:      categoryName,
					PurchasePrice: purchasePrice,
					SalePrice:     sellingPrice,
					MRP:           mrp,
					Unit:          unit,
					ItemType:      "product",
					EnableBatching: hasBatch,
					IsActive:      true,
				}
				if err := utils.DB.Create(&newProduct).Error; err != nil {
					errs = append(errs, fmt.Sprintf("Row %d (%s): failed to create product: %v", rowNum, name, err))
					continue
				}
				product = &newProduct
				productCache[strings.ToLower(name)] = product
				productsCreated++
			}
		}

		// Enable batching on existing products that now have a batch number
		// but were created without it (e.g. from a previous non-batch import).
		if hasBatch && product != nil && !product.EnableBatching {
			product.EnableBatching = true
			if err := utils.DB.Model(product).Update("enable_batching", true).Error; err != nil {
				errs = append(errs, fmt.Sprintf("Row %d (%s): failed to enable batching: %v", rowNum, name, err))
			}
		}

		// Detect the unit from the stock quantity suffix (e.g. "33.0 PCS" →
		// "PCS") and sync it onto the product. This ensures the product's unit
		// matches the actual stock quantity unit from the report, even for
		// products that already existed with a default or different unit.
		detectedUnit := parseStockUnit(csvFirst(row, header, "Stock Quantity", "StockQuantity"))
		if detectedUnit != "" && product != nil && !strings.EqualFold(product.Unit, detectedUnit) {
			product.Unit = detectedUnit
			if err := utils.DB.Model(product).Update("unit", detectedUnit).Error; err != nil {
				errs = append(errs, fmt.Sprintf("Row %d (%s): failed to update unit: %v", rowNum, name, err))
			}
		}

		// --- 3. Update / create stock for this batch ---
		qty := parseStockQty(csvFirst(row, header, "Stock Quantity", "StockQuantity"))
		cost := parseFloat(csvFirst(row, header, "Purchase Price", "PurchasePrice"))

		// Parse optional mfg/exp dates.
		var mfgDate, expDate *time.Time
		if v := csvFirst(row, header, "mfg date", "Mfg Date", "MfgDate"); v != "" {
			if t, e := parseImportDate(v); e == nil {
				mfgDate = &t
			}
		}
		if v := csvFirst(row, header, "exp. date", "Exp Date", "ExpDate"); v != "" {
			if t, e := parseImportDate(v); e == nil {
				expDate = &t
			}
		}

		if defaultWH == uuid.Nil {
			errs = append(errs, fmt.Sprintf("Row %d (%s): no warehouse available", rowNum, name))
			continue
		}

		// Upsert inventory stock for this product + batch + warehouse.
		var stock models.InventoryStock
		queryErr := utils.DB.Where(
			"user_id = ? AND product_id = ? AND outlet_id = ? AND batch_no = ?",
			userID, product.ID, defaultWH, batchNo,
		).First(&stock).Error

		if queryErr == nil {
			// Update existing stock row.
			stock.Quantity = qty
			stock.AvailableQty = qty - stock.ReservedQty
			if cost > 0 {
				stock.AverageCost = cost
			}
			if mfgDate != nil {
				stock.MfgDate = mfgDate
			}
			if expDate != nil {
				stock.ExpDate = expDate
			}
			stock.LastUpdated = time.Now()
			if err := utils.DB.Save(&stock).Error; err != nil {
				errs = append(errs, fmt.Sprintf("Row %d (%s): failed to update stock: %v", rowNum, name, err))
				continue
			}
		} else {
			// Create new stock row.
			stock = models.InventoryStock{
				ID:              uuid.New(),
				UserID:          userID,
				ProductID:       product.ID,
				OutletID:        defaultWH,
				BatchNo:         batchNo,
				MfgDate:         mfgDate,
				ExpDate:         expDate,
				Quantity:        qty,
				InitialQuantity: qty,
				AvailableQty:    qty,
				AverageCost:     cost,
				LastUpdated:     time.Now(),
			}
			if err := utils.DB.Create(&stock).Error; err != nil {
				errs = append(errs, fmt.Sprintf("Row %d (%s): failed to create stock: %v", rowNum, name, err))
				continue
			}
		}
		stockUpdated++
		imported++
	}

	c.JSON(http.StatusOK, gin.H{
		"imported":           imported,
		"categories_created": categoriesCreated,
		"products_created":   productsCreated,
		"stock_updated":      stockUpdated,
		"errors":             errs,
	})
}

// parseStockSummaryCSV finds the header row in a stock summary report CSV
// (skipping the preamble) and returns (header, dataRows).
func parseStockSummaryCSV(content []byte) ([]string, [][]string, error) {
	content = stripBOM(content)
	content = normalizeCRLF(content)

	reader := csv.NewReader(strings.NewReader(string(content)))
	reader.FieldsPerRecord = -1
	reader.LazyQuotes = true
	all, err := reader.ReadAll()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to parse CSV: %w", err)
	}
	if len(all) == 0 {
		return nil, nil, errors.New("CSV file is empty")
	}

	// Find the header row — the first row whose first cell is "Name"
	// (case-insensitive). Everything before it is preamble.
	headerIdx := -1
	for i, row := range all {
		if len(row) > 0 && strings.EqualFold(strings.TrimSpace(row[0]), "Name") {
			headerIdx = i
			break
		}
	}
	if headerIdx == -1 {
		return nil, nil, errors.New("could not find header row (expected first column \"Name\")")
	}

	header := all[headerIdx]
	var rows [][]string
	for _, row := range all[headerIdx+1:] {
		if len(row) == 0 || strings.TrimSpace(strings.Join(row, "")) == "" {
			continue
		}
		rows = append(rows, row)
	}
	return header, rows, nil
}

// parseStockQty extracts the numeric quantity from a "Stock Quantity" cell
// like "33.0 PCS" or "-1.0 BOX". Returns 0 if parsing fails.
func parseStockQty(value string) float64 {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	// Split on the first space to separate number from unit.
	parts := strings.Fields(value)
	if len(parts) == 0 {
		return 0
	}
	return parseFloat(parts[0])
}

// parseStockUnit extracts the unit from a "Stock Quantity" cell like
// "33.0 PCS" or "0.0 BOX". Returns "" if no unit is present.
func parseStockUnit(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	parts := strings.Fields(value)
	if len(parts) < 2 {
		return ""
	}
	return strings.ToUpper(parts[1])
}

// -----------------------------------------------------------------------------
// Purchase Payment Status CSV  —  POST /api/v1/migration/purchase-payments/import/csv
//
// Expected header:
//   purchase number, total amount, paid amount, balance, payment status
//
// Matches existing purchase bills by purchase number (raw "128" or "P-0128")
// and updates TotalAmount, PaidAmount, BalanceDue, and Status.
//
// Returns:
//   { "imported": <int>, "errors": [string, ...] }
// -----------------------------------------------------------------------------

func ImportPurchasePaymentStatusCSV(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	content, err := importFile(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	content = stripBOM(content)
	content = normalizeCRLF(content)

	reader := csv.NewReader(strings.NewReader(string(content)))
	reader.FieldsPerRecord = -1
	reader.LazyQuotes = true
	all, err := reader.ReadAll()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("failed to parse CSV: %v", err)})
		return
	}
	if len(all) < 2 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "CSV file has no data rows"})
		return
	}

	header := all[0]
	rows := all[1:]

	imported := 0
	var errs []string

	for i, row := range rows {
		rowNum := i + 1
		if len(row) == 0 || strings.TrimSpace(strings.Join(row, "")) == "" {
			continue
		}

		purchaseNo := strings.TrimSpace(csvFirst(row, header, "purchase number", "Purchase No", "Purchase No.", "Bill No", "Bill Number"))
		if purchaseNo == "" {
			errs = append(errs, fmt.Sprintf("Row %d: Purchase number is required", rowNum))
			continue
		}

		// Build the bill number format used in the DB ("P-0128").
		billNumber := purchaseNo
		if !strings.HasPrefix(purchaseNo, "P-") {
			billNumber = fmt.Sprintf("P-%04s", purchaseNo)
		}

		// Look up the existing bill.
		var bill models.PurchaseBill
		if err := utils.DB.Where("user_id = ? AND bill_number = ?", userID, billNumber).First(&bill).Error; err != nil {
			errs = append(errs, fmt.Sprintf("Row %d (%s): purchase bill not found", rowNum, purchaseNo))
			continue
		}

		// Parse the payment fields.
		totalAmount := parseFloat(csvFirst(row, header, "total amount", "Total Amount", "Purchase Amount"))
		paidAmount := parseFloat(csvFirst(row, header, "paid amount", "Paid Amount", "Paid"))
		balance := parseFloat(csvFirst(row, header, "balance", "Balance", "Balance Due"))
		status := strings.TrimSpace(csvFirst(row, header, "payment status", "Payment Status", "Status"))
		// Normalize status to lowercase to match existing values (unpaid, paid, partial).
		status = strings.ToLower(status)

		// If balance is not provided, compute it.
		if balance == 0 && totalAmount > 0 {
			balance = totalAmount - paidAmount
		}

		// If status is not provided, derive it from the amounts.
		if status == "" {
			if paidAmount >= totalAmount && totalAmount > 0 {
				status = "paid"
			} else if paidAmount > 0 {
				status = "partial"
			} else {
				status = "unpaid"
			}
		}

		if err := utils.DB.Model(&bill).Updates(map[string]interface{}{
			"total_amount": totalAmount,
			"paid_amount":  paidAmount,
			"balance_due":  balance,
			"status":       status,
		}).Error; err != nil {
			errs = append(errs, fmt.Sprintf("Row %d (%s): failed to update: %v", rowNum, purchaseNo, err))
			continue
		}

		// Create a PaymentOut record for the paid amount, linked to this bill.
		// Skip if paidAmount is zero or a payment already exists for this bill
		// (idempotent on re-import).
		if paidAmount > 0 {
			var existingCount int64
			utils.DB.Model(&models.PaymentOut{}).
				Where("user_id = ? AND purchase_bill_id = ?", userID, bill.ID).
				Count(&existingCount)
			if existingCount == 0 {
				po := models.PaymentOut{
					ID:             uuid.New(),
					UserID:         userID,
					PurchaseBillID: &bill.ID,
					PartyID:        bill.PartyID,
					AmountPaid:     paidAmount,
					Mode:           "cash",
					Date:           time.Now(),
					Notes:          fmt.Sprintf("Imported from payment status CSV (purchase %s)", purchaseNo),
				}
				if err := utils.DB.Create(&po).Error; err != nil {
					errs = append(errs, fmt.Sprintf("Row %d (%s): bill updated but failed to create payment out: %v", rowNum, purchaseNo, err))
				}
			}
		}

		imported++
	}

	c.JSON(http.StatusOK, gin.H{"imported": imported, "errors": errs})
}

// -----------------------------------------------------------------------------
// Purchase Bill Items CSV  —  POST /api/v1/migration/purchase-items/import/csv
//
// Expected header:
//   purchase number, item name, quantity, rate, amount
//
// Matches existing purchase bills by purchase number and creates PurchaseBillItem
// rows for each line. Products are matched by item name (case-insensitive);
// if no product is found, the item is still created with a nil product_id and
// the description set to the item name.
//
// Returns:
//   { "imported": <int>, "errors": [string, ...] }
// -----------------------------------------------------------------------------

func ImportPurchaseItemsCSV(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	content, err := importFile(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	content = stripBOM(content)
	content = normalizeCRLF(content)

	reader := csv.NewReader(strings.NewReader(string(content)))
	reader.FieldsPerRecord = -1
	reader.LazyQuotes = true
	all, err := reader.ReadAll()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("failed to parse CSV: %v", err)})
		return
	}
	if len(all) < 2 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "CSV file has no data rows"})
		return
	}

	header := all[0]
	rows := all[1:]

	imported := 0
	var errs []string

	// Cache bills and products to avoid repeated DB lookups.
	billCache := map[string]*models.PurchaseBill{}
	productCache := map[string]*models.Product{}

	for i, row := range rows {
		rowNum := i + 1
		if len(row) == 0 || strings.TrimSpace(strings.Join(row, "")) == "" {
			continue
		}

		purchaseNo := strings.TrimSpace(csvFirst(row, header, "purchase number", "Purchase No", "Purchase No.", "Bill No", "Bill Number"))
		if purchaseNo == "" {
			errs = append(errs, fmt.Sprintf("Row %d: Purchase number is required", rowNum))
			continue
		}

		billNumber := purchaseNo
		if !strings.HasPrefix(purchaseNo, "P-") {
			billNumber = fmt.Sprintf("P-%04s", purchaseNo)
		}

		bill, ok := billCache[billNumber]
		if !ok {
			var b models.PurchaseBill
			if err := utils.DB.Where("user_id = ? AND bill_number = ?", userID, billNumber).First(&b).Error; err != nil {
				errs = append(errs, fmt.Sprintf("Row %d (%s): purchase bill not found", rowNum, purchaseNo))
				continue
			}
			bill = &b
			billCache[billNumber] = bill
		}

		itemName := strings.TrimSpace(csvFirst(row, header, "item name", "Item Name", "Product Name", "Name"))
		if itemName == "" {
			errs = append(errs, fmt.Sprintf("Row %d (%s): item name is required", rowNum, purchaseNo))
			continue
		}

		quantity := parseFloat(csvFirst(row, header, "quantity", "Quantity", "Qty"))
		rate := parseFloat(csvFirst(row, header, "rate", "Rate", "Unit Price", "UnitPrice"))
		amount := parseFloat(csvFirst(row, header, "amount", "Amount", "Total"))

		if amount == 0 && quantity > 0 && rate > 0 {
			amount = quantity * rate
		}

		var productID *uuid.UUID
		var unit string
		product, pOK := productCache[strings.ToLower(itemName)]
		if !pOK {
			var p models.Product
			if err := utils.DB.Where("user_id = ? AND name = ?", userID, itemName).First(&p).Error; err == nil {
				product = &p
				productCache[strings.ToLower(itemName)] = product
			}
		}
		if product != nil {
			productID = &product.ID
			unit = product.Unit
		}

		item := models.PurchaseBillItem{
			ID:          uuid.New(),
			BillID:      bill.ID,
			ProductID:   productID,
			Description: itemName,
			Quantity:    quantity,
			Unit:        unit,
			UnitPrice:   rate,
			Total:       amount,
		}

		if err := utils.DB.Create(&item).Error; err != nil {
			errs = append(errs, fmt.Sprintf("Row %d (%s): failed to create item: %v", rowNum, purchaseNo, err))
			continue
		}
		imported++
	}

	c.JSON(http.StatusOK, gin.H{"imported": imported, "errors": errs})
}
