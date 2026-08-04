package controllers

import (
	"fmt"
	"html"
	"strings"
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
	Name     string
	SKU      string
	ItemCode string
	Category string
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

func buildProductLabelHTML(p productLabelData, size BarcodeLabelSize, compact bool) string {
	code := barcodeValueForProduct(p)
	name := html.EscapeString(p.Name)
	sku := html.EscapeString(p.SKU)
	category := html.EscapeString(p.Category)

	parts := []string{fmt.Sprintf(`<div class="label">
	<div class="product-name">%s</div>`, name)}

	if !compact && p.SKU != "" {
		parts = append(parts, fmt.Sprintf(`	<div class="product-sku">SKU: %s</div>`, sku))
	}

	parts = append(parts, fmt.Sprintf(`	<div class="product-barcode">
		<svg class="barcode"
			jsbarcode-format="code128"
			jsbarcode-value="%s"
			jsbarcode-width="%.2f"
			jsbarcode-height="%d"
			jsbarcode-fontsize="%.0f"
			jsbarcode-margin="0"
			jsbarcode-displayvalue="true">
		</svg>
	</div>
	<div class="product-price">₹%.2f</div>`,
		html.EscapeString(code), size.BarcodeW, size.BarcodeH, size.MetaFontPx, p.SalePrice))

	if !compact && p.MRP > 0 {
		parts = append(parts, fmt.Sprintf(`	<div class="product-mrp">MRP: ₹%.2f</div>`, p.MRP))
	}
	if !compact && size.Key == "3inch" && p.Category != "" {
		parts = append(parts, fmt.Sprintf(`	<div class="product-category">%s</div>`, category))
	}

	parts = append(parts, `</div>`)
	return strings.Join(parts, "\n")
}

func barcodeLabelPageCSS(size BarcodeLabelSize) string {
	return fmt.Sprintf(`
@page {
	size: %.2fmm %.2fmm;
	margin: 0;
}
* { box-sizing: border-box; }
body {
	font-family: Arial, Helvetica, sans-serif;
	margin: 0;
	padding: 0;
	-webkit-print-color-adjust: exact;
	print-color-adjust: exact;
}
.label {
	width: %.2fmm;
	height: %.2fmm;
	padding: %.2fmm;
	text-align: center;
	display: flex;
	flex-direction: column;
	align-items: center;
	justify-content: center;
	overflow: hidden;
	border: none;
}
.product-name {
	font-size: %.1fpx;
	font-weight: bold;
	line-height: 1.15;
	max-width: 100%%;
	overflow: hidden;
	text-overflow: ellipsis;
	white-space: nowrap;
	margin-bottom: 1px;
}
.product-sku {
	font-size: %.1fpx;
	line-height: 1.1;
	margin-bottom: 1px;
	color: #333;
}
.product-barcode {
	width: 100%%;
	line-height: 0;
	margin: 1px 0;
}
.product-barcode svg {
	max-width: 100%%;
	height: auto;
}
.product-price {
	font-size: %.1fpx;
	font-weight: bold;
	line-height: 1.1;
	margin-top: 1px;
}
.product-mrp, .product-category {
	font-size: %.1fpx;
	color: #555;
	line-height: 1.1;
}
.label + .label { page-break-before: always; }
@media print {
	.label { border: none; }
}
`, size.WidthMM, size.HeightMM, size.WidthMM, size.HeightMM, size.PaddingMM,
		size.NameFontPx, size.SkuFontPx, size.PriceFontPx, size.MetaFontPx)
}

func wrapBarcodeLabelDocument(title, css, bodyHTML string) string {
	return fmt.Sprintf(`<!DOCTYPE html>
<html>
<head>
<meta charset="UTF-8">
<title>%s</title>
<style>%s</style>
</head>
<body>
%s
<script src="https://cdn.jsdelivr.net/npm/jsbarcode@3.11.5/dist/JsBarcode.all.min.js"></script>
<script>
(function() {
	function render() {
		try {
			if (window.JsBarcode) {
				JsBarcode(".barcode").init();
			}
		} catch (e) {}
	}
	function doPrint() {
		render();
		setTimeout(function() { window.print(); }, 120);
	}
	if (document.readyState === "complete") {
		doPrint();
	} else {
		window.onload = doPrint;
	}
})();
</script>
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
<script src="https://cdn.jsdelivr.net/npm/jsbarcode@3.11.5/dist/JsBarcode.all.min.js"></script>
<script>
(function() {
	function render() {
		try {
			if (window.JsBarcode) {
				JsBarcode(".barcode").init();
			}
		} catch (e) {}
	}
	if (document.readyState === "complete") {
		render();
	} else {
		window.onload = render;
	}
})();
</script>
</body>
</html>`, css, bodyHTML)
}
