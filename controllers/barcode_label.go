package controllers

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"html"
	"image/png"
	"strings"

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
	"1inch": {
		Key: "1inch", Label: "1 Inch (25mm)",
		WidthMM: 25.4, HeightMM: 15,
		NameFontPx: 7, SkuFontPx: 6, PriceFontPx: 8, MetaFontPx: 5.5,
		BarcodeH: 18, BarcodeW: 1, PaddingMM: 0.8,
	},
	"1.5inch": {
		Key: "1.5inch", Label: "1.5 Inch (38mm)",
		WidthMM: 38.1, HeightMM: 25,
		NameFontPx: 9, SkuFontPx: 7, PriceFontPx: 11, MetaFontPx: 7,
		BarcodeH: 28, BarcodeW: 1.2, PaddingMM: 1.2,
	},
	"2inch": {
		Key: "2inch", Label: "2 Inch (51mm)",
		WidthMM: 50.8, HeightMM: 30,
		NameFontPx: 11, SkuFontPx: 8, PriceFontPx: 13, MetaFontPx: 8,
		BarcodeH: 36, BarcodeW: 1.4, PaddingMM: 1.5,
	},
	"3inch": {
		Key: "3inch", Label: "3 Inch (76mm)",
		WidthMM: 76.2, HeightMM: 50,
		NameFontPx: 14, SkuFontPx: 10, PriceFontPx: 16, MetaFontPx: 10,
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
	// Slightly larger “normal” name than the old bold compact sizes.
	namePx := size.NameFontPx
	if namePx < 9 {
		namePx = 9
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
	gap: 1mm;
	margin: 0;
}
.product-mrp, .product-sku {
	font-size: %.1fpx;
	color: #333;
	line-height: 1.1;
	margin: 0;
	text-align: left;
	flex: 1 1 auto;
	min-width: 0;
	word-break: break-word;
}
.product-price {
	font-size: %.1fpx;
	font-weight: 700;
	line-height: 1.1;
	margin: 0;
	text-align: right;
	flex: 0 0 auto;
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
		namePx, barcodeMaxH, size.MetaFontPx, size.MetaFontPx, size.PriceFontPx,
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
