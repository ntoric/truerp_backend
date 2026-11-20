package controllers

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"testing"

	"github.com/go-pdf/fpdf"
)

func TestSafeRegisterImageRecoversFromUnsupportedPNG(t *testing.T) {
	// A JPEG labelled as PNG: fpdf rejects it ("not a PNG buffer") and,
	// without recovery, Output() fails for the whole document.
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	img.Set(1, 1, color.RGBA{R: 255, A: 255})
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}

	pdf := fpdf.New("P", "mm", "A4", "")
	pdf.AddPage()
	info, name := safeRegisterImage(pdf, "logo", "PNG", buf.Bytes())
	if info == nil {
		t.Fatal("expected image to be registered after re-encode")
	}
	pdf.ImageOptions(name, 10, 10, 20, 20, false, fpdf.ImageOptions{}, 0, "")

	// Garbage labelled as PNG must be skipped, not break the PDF.
	if info, _ := safeRegisterImage(pdf, "bad", "PNG", []byte("<svg></svg>")); info != nil {
		t.Fatal("expected garbage image to be skipped")
	}

	var out bytes.Buffer
	if err := pdf.Output(&out); err != nil {
		t.Fatalf("Output failed: %v", err)
	}
}
