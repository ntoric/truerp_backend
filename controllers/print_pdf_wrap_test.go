package controllers

import (
	"testing"
	"truerp/models"

	"github.com/go-pdf/fpdf"
)

func TestWrapPDFTextFitsWidth(t *testing.T) {
	pdf := fpdf.New("P", "mm", "A4", "")
	pdf.AddPage()
	pdf.SetFont("Arial", "", 7)

	// Matches buildPeriodReportPDF card inner width and wrap width.
	innerW := (210.0 - 14 - 14 - 12) / 5 - 8
	wrapW := innerW - 2.0
	samples := []string{
		"Purchases 0 · Ops expenses 0 · AP 0.00",
		"Sales - purchases - expenses +/- returns",
		"Sale value - purchase cost on items",
		"Payments in - out - expenses",
		"Purchase expense",
		"Daily · 2026-08-16",
	}
	for _, sample := range samples {
		lines := wrapPDFText(pdf, sample, wrapW)
		if len(lines) == 0 {
			t.Fatalf("no lines for %q", sample)
		}
		for _, line := range lines {
			if w := pdf.GetStringWidth(line); w > wrapW+0.05 {
				t.Errorf("line %q width %.2f exceeds wrap width %.2f (source %q)", line, w, wrapW, sample)
			}
		}
		full := sanitizePDFText(sample)
		if pdf.GetStringWidth(full) > wrapW && len(lines) < 2 {
			t.Errorf("expected %q to wrap into multiple lines, got %#v", sample, lines)
		}
	}
}

func TestSanitizePDFTextLatin1MiddleDot(t *testing.T) {
	got := sanitizePDFText("Daily · 2026-08-16")
	if got != "Daily \xb7 2026-08-16" {
		t.Fatalf("got %q bytes %v", got, []byte(got))
	}
}

func TestBuildPeriodReportPDF(t *testing.T) {
	pdfBytes, err := buildPeriodReportPDF(models.PeriodReport{
		DailyReport: models.DailyReport{
			Date:          "2026-08-16",
			BusinessName:  "TruERP Softwares",
			Sales:         models.DailyReportMetric{Count: 4, TotalAmount: 361},
			DailyProfit:   361,
			ProductProfit: 182.37,
			NetCashFlow:   361,
		},
		Period:    "daily",
		StartDate: "2026-08-16",
		EndDate:   "2026-08-16",
		Label:     "Daily · 2026-08-16",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(pdfBytes) < 200 {
		t.Fatalf("pdf too small: %d", len(pdfBytes))
	}
}
