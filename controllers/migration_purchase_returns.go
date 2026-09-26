package controllers

import (
	"fmt"
	"net/http"
	"strings"
	"time"
	"truerp/models"
	"truerp/services"
	"truerp/utils"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// -----------------------------------------------------------------------------
// Purchase Returns CSV  —  POST /api/v1/migration/purchase-returns/import/csv
//
// Expected header (myBillBook daybook-style "Purchase Return" export):
//   Date, Name, Transaction Type, Sr No., Total Amount, Money In, Money Out,
//   Balance Amount, Created By
//
// One PurchaseReturn document per row, numbered PR-<Sr No.>. A single summary
// line item carries the Total Amount (the export has no line-item detail) and
// the return is marked "processed" — the goods already left and the imported
// opening stock already reflects that.
//
// Only rows whose Transaction Type is "Purchase Return" are imported; other
// types are skipped so a combined daybook file can be uploaded safely. Rows
// with an empty type are treated as purchase returns.
//
// Money columns are informational only: no payment-in, cash transaction, or
// party balance change is created — refunds are migrated separately via the
// Daybook / Cash & Bank statement importers. A pending "Balance Amount" is
// noted on the return's notes.
//
// Idempotent: returns dedupe by return number, so re-running with the same
// file is safe.
// -----------------------------------------------------------------------------

func ImportPurchaseReturnsCSV(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	content, err := importFile(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	result, errs, perr := importPurchaseReturnsRows(userID, content, nil, nil)
	if perr != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": perr.Error()})
		return
	}
	result["errors"] = errs
	c.JSON(http.StatusOK, result)
}

func importPurchaseReturnsRows(userID uuid.UUID, content []byte, _ map[string]string, progress services.ProgressFunc) (map[string]interface{}, []string, error) {
	header, rows, err := mbReadCSV(content, "Date")
	if err != nil {
		// Fall back to a plain header-first parse for files without the
		// myBillBook preamble.
		header, rows, err = mbReadCSVPlain(content)
		if err != nil {
			return nil, nil, err
		}
	}

	// Cache party lookups so repeated vendors do not hit the database for
	// every row.
	partyCache := map[string]uuid.UUID{}
	findParty := func(name, partyType string) (uuid.UUID, error) {
		key := strings.ToLower(strings.TrimSpace(name))
		if id, ok := partyCache[key]; ok {
			return id, nil
		}
		id, err := mbFindOrCreatePartyByName(utils.DB, userID, name, partyType)
		if err != nil {
			return uuid.Nil, err
		}
		partyCache[key] = id
		return id, nil
	}

	imported := 0
	skipped := 0
	var errs []string

	for i, row := range rows {
		if progress != nil {
			progress(i+1, len(rows), imported)
		}
		rowNum := i + 1
		if len(row) == 0 {
			continue
		}

		typ := strings.TrimSpace(mbFirstCSVValue(row, header, "Transaction Type", "Type"))
		if typ != "" && !strings.EqualFold(typ, "Purchase Return") {
			skipped++
			continue
		}

		txnNo := strings.TrimSpace(mbFirstCSVValue(row, header, "Sr No.", "Sr No", "Return No", "Return No.", "Return Number"))
		name := strings.TrimSpace(mbFirstCSVValue(row, header, "Name", "Party", "Party Name", "Vendor"))
		total := mbParseAmount(mbFirstCSVValue(row, header, "Total Amount", "Amount"))
		moneyIn := mbParseAmount(mbFirstCSVValue(row, header, "Money In", "Received"))
		balance := mbParseAmount(mbFirstCSVValue(row, header, "Balance Amount", "Balance", "Balance Due"))
		createdBy := strings.TrimSpace(mbFirstCSVValue(row, header, "Created By", "Created by"))

		dateStr := strings.TrimSpace(mbFirstCSVValue(row, header, "Date"))
		date, derr := mbParseDate(dateStr)
		if derr != nil {
			// Rows with no date at all carry no transaction — skip rather
			// than error.
			if dateStr == "" && total <= 0 && moneyIn <= 0 {
				skipped++
				continue
			}
			errs = append(errs, fmt.Sprintf("Row %d: invalid date %q", rowNum, dateStr))
			continue
		}
		if date.IsZero() {
			date = time.Now()
		}

		if total <= 0 {
			skipped++
			continue
		}

		if txnNo == "" {
			errs = append(errs, fmt.Sprintf("Row %d (%s): Sr No. is required", rowNum, name))
			continue
		}
		returnNumber := txnNo
		if !strings.HasPrefix(strings.ToUpper(txnNo), "PR-") {
			returnNumber = fmt.Sprintf("PR-%04s", txnNo)
		}

		// Dedupe by return number so re-imports are a no-op.
		var existing models.PurchaseReturn
		if err := utils.DB.Where("user_id = ? AND return_number = ?", userID, returnNumber).First(&existing).Error; err == nil {
			skipped++
			continue
		}

		if name == "" {
			errs = append(errs, fmt.Sprintf("Row %d (%s): vendor name is required", rowNum, returnNumber))
			continue
		}
		partyID, perr := findParty(name, "vendor")
		if perr != nil {
			errs = append(errs, fmt.Sprintf("Row %d (%s): %v", rowNum, name, perr))
			continue
		}

		notes := ""
		if createdBy != "" {
			notes = "Created by: " + createdBy
		}
		if balance > 0.005 {
			if notes != "" {
				notes += " | "
			}
			notes += fmt.Sprintf("Pending refund: %.2f", balance)
		}

		refundMode := "credit_note"
		if moneyIn > 0 {
			refundMode = "cash"
		}

		ret := models.PurchaseReturn{
			ID:           uuid.New(),
			UserID:       userID,
			PartyID:      partyID,
			ReturnNumber: returnNumber,
			Date:         date,
			Amount:       total,
			Status:       "processed",
			RefundMode:   refundMode,
			Notes:        notes,
			Items: []models.PurchaseReturnItem{
				{
					ID:          uuid.New(),
					Description: "Migrated from myBillBook (summary)",
					Quantity:    1,
					UnitPrice:   total,
					Total:       total,
				},
			},
		}
		if err := utils.DB.Create(&ret).Error; err != nil {
			errs = append(errs, fmt.Sprintf("Row %d (%s): %v", rowNum, returnNumber, err))
			continue
		}
		imported++
	}

	return map[string]interface{}{
		"imported": imported,
		"skipped":  skipped,
	}, errs, nil
}
