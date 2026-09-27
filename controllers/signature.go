package controllers

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"html"
	"strings"
	"time"

	"github.com/go-pdf/fpdf"
)

// signatureBlockHTML renders a document signature image with an "Authorized
// Signatory" caption for HTML document templates. Returns "" when no signature
// is stored on the document.
func signatureBlockHTML(signature string) string {
	signature = strings.TrimSpace(signature)
	if signature == "" {
		return ""
	}
	return fmt.Sprintf(`<div style="margin-top:30px;text-align:right;">`+
		`<img src="%s" alt="Signature" style="max-height:80px;max-width:200px;display:inline-block;"/>`+
		`<div style="border-top:1px solid #ccc;display:inline-block;min-width:160px;margin-top:4px;padding-top:4px;text-align:center;font-size:12px;color:#666;">Authorized Signatory</div>`+
		`</div>`, html.EscapeString(signature))
}

// pdfDrawSignature draws a base64 data-URL signature image right-aligned at the
// current position with an "Authorized Signatory" caption. No-op for empty or
// unsupported signatures (e.g. remote http URLs that fpdf cannot embed).
func pdfDrawSignature(pdf *fpdf.Fpdf, signature string) {
	imgType, data, ok := decodeSignatureImage(signature)
	if !ok {
		return
	}
	name := fmt.Sprintf("signature-%d", time.Now().UnixNano())
	if err := pdf.RegisterImageOptionsReader(name, fpdf.ImageOptions{
		ImageType: imgType,
		ReadDpi:   true,
	}, bytes.NewReader(data)); err != nil {
		return
	}

	pageW, _ := pdf.GetPageSize()
	_, _, right, _ := pdf.GetMargins()
	imgW := 50.0
	imgH := 16.0
	x := pageW - right - imgW

	pdf.Ln(4)
	pdf.ImageOptions(name, x, pdf.GetY(), imgW, imgH, false, fpdf.ImageOptions{}, 0, "")
	pdf.SetY(pdf.GetY() + imgH + 1)
	pdf.SetFont("Arial", "", 8)
	pdf.SetTextColor(80, 80, 80)
	pdf.CellFormat(0, 5, "Authorized Signatory", "", 1, "R", false, 0, "")
	pdf.SetTextColor(0, 0, 0)
}

// decodeSignatureImage extracts the image type and bytes from a
// "data:image/...;base64,..." signature value.
func decodeSignatureImage(signature string) (string, []byte, bool) {
	signature = strings.TrimSpace(signature)
	if !strings.HasPrefix(signature, "data:image/") {
		return "", nil, false
	}
	comma := strings.Index(signature, ",")
	if comma < 0 {
		return "", nil, false
	}
	meta := signature[5:comma]
	if !strings.HasSuffix(meta, ";base64") {
		return "", nil, false
	}
	mime := strings.TrimSuffix(meta, ";base64")
	imgType := ""
	switch mime {
	case "image/png":
		imgType = "PNG"
	case "image/jpeg", "image/jpg":
		imgType = "JPG"
	case "image/gif":
		imgType = "GIF"
	default:
		return "", nil, false
	}
	data, err := base64.StdEncoding.DecodeString(signature[comma+1:])
	if err != nil || len(data) == 0 {
		return "", nil, false
	}
	return imgType, data, true
}
