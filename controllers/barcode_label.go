package controllers

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"html"
	"image/png"
	"strings"
	"truerp/models"

	"github.com/boombuler/barcode"
	"github.com/boombuler/barcode/code128"
)

// BarcodeLabelSize describes a thermal barcode label roll preset.
type BarcodeLabelSize struct {
	Key         string
	Label       string
	WidthMM     float64
	HeightMM    float64
	NameFontPx  float64
	SkuFontPx   float64
	PriceFontPx float64
	MetaFontPx  float64
	BarcodeH    int
	BarcodeW    float64
	PaddingMM   float64
}

var barcodeLabelSizes = map[string]BarcodeLabelSize{
	// Name / MRP / sale price share NameFontPx (a bit larger than barcode digits).
	// MetaFontPx = barcode number text; PriceFontPx kept equal to NameFontPx.
	"1inch": {
		Key: "1inch", Label: "1 Inch (25mm)",
		WidthMM: 25.4, HeightMM: 15,
		NameFontPx: 7, SkuFontPx: 6, PriceFontPx: 7, MetaFontPx: 5.5,
		BarcodeH: 18, BarcodeW: 1, PaddingMM: 0.8,
	},
	"1.5inch": {
		Key: "1.5inch", Label: "1.5 Inch (38mm)",
		WidthMM: 38.1, HeightMM: 25,
		NameFontPx: 8, SkuFontPx: 7, PriceFontPx: 8, MetaFontPx: 6.5,
		BarcodeH: 28, BarcodeW: 1.2, PaddingMM: 1.2,
	},
	"2inch": {
		Key: "2inch", Label: "2 Inch (51mm)",
		WidthMM: 50.8, HeightMM: 30,
		NameFontPx: 9, SkuFontPx: 8, PriceFontPx: 9, MetaFontPx: 7.5,
		BarcodeH: 36, BarcodeW: 1.4, PaddingMM: 1.5,
	},
	"3inch": {
		Key: "3inch", Label: "3 Inch (76mm)",
		WidthMM: 76.2, HeightMM: 50,
		NameFontPx: 11, SkuFontPx: 9, PriceFontPx: 11, MetaFontPx: 9,
		BarcodeH: 48, BarcodeW: 1.6, PaddingMM: 2,
	},
}

func normalizeBarcodeLabelSize(size string) string {
	switch size {
	case "1inch", "1.5inch", "2inch", "3inch":
		return size
	default:
		return "2inch"
	}
}

func getBarcodeLabelSize(size string) BarcodeLabelSize {
	key := normalizeBarcodeLabelSize(size)
	return barcodeLabelSizes[key]
}

type productLabelData struct {
	Name      string
	SKU       string
	ItemCode  string
	Category  string
	SalePrice float64
	MRP       float64
}

func barcodeValueForProduct(p productLabelData) string {
	code := strings.TrimSpace(p.ItemCode)
	if code == "" {
		code = strings.TrimSpace(p.SKU)
	}
	if code == "" {
		code = "0000000000"
	}
	return code
}

// code128PNGDataURI renders a Code128 barcode as a PNG data URI (no CDN / JS needed).
func code128PNGDataURI(value string, moduleWidth float64, heightPx int) string {
	value = strings.TrimSpace(value)
	if value == "" {
		value = "0000000000"
	}
	if heightPx < 16 {
		heightPx = 16
	}

	bc, err := code128.Encode(value)
	if err != nil {
		// Some characters can fail Code128; fall back to a safe payload.
		bc, err = code128.Encode("0000000000")
		if err != nil {
			return ""
		}
		value = "0000000000"
	}

	scale := int(moduleWidth * 2)
	if scale < 1 {
		scale = 1
	}
	width := bc.Bounds().Dx() * scale
	if width < 40 {
		width = bc.Bounds().Dx() * 2
		if width < 40 {
			width = 40
		}
	}

	scaled, err := barcode.Scale(bc, width, heightPx)
	if err != nil {
		return ""
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, scaled); err != nil {
		return ""
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())
}

func barcodeImageHTML(value string, moduleWidth float64, heightPx int, fontPx float64) string {
	dataURI := code128PNGDataURI(value, moduleWidth, heightPx)
	display := html.EscapeString(strings.TrimSpace(value))
	if dataURI == "" {
		return fmt.Sprintf(`<div class="product-barcode-fallback">%s</div>`, display)
	}
	return fmt.Sprintf(
		`<img class="barcode-img" src="%s" alt="%s" />
	<div class="barcode-text" style="font-size:%.0fpx">%s</div>`,
		dataURI, display, fontPx, display,
	)
}

func buildProductLabelHTML(p productLabelData, size BarcodeLabelSize, compact bool) string {
	code := barcodeValueForProduct(p)
	name := html.EscapeString(p.Name)

	// Vertical layout: name (1–2 lines) → barcode → MRP left / sale price right.
	mrpCell := ""
	if p.MRP > 0 {
		mrpCell = fmt.Sprintf(`<span class="product-mrp">MRP: ₹%.2f</span>`, p.MRP)
	} else if !compact && p.SKU != "" {
		mrpCell = fmt.Sprintf(`<span class="product-sku">SKU: %s</span>`, html.EscapeString(p.SKU))
	}

	return fmt.Sprintf(`<div class="label">
	<div class="product-name">%s</div>
	<div class="product-barcode">
		%s
	</div>
	<div class="price-row">
		%s
		<span class="product-price">₹%.2f</span>
	</div>
</div>`, name,
		barcodeImageHTML(code, size.BarcodeW, size.BarcodeH, size.MetaFontPx),
		mrpCell, p.SalePrice)
}

// BarcodeLabelItemJSON is one printable sticker for silent ESC/POS / client rendering.
type BarcodeLabelItemJSON struct {
	Name    string  `json:"name"`
	Barcode string  `json:"barcode"`
	SKU     string  `json:"sku,omitempty"`
	Price   float64 `json:"price"`
	MRP     float64 `json:"mrp,omitempty"`
}

// BarcodeLabelsResponse is the structured thermal-label payload (no OS print dialog path).
type BarcodeLabelsResponse struct {
	Title    string                 `json:"title"`
	Size     string                 `json:"size"`
	WidthMM  float64                `json:"width_mm"`
	HeightMM float64                `json:"height_mm"`
	Compact  bool                   `json:"compact"`
	Labels   []BarcodeLabelItemJSON `json:"labels"`
}

func barcodeLabelPageCSS(size BarcodeLabelSize) string {
	// Name, MRP, and sale price share one size — a bit larger than barcode digits.
	bodyPx := size.NameFontPx
	if bodyPx < size.PriceFontPx {
		bodyPx = size.PriceFontPx
	}
	codePx := size.MetaFontPx
	if codePx <= 0 || codePx >= bodyPx {
		codePx = bodyPx - 1.5
		if codePx < 5 {
			codePx = 5
		}
	}
	barcodeMaxH := size.HeightMM * 0.42
	if barcodeMaxH < 6 {
		barcodeMaxH = 6
	}
	return fmt.Sprintf(`
@page {
	size: %.2fmm %.2fmm;
	margin: 0;
}
* { box-sizing: border-box; }
html, body {
	width: %.2fmm;
	margin: 0;
	padding: 0;
}
body {
	font-family: Arial, Helvetica, sans-serif;
	-webkit-print-color-adjust: exact;
	print-color-adjust: exact;
}
.label {
	width: %.2fmm;
	height: %.2fmm;
	max-width: %.2fmm;
	max-height: %.2fmm;
	padding: %.2fmm;
	display: flex;
	flex-direction: column;
	align-items: stretch;
	justify-content: space-between;
	gap: 0.4mm;
	overflow: hidden;
	border: none;
	page-break-after: always;
	break-after: page;
	page-break-inside: avoid;
	break-inside: avoid;
}
.label:last-child {
	page-break-after: auto;
	break-after: auto;
}
.product-name {
	font-size: %.1fpx;
	font-weight: 400;
	line-height: 1.15;
	width: 100%%;
	text-align: center;
	margin: 0;
	display: -webkit-box;
	-webkit-box-orient: vertical;
	-webkit-line-clamp: 2;
	overflow: hidden;
	word-break: break-word;
	overflow-wrap: anywhere;
}
.product-barcode {
	width: 100%%;
	flex: 1 1 auto;
	min-height: 0;
	line-height: 1;
	margin: 0;
	display: flex;
	flex-direction: column;
	align-items: center;
	justify-content: center;
}
.product-barcode .barcode-img {
	max-width: 100%%;
	max-height: %.2fmm;
	width: auto;
	height: auto;
	display: block;
	object-fit: contain;
}
.product-barcode .barcode-text,
.product-barcode-fallback {
	font-family: "Courier New", Courier, monospace;
	font-size: %.1fpx;
	line-height: 1.1;
	margin-top: 1px;
	letter-spacing: 0.2px;
	text-align: center;
	width: 100%%;
	word-break: break-all;
	overflow-wrap: anywhere;
	white-space: normal;
}
.price-row {
	width: 100%%;
	display: flex;
	flex-direction: row;
	align-items: baseline;
	justify-content: space-between;
	gap: 1.5mm;
	margin: 0;
	min-width: 0;
}
.product-mrp, .product-sku {
	font-size: %.1fpx;
	color: #333;
	line-height: 1.1;
	margin: 0;
	text-align: left;
	flex: 1 1 50%%;
	min-width: 0;
	overflow: hidden;
	text-overflow: ellipsis;
	white-space: nowrap;
}
.product-price {
	font-size: %.1fpx;
	font-weight: 700;
	line-height: 1.1;
	margin: 0;
	text-align: right;
	flex: 0 1 50%%;
	min-width: 0;
	overflow: hidden;
	text-overflow: ellipsis;
	white-space: nowrap;
}
@media print {
	html, body {
		width: %.2fmm !important;
		margin: 0 !important;
		padding: 0 !important;
	}
	.label {
		border: none;
		width: %.2fmm !important;
		height: %.2fmm !important;
	}
}
`, size.WidthMM, size.HeightMM, size.WidthMM,
		size.WidthMM, size.HeightMM, size.WidthMM, size.HeightMM, size.PaddingMM,
		bodyPx, barcodeMaxH, codePx, bodyPx, bodyPx,
		size.WidthMM, size.WidthMM, size.HeightMM)
}

func wrapBarcodeLabelDocument(title, css, bodyHTML string) string {
	// Printing is triggered by the frontend printHtmlDocument helper (in-app iframe).
	// Avoid embedding window.print() here so desktop/Tauri never depends on popup windows.
	return fmt.Sprintf(`<!DOCTYPE html>
<html>
<head>
<meta charset="UTF-8">
<title>%s</title>
<style>%s</style>
</head>
<body>
%s
</body>
</html>`, html.EscapeString(title), css, bodyHTML)
}

func wrapBarcodePreviewDocument(css, bodyHTML string) string {
	return fmt.Sprintf(`<!DOCTYPE html>
<html>
<head>
<meta charset="UTF-8">
<style>%s</style>
</head>
<body>
%s
</body>
</html>`, css, bodyHTML)
}

// A4LabelSheetLayout describes a fixed-grid sticker sheet (e.g. 4×11 on A4).
type A4LabelSheetLayout struct {
	PaperSize      string
	LabelWidthMM   float64
	LabelHeightMM  float64
	Columns        int
	Rows           int
	MarginTopMM    float64
	MarginRightMM  float64
	MarginBottomMM float64
	MarginLeftMM   float64
	GapHMM         float64
	GapVMM         float64
}

func (l A4LabelSheetLayout) labelsPerSheet() int {
	if l.Columns < 1 || l.Rows < 1 {
		return 1
	}
	return l.Columns * l.Rows
}

func normalizeStartPosition(start, perSheet int) int {
	if perSheet < 1 {
		perSheet = 1
	}
	if start < 1 {
		return 1
	}
	if start > perSheet {
		return perSheet
	}
	return start
}

// A4LabelSheetPreset485x254 — common 48.5 × 25.4 mm stickers, 4 cols × 11 rows (44/sheet).
// Verified: 5 + 4×48.5 + 3×2 + 5 = 210 mm; 8.8 + 11×25.4 + 8.8 = 297 mm.
func A4LabelSheetPreset485x254() A4LabelSheetLayout {
	return A4LabelSheetLayout{
		PaperSize: "A4", LabelWidthMM: 48.5, LabelHeightMM: 25.4,
		Columns: 4, Rows: 11,
		MarginTopMM: 8.8, MarginBottomMM: 8.8, MarginLeftMM: 5, MarginRightMM: 5,
		GapHMM: 2, GapVMM: 0,
	}
}

// A4LabelSheetPreset46x24 — 46 × 24 mm stickers with manufacturer gaps/margins.
// Verified: 10 + 4×46 + 3×2 + 10 = 210 mm; 11 + 11×24 + 10×1.1 + 11 = 297 mm.
func A4LabelSheetPreset46x24() A4LabelSheetLayout {
	return A4LabelSheetLayout{
		PaperSize: "A4", LabelWidthMM: 46, LabelHeightMM: 24,
		Columns: 4, Rows: 11,
		MarginTopMM: 11, MarginBottomMM: 11, MarginLeftMM: 10, MarginRightMM: 10,
		GapHMM: 2, GapVMM: 1.1,
	}
}

func a4LabelSheetPresetByKey(key string) (A4LabelSheetLayout, bool) {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "48.5x25.4", "485x254", "a4_48_5x25_4":
		return A4LabelSheetPreset485x254(), true
	case "46x24", "a4_46x24":
		return A4LabelSheetPreset46x24(), true
	default:
		return A4LabelSheetLayout{}, false
	}
}

func barcodeLabelSizeForA4Layout(layout A4LabelSheetLayout) BarcodeLabelSize {
	h := layout.LabelHeightMM
	namePx := 9.0
	metaPx := 7.0
	barcodeH := 24
	padding := 2.0
	if h >= 24 {
		namePx = 10
		metaPx = 7.5
		barcodeH = 26
		padding = 2.5
	}
	if h >= 25 {
		namePx = 10.5
		metaPx = 8
		barcodeH = 28
	}
	return BarcodeLabelSize{
		Key:         "a4",
		WidthMM:     layout.LabelWidthMM,
		HeightMM:    layout.LabelHeightMM,
		NameFontPx:  namePx,
		SkuFontPx:   namePx - 1,
		PriceFontPx: namePx,
		MetaFontPx:  metaPx,
		BarcodeH:    barcodeH,
		BarcodeW:    1.2,
		PaddingMM:   padding,
	}
}

func a4PaperDimensionsMM(paperSize string) (width, height float64) {
	switch strings.ToLower(strings.TrimSpace(paperSize)) {
	case "letter":
		return 216, 279
	case "a5":
		return 148, 210
	case "legal":
		return 216, 356
	default:
		return 210, 297
	}
}

func a4LabelSheetCSS(layout A4LabelSheetLayout, screenPreview bool) string {
	paperW, paperH := a4PaperDimensionsMM(layout.PaperSize)
	size := barcodeLabelSizeForA4Layout(layout)
	labelCSS := barcodeLabelPageCSS(size)
	// Strip per-label page breaks — labels sit on a shared sheet grid.
	labelCSS = strings.ReplaceAll(labelCSS, "page-break-after: always;", "")
	labelCSS = strings.ReplaceAll(labelCSS, "break-after: page;", "")
	labelCSS = strings.ReplaceAll(labelCSS, "page-break-after: auto;", "")
	labelCSS = strings.ReplaceAll(labelCSS, "break-after: auto;", "")

	borderRule := ""
	if screenPreview {
		borderRule = `.label-slot .label { border: 1px dashed #9ca3af; }`
	}

	return labelCSS + fmt.Sprintf(`
@page {
	size: %s;
	margin: 0;
}
html, body {
	margin: 0;
	padding: 0;
	width: %.2fmm;
	font-family: Arial, Helvetica, sans-serif;
	-webkit-print-color-adjust: exact;
	print-color-adjust: exact;
}
body.screen-preview {
	background: #f3f4f6;
	padding: 8px;
}
.sheet {
	width: %.2fmm;
	height: %.2fmm;
	box-sizing: border-box;
	padding: %.2fmm %.2fmm %.2fmm %.2fmm;
	page-break-after: always;
	break-after: page;
	overflow: hidden;
}
body.screen-preview .sheet {
	background: white;
	border: 1px solid #d1d5db;
	margin: 0 auto 12px;
	box-shadow: 0 1px 3px rgba(0,0,0,0.08);
}
.sheet:last-child {
	page-break-after: auto;
	break-after: auto;
}
.labels-grid {
	display: grid;
	grid-template-columns: repeat(%d, %.2fmm);
	grid-template-rows: repeat(%d, %.2fmm);
	column-gap: %.2fmm;
	row-gap: %.2fmm;
}
.label-slot {
	width: %.2fmm;
	height: %.2fmm;
	overflow: hidden;
	box-sizing: border-box;
}
.label-slot.label-empty {
	visibility: hidden;
}
.label-slot .label {
	width: 100%% !important;
	height: 100%% !important;
	max-width: 100%% !important;
	max-height: 100%% !important;
	padding: %.2fmm;
	page-break-after: unset !important;
	break-after: unset !important;
	border: none;
}
%s
@media print {
	html, body { width: %.2fmm !important; margin: 0 !important; padding: 0 !important; }
	.sheet { width: %.2fmm !important; height: %.2fmm !important; }
}
`, layout.PaperSize, paperW,
		paperW, paperH,
		layout.MarginTopMM, layout.MarginRightMM, layout.MarginBottomMM, layout.MarginLeftMM,
		layout.Columns, layout.LabelWidthMM,
		layout.Rows, layout.LabelHeightMM,
		layout.GapHMM, layout.GapVMM,
		layout.LabelWidthMM, layout.LabelHeightMM,
		size.PaddingMM,
		borderRule,
		paperW, paperW, paperH)
}

func buildA4LabelsSheetDocument(title string, labelHTMLs []string, layout A4LabelSheetLayout, startPosition int, screenPreview bool) string {
	perSheet := layout.labelsPerSheet()
	startPosition = normalizeStartPosition(startPosition, perSheet)

	var pages strings.Builder
	labelIdx := 0
	firstPage := true

	for labelIdx < len(labelHTMLs) || firstPage {
		pages.WriteString(`<div class="sheet"><div class="labels-grid">`)
		cellsUsed := 0

		if firstPage {
			for i := 1; i < startPosition; i++ {
				pages.WriteString(`<div class="label-slot label-empty"></div>`)
				cellsUsed++
			}
			firstPage = false
		}

		for cellsUsed < perSheet && labelIdx < len(labelHTMLs) {
			pages.WriteString(`<div class="label-slot">`)
			pages.WriteString(labelHTMLs[labelIdx])
			pages.WriteString(`</div>`)
			labelIdx++
			cellsUsed++
		}

		pages.WriteString(`</div></div>`)
		if labelIdx >= len(labelHTMLs) {
			break
		}
	}

	bodyClass := ""
	if screenPreview {
		bodyClass = ` class="screen-preview"`
	}
	css := a4LabelSheetCSS(layout, screenPreview)
	return fmt.Sprintf(`<!DOCTYPE html>
<html>
<head>
<meta charset="UTF-8">
<title>%s</title>
<style>%s</style>
</head>
<body%s>
%s
</body>
</html>`, html.EscapeString(title), css, bodyClass, pages.String())
}

func a4LabelSheetLayoutFromBusiness(b models.Business) A4LabelSheetLayout {
	layout := A4LabelSheetPreset485x254()
	if preset, ok := a4LabelSheetPresetByKey(b.LabelSheetPreset); ok {
		layout = preset
	}
	if b.LabelPaperSize != "" {
		layout.PaperSize = b.LabelPaperSize
	}
	if b.LabelWidthMM > 0 {
		layout.LabelWidthMM = b.LabelWidthMM
	}
	if b.LabelHeightMM > 0 {
		layout.LabelHeightMM = b.LabelHeightMM
	}
	if b.LabelColumns > 0 {
		layout.Columns = b.LabelColumns
	}
	if b.LabelRows > 0 {
		layout.Rows = b.LabelRows
	}
	margin := b.LabelMarginMM
	if margin <= 0 {
		margin = 5
	}
	if b.LabelMarginTopMM >= 0 {
		layout.MarginTopMM = b.LabelMarginTopMM
	} else if margin > 0 {
		layout.MarginTopMM = margin
	}
	if b.LabelMarginLeftMM >= 0 {
		layout.MarginLeftMM = b.LabelMarginLeftMM
	} else if margin > 0 {
		layout.MarginLeftMM = margin
	}
	layout.MarginRightMM = layout.MarginLeftMM
	layout.MarginBottomMM = layout.MarginTopMM
	if b.LabelGapHMM >= 0 {
		layout.GapHMM = b.LabelGapHMM
	}
	if b.LabelGapVMM >= 0 {
		layout.GapVMM = b.LabelGapVMM
	}
	if layout.Columns < 1 || layout.Columns > 6 {
		layout.Columns = 4
	}
	if layout.Rows < 1 || layout.Rows > 20 {
		layout.Rows = 11
	}
	return layout
}
