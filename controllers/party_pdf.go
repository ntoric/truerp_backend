package controllers

import (
	"bytes"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"
	"truerp/models"
	"truerp/utils"

	"github.com/gin-gonic/gin"
	"github.com/go-pdf/fpdf"
	"github.com/google/uuid"
)

// ExportPartiesPDF renders every party matching the list filters (search,
// category, party_type) as a landscape A4 PDF table.
func ExportPartiesPDF(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	search := c.Query("search")
	category := c.Query("category")
	partyType := c.Query("party_type")

	parties := make([]models.Party, 0)
	if err := partyFilterQuery(userID, search, category, partyType).Order("name ASC").Find(&parties).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch parties"})
		return
	}

	businessName := ""
	var business models.Business
	if err := utils.DB.Where("user_id = ?", userID).First(&business).Error; err == nil {
		businessName = business.Name
	}

	var filters []string
	if search != "" {
		filters = append(filters, fmt.Sprintf("Search: %q", search))
	}
	if category != "" {
		filters = append(filters, "Category: "+category)
	}
	if partyType != "" {
		filters = append(filters, "Type: "+partyTypeLabel(partyType))
	}

	pdfBytes, err := buildPartiesPDF(businessName, strings.Join(filters, "  |  "), parties)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to generate PDF"})
		return
	}

	filename := fmt.Sprintf("parties_%s.pdf", time.Now().Format("2006-01-02"))
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", filename))
	c.Data(http.StatusOK, "application/pdf", pdfBytes)
}

func partyTypeLabel(t string) string {
	if t == "customer" {
		return "Customer"
	}
	return "Vendor"
}

func buildPartiesPDF(businessName, filterLabel string, parties []models.Party) ([]byte, error) {
	pdf := fpdf.New("L", "mm", "A4", "")
	pdf.SetMargins(12, 14, 12)
	pdf.SetAutoPageBreak(true, 14)
	pdf.AliasNbPages("")
	pdf.SetFooterFunc(func() {
		pdf.SetY(-10)
		pdf.SetFont("Arial", "I", 7)
		pdf.SetTextColor(140, 140, 140)
		pdf.CellFormat(0, 4, fmt.Sprintf("Page %d of {nb}", pdf.PageNo()), "", 0, "R", false, 0, "")
	})
	pdf.AddPage()

	pdf.SetFont("Arial", "B", 18)
	pdf.SetTextColor(37, 99, 235)
	pdf.CellFormat(0, 10, "PARTIES", "", 1, "L", false, 0, "")

	pdf.SetFont("Arial", "", 10)
	pdf.SetTextColor(80, 80, 80)
	if businessName != "" {
		pdf.CellFormat(0, 5.5, sanitizePDFText(businessName), "", 1, "L", false, 0, "")
	}
	pdf.CellFormat(0, 5.5, "Generated on "+time.Now().Format("02 Jan 2006, 03:04 PM"), "", 1, "L", false, 0, "")
	if filterLabel != "" {
		pdf.CellFormat(0, 5.5, sanitizePDFText("Filters: "+filterLabel), "", 1, "L", false, 0, "")
	}

	var toCollect, toPay float64
	for _, p := range parties {
		if p.Balance > 0 {
			toCollect += p.Balance
		} else if p.Balance < 0 {
			toPay += -p.Balance
		}
	}
	pdf.SetFont("Arial", "B", 10)
	pdf.SetTextColor(40, 40, 40)
	pdf.CellFormat(0, 6, fmt.Sprintf("Total parties: %d    To collect: Rs. %s    To pay: Rs. %s",
		len(parties), formatPartyAmount(toCollect), formatPartyAmount(toPay)), "", 1, "L", false, 0, "")
	pdf.Ln(3)

	pageW, _ := pdf.GetPageSize()
	leftM, _, rightM, _ := pdf.GetMargins()
	usable := pageW - leftM - rightM

	type col struct {
		title string
		width float64
		align string
	}
	cols := []col{
		{"#", 0.04, "R"},
		{"Name", 0.20, "L"},
		{"Type", 0.07, "L"},
		{"Category", 0.10, "L"},
		{"Mobile", 0.10, "L"},
		{"GSTIN", 0.13, "L"},
		{"City", 0.09, "L"},
		{"State", 0.09, "L"},
		{"Credit Limit", 0.08, "R"},
		{"Balance", 0.10, "R"},
	}
	for i := range cols {
		cols[i].width *= usable
	}

	header := func() {
		pdf.SetFillColor(243, 244, 246)
		pdf.SetDrawColor(200, 200, 200)
		pdf.SetFont("Arial", "B", 8)
		pdf.SetTextColor(40, 40, 40)
		for i, cl := range cols {
			ln := 0
			if i == len(cols)-1 {
				ln = 1
			}
			pdf.CellFormat(cl.width, 8, cl.title, "1", ln, cl.align, true, 0, "")
		}
	}
	header()

	const rowH = 6.5
	_, pageH := pdf.GetPageSize()
	_, _, _, bottomM := pdf.GetMargins()
	for i, p := range parties {
		if pdf.GetY()+rowH > pageH-bottomM {
			pdf.AddPage()
			header()
		}

		balance := formatPartyAmount(math.Abs(p.Balance))
		if p.Balance > 0 {
			balance += " (Collect)"
		} else if p.Balance < 0 {
			balance += " (Pay)"
		}

		cells := []string{
			fmt.Sprintf("%d", i+1),
			truncatePDF(p.Name, 38),
			partyTypeLabel(p.PartyType),
			truncatePDF(p.Category, 18),
			truncatePDF(p.Phone, 16),
			truncatePDF(p.GSTIN, 18),
			truncatePDF(p.City, 16),
			truncatePDF(p.State, 16),
			formatPartyAmount(p.CreditLimit),
			balance,
		}

		pdf.SetFillColor(255, 255, 255)
		pdf.SetFont("Arial", "", 8)
		for j, cl := range cols {
			pdf.SetTextColor(40, 40, 40)
			if j == len(cols)-1 {
				if p.Balance > 0 {
					pdf.SetTextColor(22, 101, 52)
				} else if p.Balance < 0 {
					pdf.SetTextColor(153, 27, 27)
				}
			}
			ln := 0
			if j == len(cols)-1 {
				ln = 1
			}
			pdf.CellFormat(cl.width, rowH, sanitizePDFText(cells[j]), "1", ln, cl.align, true, 0, "")
		}
	}

	if len(parties) == 0 {
		pdf.SetFont("Arial", "I", 9)
		pdf.SetTextColor(120, 120, 120)
		pdf.CellFormat(usable, 8, "No parties match the selected filters.", "1", 1, "C", false, 0, "")
	}

	var buf bytes.Buffer
	if err := pdf.Output(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// formatPartyAmount renders money with Indian digit grouping, e.g. 12,34,567.50.
func formatPartyAmount(v float64) string {
	neg := v < 0
	s := fmt.Sprintf("%.2f", math.Abs(v))
	intPart, frac := s[:len(s)-3], s[len(s)-3:]
	if len(intPart) > 3 {
		head, tail := intPart[:len(intPart)-3], intPart[len(intPart)-3:]
		var groups []string
		for len(head) > 2 {
			groups = append([]string{head[len(head)-2:]}, groups...)
			head = head[:len(head)-2]
		}
		if head != "" {
			groups = append([]string{head}, groups...)
		}
		intPart = strings.Join(groups, ",") + "," + tail
	}
	if neg {
		return "-" + intPart + frac
	}
	return intPart + frac
}
