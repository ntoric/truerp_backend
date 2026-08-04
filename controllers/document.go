package controllers

import (
	"truerp/models"
	"truerp/utils"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func GenerateReceiptPDF(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id := c.Param("id")

	var payment models.Payment
	if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).
		Preload("Party").
		First(&payment).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Payment not found"})
		return
	}

	// Generate HTML for PDF
	html := fmt.Sprintf(`
<!DOCTYPE html>
<html>
<head>
	<meta charset="UTF-8">
	<title>Receipt - %s</title>
	<style>
		body {
			font-family: Arial, sans-serif;
			margin: 0;
			padding: 20px;
			color: #333;
		}
		.header {
			text-align: center;
			margin-bottom: 30px;
		}
		.title {
			font-size: 28px;
			font-weight: bold;
			color: #2563eb;
		}
		.receipt-number {
			font-size: 18px;
			color: #666;
			margin-top: 10px;
		}
		.section {
			margin-bottom: 20px;
		}
		.section-title {
			font-size: 14px;
			font-weight: bold;
			color: #666;
			margin-bottom: 10px;
		}
		.info-grid {
			display: grid;
			grid-template-columns: 1fr 1fr;
			gap: 10px;
		}
		.info-item {
			margin-bottom: 5px;
		}
		.info-label {
			font-weight: bold;
			color: #666;
		}
		.amount-display {
			font-size: 36px;
			font-weight: bold;
			color: #16a34a;
			text-align: center;
			margin: 30px 0;
		}
		.party-info {
			background-color: #f3f4f6;
			padding: 15px;
			border-radius: 8px;
		}
		.footer {
			margin-top: 40px;
			padding-top: 20px;
			border-top: 1px solid #ddd;
			text-align: center;
		}
		.thank-you {
			font-size: 18px;
			font-weight: bold;
			color: #666;
		}
		.qr-code {
			width: 120px;
			height: 120px;
			background-color: #f3f4f6;
			display: flex;
			align-items: center;
			justify-content: center;
			font-size: 10px;
			color: #666;
			margin: 20px auto;
		}
		@media print {
			body { margin: 0; padding: 10px; }
		}
	</style>
</head>
<body>
	<div class="header">
		<div class="title">PAYMENT RECEIPT</div>
		<div class="receipt-number">%s</div>
	</div>

	<div class="amount-display">
		₹%.2f
	</div>

	<div class="section">
		<div class="section-title">Received From</div>
		<div class="party-info">
			<strong>%s</strong><br>
			%s<br>
			%s<br>
			GSTIN: %s
		</div>
	</div>

	<div class="section">
		<div class="section-title">Payment Details</div>
		<div class="info-grid">
			<div class="info-item">
				<span class="info-label">Payment Date:</span> %s
			</div>
			<div class="info-item">
				<span class="info-label">Payment Mode:</span> %s
			</div>
			<div class="info-item">
				<span class="info-label">Reference:</span> %s
			</div>
			<div class="info-item">
				<span class="info-label">Discount:</span> ₹%.2f
			</div>
		</div>
	</div>

	<div class="section">
		<div class="section-title">Summary</div>
		<div class="info-grid">
			<div class="info-item">
				<span class="info-label">Amount Received:</span> ₹%.2f
			</div>
			<div class="info-item">
				<span class="info-label">Net Amount:</span> ₹%.2f
			</div>
		</div>
	</div>

	%s

	<div class="footer">
		<div class="thank-you">Thank you for your payment!</div>
		<div class="qr-code">QR Code</div>
		<div style="font-size: 12px; color: #666; margin-top: 10px;">
			This is a computer-generated receipt. No signature required.
		</div>
	</div>

	<script>
		window.onload = function() {
			window.print();
		};
	</script>
</body>
</html>`,
		payment.PaymentInNumber,
		payment.PaymentInNumber,
		payment.AmountReceived,
		payment.Party.Name,
		payment.Party.Address,
		fmt.Sprintf("%s, %s - %s", payment.Party.City, payment.Party.State, payment.Party.Pincode),
		payment.Party.GSTIN,
		payment.Date.Format("02-01-2006"),
		strings.ToUpper(payment.Mode),
		payment.Reference,
		payment.PaymentInDiscount,
		payment.AmountReceived,
		payment.AmountReceived-payment.PaymentInDiscount,
		func() string {
			if payment.Notes != "" {
				return fmt.Sprintf(`<div class="section">
				<div class="section-title">Notes</div>
				<div style="font-size: 12px; color: #666;">%s</div>
			</div>`, payment.Notes)
			}
			return ""
		}(),
	)

	c.Header("Content-Type", "text/html")
	c.String(http.StatusOK, html)
}

func BulkExportPDFs(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	var input struct {
		DocumentType string     `json:"document_type" binding:"required,oneof=invoice quotation"` // invoice, quotation
		IDs          []uuid.UUID `json:"ids" binding:"required,min=1"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var documentURLs []string

	switch input.DocumentType {
	case "invoice":
		for _, id := range input.IDs {
			var invoice models.Invoice
			if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&invoice).Error; err == nil {
				documentURLs = append(documentURLs, fmt.Sprintf("/api/v1/invoices/%s/pdf", id))
			}
		}
	case "quotation":
		for _, id := range input.IDs {
			var quotation models.Quotation
			if err := utils.DB.Where("user_id = ? AND id = ?", userID, id).First(&quotation).Error; err == nil {
				documentURLs = append(documentURLs, fmt.Sprintf("/api/v1/quotations/%s/pdf", id))
			}
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"document_type": input.DocumentType,
		"total":         len(documentURLs),
		"urls":          documentURLs,
	})
}

func GenerateBarcode(c *gin.Context) {
	var input struct {
		Type  string `json:"type" binding:"required,oneof=code128 ean13 upca"` // code128, ean13, upca
		Value string `json:"value" binding:"required"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Generate SVG barcode (simplified implementation)
	// In production, use a proper barcode library like github.com/boombuler/barcode
	barcodeSVG := fmt.Sprintf(`
<svg xmlns="http://www.w3.org/2000/svg" width="200" height="100">
	<rect x="10" y="10" width="180" height="80" fill="white" stroke="black" stroke-width="1"/>
	<text x="100" y="50" text-anchor="middle" font-family="monospace" font-size="12">%s</text>
	<text x="100" y="70" text-anchor="middle" font-family="monospace" font-size="10">Type: %s</text>
</svg>`, input.Value, input.Type)

	c.Header("Content-Type", "image/svg+xml")
	c.String(http.StatusOK, barcodeSVG)
}

func GenerateQRCode(c *gin.Context) {
	var input struct {
		Data string `json:"data" binding:"required"`
		Size int    `json:"size"`
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if input.Size == 0 {
		input.Size = 200
	}

	// Generate SVG QR code (simplified implementation)
	// In production, use a proper QR code library like github.com/skip2/go-qrcode
	qrSVG := fmt.Sprintf(`
<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d">
	<rect x="0" y="0" width="%d" height="%d" fill="white"/>
	<rect x="10" y="10" width="%d" height="%d" fill="black"/>
	<rect x="%d" y="10" width="%d" height="%d" fill="black"/>
	<rect x="10" y="%d" width="%d" height="%d" fill="black"/>
	<text x="%d" y="%d" text-anchor="middle" font-family="monospace" font-size="8">%s</text>
</svg>`, input.Size, input.Size, input.Size, input.Size, input.Size/3, input.Size/3, input.Size-input.Size/3-10, input.Size/3, input.Size/3, input.Size-input.Size/3-10, input.Size/3, input.Size/3, input.Size/2, input.Size-10, input.Data)

	c.Header("Content-Type", "image/svg+xml")
	c.String(http.StatusOK, qrSVG)
}
