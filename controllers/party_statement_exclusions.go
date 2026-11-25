package controllers

import (
	"fmt"
	"net/http"
	"time"
	"truerp/models"
	"truerp/utils"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm/clause"
)

// Parties on the store's exclusion list are left out of bulk statement sends
// and schedules. Sending one party's statement by hand still works.

// excludedStatementParties returns the store's excluded party IDs.
func excludedStatementParties(userID uuid.UUID) map[uuid.UUID]bool {
	var ids []uuid.UUID
	utils.DB.Model(&models.PartyStatementExclusion{}).Where("user_id = ?", userID).Pluck("party_id", &ids)
	out := make(map[uuid.UUID]bool, len(ids))
	for _, id := range ids {
		out[id] = true
	}
	return out
}

// withoutExcludedStatementParties drops excluded parties from ids, keeping
// order, and reports how many were dropped.
func withoutExcludedStatementParties(userID uuid.UUID, ids []uuid.UUID) ([]uuid.UUID, int) {
	excluded := excludedStatementParties(userID)
	if len(excluded) == 0 {
		return ids, 0
	}
	kept := make([]uuid.UUID, 0, len(ids))
	for _, id := range ids {
		if !excluded[id] {
			kept = append(kept, id)
		}
	}
	return kept, len(ids) - len(kept)
}

type partyStatementExclusionView struct {
	PartyID    uuid.UUID `json:"party_id"`
	Name       string    `json:"name"`
	Phone      string    `json:"phone"`
	PartyType  string    `json:"party_type"`
	ExcludedAt time.Time `json:"excluded_at"`
}

// ListPartyStatementExclusions lists the store's excluded parties by name.
func ListPartyStatementExclusions(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	rows := make([]partyStatementExclusionView, 0)
	err := utils.DB.Table("party_statement_exclusions AS e").
		Select("e.party_id, p.name, p.phone, p.party_type, e.created_at AS excluded_at").
		Joins("JOIN parties p ON p.id = e.party_id AND p.deleted_at IS NULL").
		Where("e.user_id = ?", userID).
		Order("p.name ASC").
		Scan(&rows).Error
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load excluded parties"})
		return
	}
	c.JSON(http.StatusOK, rows)
}

// AddPartyStatementExclusions excludes the given parties, or every party
// matching the list filters (already-excluded ones are left as they are).
func AddPartyStatementExclusions(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	var in struct {
		PartyIDs    []uuid.UUID      `json:"party_ids"`
		AllMatching bool             `json:"all_matching"`
		Filters     PartyBulkFilters `json:"filters"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	var owned []uuid.UUID
	if in.AllMatching {
		f := in.Filters
		partyFilterQuery(userID, f.Search, f.Category, f.PartyType, f.Balance).
			Limit(maxPartyStatementBulk+1).Pluck("id", &owned)
	} else {
		if len(in.PartyIDs) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Select at least one party"})
			return
		}
		if len(in.PartyIDs) > maxPartyStatementBulk {
			in.PartyIDs = in.PartyIDs[:maxPartyStatementBulk+1]
		}
		utils.DB.Model(&models.Party{}).Where("user_id = ? AND id IN ?", userID, in.PartyIDs).Pluck("id", &owned)
	}
	if len(owned) > maxPartyStatementBulk {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("Exclude at most %d parties at once", maxPartyStatementBulk)})
		return
	}
	if len(owned) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "None of the selected parties were found"})
		return
	}
	rows := make([]models.PartyStatementExclusion, 0, len(owned))
	for _, id := range owned {
		rows = append(rows, models.PartyStatementExclusion{ID: uuid.New(), UserID: userID, PartyID: id})
	}
	if err := utils.DB.Clauses(clause.OnConflict{DoNothing: true}).CreateInBatches(&rows, 200).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to exclude parties"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"excluded": len(owned)})
}

// RemovePartyStatementExclusion puts a party back on bulk sends and schedules.
func RemovePartyStatementExclusion(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	pid, err := uuid.Parse(c.Param("pid"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid party id"})
		return
	}
	if err := utils.DB.Where("user_id = ? AND party_id = ?", userID, pid).
		Delete(&models.PartyStatementExclusion{}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update exclusions"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"party_id": pid})
}
