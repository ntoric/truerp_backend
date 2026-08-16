package controllers

import (
	"fmt"
	"strconv"
	"strings"
	"time"
	"truerp/models"
	"truerp/utils"

	"github.com/google/uuid"
)

type posPartySnapshot struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Phone string `json:"phone"`
	GSTIN string `json:"gstin"`
}

func parseOptionalUUID(raw string) uuid.UUID {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return uuid.Nil
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		return uuid.Nil
	}
	return id
}

func findInvoiceByClientSaleID(userID uuid.UUID, clientSaleID uuid.UUID) (*models.Invoice, bool) {
	if clientSaleID == uuid.Nil {
		return nil, false
	}
	var invoice models.Invoice
	err := utils.DB.Where("user_id = ? AND client_sale_id = ?", userID, clientSaleID).
		Preload("Party").
		Preload("Items").
		First(&invoice).Error
	if err != nil {
		return nil, false
	}
	return &invoice, true
}

func invoiceNumberInUse(userID uuid.UUID, invoiceNumber string, excludeID uuid.UUID) bool {
	invoiceNumber = strings.TrimSpace(invoiceNumber)
	if invoiceNumber == "" {
		return false
	}
	q := utils.DB.Model(&models.Invoice{}).Where("user_id = ? AND invoice_number = ?", userID, invoiceNumber)
	if excludeID != uuid.Nil {
		q = q.Where("id <> ?", excludeID)
	}
	var n int64
	q.Count(&n)
	return n > 0
}

func parseInvoiceSequence(invoiceNumber, prefix string) int64 {
	raw := strings.TrimSpace(invoiceNumber)
	p := strings.TrimSpace(prefix)
	if raw == "" || p == "" {
		return 0
	}
	want := strings.ToUpper(p) + "-"
	if !strings.HasPrefix(strings.ToUpper(raw), want) {
		return 0
	}
	rest := raw[len(want):]
	n, err := strconv.ParseInt(strings.TrimSpace(rest), 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

func maxInvoiceSequence(userID uuid.UUID, prefix string) int64 {
	var numbers []string
	utils.DB.Model(&models.Invoice{}).
		Where("user_id = ? AND invoice_number LIKE ?", userID, prefix+"-%").
		Pluck("invoice_number", &numbers)
	var max int64
	for _, number := range numbers {
		if seq := parseInvoiceSequence(number, prefix); seq > max {
			max = seq
		}
	}
	return max
}

func allocateUniqueInvoiceNumber(userID uuid.UUID, preferred string) string {
	preferred = strings.TrimSpace(preferred)
	if preferred != "" && !invoiceNumberInUse(userID, preferred, uuid.Nil) {
		return preferred
	}

	prefix := "INV"
	var settings models.InvoiceSettings
	if err := utils.DB.Where("user_id = ?", userID).First(&settings).Error; err == nil && strings.TrimSpace(settings.InvoicePrefix) != "" {
		prefix = strings.TrimSpace(settings.InvoicePrefix)
	}

	start := maxInvoiceSequence(userID, prefix) + 1
	if start < 1 {
		start = 1
	}
	if int64(settings.StartingNumber) > start {
		start = int64(settings.StartingNumber)
	}
	for i := start; i < start+10000; i++ {
		candidate := fmt.Sprintf("%s-%04d", prefix, i)
		if !invoiceNumberInUse(userID, candidate, uuid.Nil) {
			return candidate
		}
	}
	return fmt.Sprintf("%s-%d", prefix, time.Now().Unix())
}

func resolvePOSParty(userID, partyID uuid.UUID, snapshot *posPartySnapshot) (models.Party, error) {
	var party models.Party
	if partyID != uuid.Nil {
		if err := utils.DB.Where("user_id = ? AND id = ?", userID, partyID).First(&party).Error; err == nil {
			return party, nil
		}
	}

	name := "Walk-in Customer"
	phone := ""
	gstin := ""
	if snapshot != nil {
		if strings.TrimSpace(snapshot.Name) != "" {
			name = strings.TrimSpace(snapshot.Name)
		}
		phone = strings.TrimSpace(snapshot.Phone)
		gstin = strings.TrimSpace(snapshot.GSTIN)
	}

	if phone != "" {
		if err := utils.DB.Where("user_id = ? AND phone = ? AND party_type = ?", userID, phone, "customer").
			First(&party).Error; err == nil {
			return party, nil
		}
	}

	if strings.EqualFold(name, "Walk-in Customer") {
		if err := utils.DB.Where("user_id = ? AND LOWER(TRIM(name)) = ? AND party_type = ?", userID, "walk-in customer", "customer").
			First(&party).Error; err == nil {
			return party, nil
		}
	}

	party = models.Party{
		ID:        uuid.New(),
		UserID:    userID,
		Name:      name,
		Phone:     phone,
		GSTIN:     gstin,
		PartyType: "customer",
		IsActive:  true,
	}
	if err := utils.DB.Create(&party).Error; err != nil {
		return party, err
	}
	return party, nil
}

func resolvePOSSessionID(userID uuid.UUID, sessionID *uuid.UUID, openingCash float64) *uuid.UUID {
	if sessionID == nil || *sessionID == uuid.Nil {
		var open models.POSSession
		if err := utils.DB.Where("user_id = ? AND status = ?", userID, "open").First(&open).Error; err == nil {
			return &open.ID
		}
		return nil
	}

	var session models.POSSession
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, *sessionID).First(&session).Error; err == nil {
		return &session.ID
	}

	var open models.POSSession
	if err := utils.DB.Where("user_id = ? AND status = ?", userID, "open").First(&open).Error; err == nil {
		return &open.ID
	}

	created := models.POSSession{
		ID:          *sessionID,
		UserID:      userID,
		CashierID:   userID,
		Status:      "open",
		OpeningCash: openingCash,
		OpenedAt:    time.Now(),
	}
	if err := utils.DB.Create(&created).Error; err != nil {
		return nil
	}
	return &created.ID
}
