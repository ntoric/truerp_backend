package controllers

import (
	"fmt"
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

func allocateUniqueInvoiceNumber(userID uuid.UUID, preferred string) string {
	preferred = strings.TrimSpace(preferred)
	if preferred != "" {
		var n int64
		utils.DB.Model(&models.Invoice{}).Where("user_id = ? AND invoice_number = ?", userID, preferred).Count(&n)
		if n == 0 {
			return preferred
		}
	}

	prefix := "INV"
	var settings models.InvoiceSettings
	if err := utils.DB.Where("user_id = ?", userID).First(&settings).Error; err == nil && strings.TrimSpace(settings.InvoicePrefix) != "" {
		prefix = strings.TrimSpace(settings.InvoicePrefix)
	}

	var count int64
	utils.DB.Model(&models.Invoice{}).Where("user_id = ?", userID).Count(&count)
	start := count + 1
	if settings.StartingNumber > int(start) {
		start = int64(settings.StartingNumber)
	}
	for i := start; i < start+10000; i++ {
		candidate := fmt.Sprintf("%s-%04d", prefix, i)
		var n int64
		utils.DB.Model(&models.Invoice{}).Where("user_id = ? AND invoice_number = ?", userID, candidate).Count(&n)
		if n == 0 {
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
