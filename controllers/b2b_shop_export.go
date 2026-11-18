package controllers

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"net/http"
	"strings"
	"time"
	"truerp/models"
	"truerp/utils"

	"github.com/gin-gonic/gin"
	"github.com/go-pdf/fpdf"
	"github.com/google/uuid"
	"github.com/tealeg/xlsx/v3"
)

// b2bExportColKind controls alignment and number formatting of a column.
type b2bExportColKind int

const (
	b2bColText b2bExportColKind = iota
	b2bColInt
	b2bColQty
	b2bColMoney
)

type b2bExportCol struct {
	Title string
	Kind  b2bExportColKind
	Width float64 // share of the PDF page width
}

type b2bExportRow struct {
	Cells []interface{} // string or float64, one per column
	Bold  bool          // totals
	Sub   bool          // breakdown line under an item
}

// b2bExportTable is one sheet/document: a title block, columns and rows.
type b2bExportTable struct {
	Title    string
	Subtitle []string
	Cols     []b2bExportCol
	Rows     []b2bExportRow
}

// ExportB2BOrders exports the B2B Orders page as it is filtered: the orders
// of the selected status tab (view=orders, the default) or the consolidated
// pending items (view=items), in format=excel|csv|pdf. All matching rows are
// exported, not just the visible page.
func ExportB2BOrders(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	format := strings.ToLower(strings.TrimSpace(c.DefaultQuery("format", "excel")))
	if format != "excel" && format != "csv" && format != "pdf" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "format must be excel, csv or pdf"})
		return
	}

	var business models.Business
	utils.DB.Where("user_id = ?", userID).First(&business)

	var table b2bExportTable
	var baseName string
	var err error
	if c.Query("view") == "items" {
		table, err = b2bPendingItemsExportTable(c, userID)
		baseName = "b2b_pending_items"
	} else {
		status := strings.ToLower(strings.TrimSpace(c.DefaultQuery("status", "all")))
		table, err = b2bOrdersExportTable(c, userID, status)
		baseName = "b2b_orders_" + status
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load orders"})
		return
	}

	sub := []string{}
	if business.Name != "" {
		sub = append(sub, business.Name)
	}
	sub = append(sub, b2bExportFilterSummary(c)...)
	sub = append(sub, "Generated "+time.Now().Format("02 Jan 2006, 03:04 pm"))
	table.Subtitle = append(sub, table.Subtitle...)

	filename := fmt.Sprintf("%s_%s", baseName, time.Now().Format("2006-01-02"))
	switch format {
	case "csv":
		data, err := b2bExportCSV(table)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to write CSV"})
			return
		}
		c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s.csv\"", filename))
		c.Data(http.StatusOK, "text/csv; charset=utf-8", data)
	case "pdf":
		data, err := b2bExportPDF(table)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to generate PDF"})
			return
		}
		c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s.pdf\"", filename))
		c.Data(http.StatusOK, "application/pdf", data)
	default:
		var buf bytes.Buffer
		if err := b2bExportXLSX(table).Write(&buf); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to write Excel file"})
			return
		}
		c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s.xlsx\"", filename))
		c.Data(http.StatusOK, "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", buf.Bytes())
	}
}

func b2bOrdersExportTable(c *gin.Context, userID uuid.UUID, status string) (b2bExportTable, error) {
	q := b2bOrderFilters(c, utils.DB.Model(&models.B2BOrder{}).Where("user_id = ?", userID), "")
	if status != "" && status != "all" {
		q = q.Where("status = ?", status)
	}
	var orders []models.B2BOrder
	if err := q.Order("created_at DESC").Find(&orders).Error; err != nil {
		return b2bExportTable{}, err
	}

	title := "B2B Orders"
	if status != "" && status != "all" {
		title = "B2B Orders - " + b2bTitleCase(status)
	}
	t := b2bExportTable{
		Title: title,
		Cols: []b2bExportCol{
			{"Order #", b2bColText, 0.09},
			{"Date", b2bColText, 0.12},
			{"Buyer", b2bColText, 0.19},
			{"Contact", b2bColText, 0.11},
			{"Phone", b2bColText, 0.10},
			{"City", b2bColText, 0.09},
			{"Status", b2bColText, 0.08},
			{"Items", b2bColInt, 0.05},
			{"Qty", b2bColQty, 0.07},
			{"Amount (INR)", b2bColMoney, 0.10},
		},
	}
	var qty, amount float64
	for _, o := range orders {
		t.Rows = append(t.Rows, b2bExportRow{Cells: []interface{}{
			o.OrderNumber, o.CreatedAt.Local().Format("02 Jan 2006 15:04"), o.BuyerName, o.ContactPerson,
			o.BuyerPhone, o.City, b2bTitleCase(o.Status), float64(o.ItemCount), o.TotalQty, o.TotalAmount,
		}})
		qty += o.TotalQty
		amount += o.TotalAmount
	}
	t.Rows = append(t.Rows, b2bExportRow{Bold: true, Cells: []interface{}{
		"Total", b2bPlural(len(orders), "order"), "", "", "", "", "", "", qty, amount,
	}})
	return t, nil
}

func b2bPendingItemsExportTable(c *gin.Context, userID uuid.UUID) (b2bExportTable, error) {
	items, err := loadB2BPendingItems(c, userID)
	if err != nil {
		return b2bExportTable{}, err
	}
	t := b2bExportTable{
		Title:    "B2B Pending Items",
		Subtitle: []string{"Quantities consolidated across pending orders, with each order's share below the item."},
		Cols: []b2bExportCol{
			{"Item / Order", b2bColText, 0.40},
			{"Unit", b2bColText, 0.08},
			{"Orders", b2bColInt, 0.10},
			{"Total Qty", b2bColQty, 0.14},
			{"In Stock", b2bColQty, 0.13},
			{"Value (INR)", b2bColMoney, 0.15},
		},
	}
	var amount float64
	for _, it := range items {
		var stock interface{} = ""
		if it.StockQty != nil {
			stock = *it.StockQty
		}
		t.Rows = append(t.Rows, b2bExportRow{Bold: true, Cells: []interface{}{
			it.Name, it.Unit, float64(it.OrderCount), it.Quantity, stock, it.Amount,
		}})
		for _, o := range it.Orders {
			label := fmt.Sprintf("%s - %s (%s)", o.OrderNumber, o.BuyerName, o.CreatedAt.Local().Format("02 Jan 2006"))
			t.Rows = append(t.Rows, b2bExportRow{Sub: true, Cells: []interface{}{label, it.Unit, "", o.Quantity, "", o.Amount}})
		}
		amount += it.Amount
	}
	t.Rows = append(t.Rows, b2bExportRow{Bold: true, Cells: []interface{}{
		"Total (" + b2bPlural(len(items), "item") + ")", "", "", "", "", amount,
	}})
	return t, nil
}

func b2bTitleCase(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func b2bPlural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

func b2bExportFilterSummary(c *gin.Context) []string {
	var out []string
	from, to := strings.TrimSpace(c.Query("from")), strings.TrimSpace(c.Query("to"))
	switch {
	case from != "" && to != "":
		out = append(out, "Date: "+from+" to "+to)
	case from != "":
		out = append(out, "Date: from "+from)
	case to != "":
		out = append(out, "Date: up to "+to)
	}
	if s := strings.TrimSpace(c.Query("search")); s != "" {
		out = append(out, "Search: "+s)
	}
	return out
}

func b2bExportCellText(v interface{}, kind b2bExportColKind) string {
	f, ok := v.(float64)
	if !ok {
		s, _ := v.(string)
		return s
	}
	switch kind {
	case b2bColMoney:
		return fmt.Sprintf("%.2f", f)
	case b2bColInt:
		return fmt.Sprintf("%.0f", f)
	default:
		return formatQty(f)
	}
}

func b2bExportCSV(t b2bExportTable) ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteString("\ufeff") // BOM so Excel opens UTF-8 names correctly
	w := csv.NewWriter(&buf)
	header := make([]string, len(t.Cols))
	for i, col := range t.Cols {
		header[i] = col.Title
	}
	_ = w.Write(header)
	for _, r := range t.Rows {
		rec := make([]string, len(t.Cols))
		for i, col := range t.Cols {
			rec[i] = b2bExportCellText(r.Cells[i], col.Kind)
		}
		if r.Sub {
			rec[0] = "  " + rec[0]
		}
		_ = w.Write(rec)
	}
	w.Flush()
	return buf.Bytes(), w.Error()
}

func b2bExportXLSX(t b2bExportTable) *xlsx.File {
	file := xlsx.NewFile()
	sheet, _ := file.AddSheet("Export")

	title := sheet.AddRow().AddCell()
	title.SetValue(t.Title)
	title.GetStyle().Font.Bold = true
	title.GetStyle().Font.Size = 14
	for _, s := range t.Subtitle {
		sheet.AddRow().AddCell().SetValue(s)
	}
	sheet.AddRow()

	header := sheet.AddRow()
	for _, col := range t.Cols {
		cell := header.AddCell()
		cell.SetValue(col.Title)
		cell.GetStyle().Font.Bold = true
	}
	for _, r := range t.Rows {
		row := sheet.AddRow()
		for i, col := range t.Cols {
			cell := row.AddCell()
			switch v := r.Cells[i].(type) {
			case float64:
				cell.SetFloat(v)
				switch col.Kind {
				case b2bColMoney:
					cell.SetFormat("#,##0.00")
				case b2bColInt:
					cell.SetFormat("0")
				default:
					cell.SetFormat("#,##0.###")
				}
			case string:
				if i == 0 && r.Sub {
					v = "    " + v
				}
				cell.SetValue(v)
			}
			if r.Bold {
				cell.GetStyle().Font.Bold = true
			}
		}
	}
	for i, col := range t.Cols {
		width := col.Width * 120
		if width < 8 {
			width = 8
		}
		sheet.SetColWidth(i+1, i+1, width)
	}
	return file
}

func b2bExportPDF(t b2bExportTable) ([]byte, error) {
	pdf := fpdf.New("L", "mm", "A4", "")
	pdf.SetMargins(12, 14, 12)
	pdf.SetAutoPageBreak(true, 14)
	pdf.AliasNbPages("")
	pdf.SetFooterFunc(func() {
		pdf.SetY(-10)
		pdf.SetFont("Arial", "I", 7)
		pdf.SetTextColor(150, 150, 150)
		pdf.CellFormat(0, 4, fmt.Sprintf("Page %d of {nb}", pdf.PageNo()), "", 0, "R", false, 0, "")
	})
	pdf.AddPage()

	pdf.SetFont("Arial", "B", 16)
	pdf.SetTextColor(37, 99, 235)
	pdf.CellFormat(0, 9, sanitizePDFText(t.Title), "", 1, "L", false, 0, "")
	pdf.SetFont("Arial", "", 9)
	pdf.SetTextColor(90, 90, 90)
	for _, s := range t.Subtitle {
		pdf.CellFormat(0, 5, sanitizePDFText(s), "", 1, "L", false, 0, "")
	}
	pdf.Ln(3)

	pageW, _ := pdf.GetPageSize()
	left, _, right, _ := pdf.GetMargins()
	usable := pageW - left - right
	var share float64
	for _, col := range t.Cols {
		share += col.Width
	}
	widths := make([]float64, len(t.Cols))
	for i, col := range t.Cols {
		widths[i] = usable * col.Width / share
	}
	align := func(k b2bExportColKind) string {
		if k == b2bColText {
			return "L"
		}
		return "R"
	}

	drawHeader := func() {
		pdf.SetFillColor(243, 244, 246)
		pdf.SetDrawColor(210, 210, 210)
		pdf.SetFont("Arial", "B", 8)
		pdf.SetTextColor(40, 40, 40)
		for i, col := range t.Cols {
			pdf.CellFormat(widths[i], 7, sanitizePDFText(col.Title), "1", 0, align(col.Kind), true, 0, "")
		}
		pdf.Ln(-1)
	}
	drawHeader()

	_, pageH := pdf.GetPageSize()
	_, _, _, bottom := pdf.GetMargins()
	for _, r := range t.Rows {
		h := 6.0
		if r.Sub {
			h = 5.0
		}
		if pdf.GetY()+h > pageH-bottom {
			pdf.AddPage()
			drawHeader()
		}
		switch {
		case r.Sub:
			pdf.SetFont("Arial", "", 7)
			pdf.SetTextColor(100, 100, 100)
			pdf.SetFillColor(250, 250, 250)
		case r.Bold:
			pdf.SetFont("Arial", "B", 8)
			pdf.SetTextColor(30, 30, 30)
			pdf.SetFillColor(248, 250, 252)
		default:
			pdf.SetFont("Arial", "", 8)
			pdf.SetTextColor(40, 40, 40)
			pdf.SetFillColor(255, 255, 255)
		}
		for i, col := range t.Cols {
			text := b2bExportCellText(r.Cells[i], col.Kind)
			if i == 0 && r.Sub {
				text = "    " + text
			}
			maxChars := int(widths[i] / 1.6)
			pdf.CellFormat(widths[i], h, sanitizePDFText(truncatePDF(text, maxChars)), "1", 0, align(col.Kind), true, 0, "")
		}
		pdf.Ln(-1)
	}

	var buf bytes.Buffer
	if err := pdf.Output(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
