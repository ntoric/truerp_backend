package controllers

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"truerp/models"
	"truerp/services"
	"truerp/utils"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// -----------------------------------------------------------------------------
// Async migration jobs — runners + HTTP handlers
//
// The generic job framework lives in services/migration_jobs.go. This file
// registers a MigrationRunner for every import kind and exposes the HTTP
// endpoints to enqueue, poll, and list jobs.
//
// Kind names are stable identifiers used by the frontend and stored on each
// MigrationJob row. Add new kinds here when a new importer is introduced.
// -----------------------------------------------------------------------------

// RegisterMigrationRunners wires every migration importer into the async
// job framework. Called once at application startup from main.go.
func RegisterMigrationRunners() {
	register := func(kind string, runner services.MigrationRunner) {
		services.RegisterMigrationRunner(kind, runner)
	}

	// --- Per-entity CSV importers (migration_imports.go) ---
	register("categories", func(userID uuid.UUID, content []byte, options map[string]string, progress services.ProgressFunc) (map[string]interface{}, []string, error) {
		return importCategoriesRows(userID, content, progress)
	})
	register("expense-categories", func(userID uuid.UUID, content []byte, options map[string]string, progress services.ProgressFunc) (map[string]interface{}, []string, error) {
		return importExpenseCategoriesRows(userID, content, progress)
	})
	register("inventory", func(userID uuid.UUID, content []byte, options map[string]string, progress services.ProgressFunc) (map[string]interface{}, []string, error) {
		return importInventoryRows(userID, content, progress)
	})
	register("bank-accounts", func(userID uuid.UUID, content []byte, options map[string]string, progress services.ProgressFunc) (map[string]interface{}, []string, error) {
		return importBankAccountsRows(userID, content, options, progress)
	})
	register("cash-transactions", func(userID uuid.UUID, content []byte, options map[string]string, progress services.ProgressFunc) (map[string]interface{}, []string, error) {
		return importCashTransactionsRows(userID, content, progress)
	})
	register("users", func(userID uuid.UUID, content []byte, options map[string]string, progress services.ProgressFunc) (map[string]interface{}, []string, error) {
		return importUsersRows(userID, content, options, progress)
	})
	register("staff", func(userID uuid.UUID, content []byte, options map[string]string, progress services.ProgressFunc) (map[string]interface{}, []string, error) {
		return importStaffRows(userID, content, options, progress)
	})
	register("stock-summary", func(userID uuid.UUID, content []byte, options map[string]string, progress services.ProgressFunc) (map[string]interface{}, []string, error) {
		return importStockSummaryRows(userID, content, options, progress)
	})
	register("purchase-payments", func(userID uuid.UUID, content []byte, options map[string]string, progress services.ProgressFunc) (map[string]interface{}, []string, error) {
		return importPurchasePaymentStatusRows(userID, content, progress)
	})
	register("purchase-items", func(userID uuid.UUID, content []byte, options map[string]string, progress services.ProgressFunc) (map[string]interface{}, []string, error) {
		return importPurchaseItemsRows(userID, content, progress)
	})
	register("sales-items", func(userID uuid.UUID, content []byte, options map[string]string, progress services.ProgressFunc) (map[string]interface{}, []string, error) {
		return importSalesItemsRows(userID, content, progress)
	})

	// --- myBillBook importers (migration.go) ---
	register("parties", func(userID uuid.UUID, content []byte, options map[string]string, progress services.ProgressFunc) (map[string]interface{}, []string, error) {
		// Vendor hints are derived from the purchase summary during ZIP import;
		// for the standalone parties import the user can supply a default
		// party type via options.
		vendorHints := map[string]string{}
		if v, ok := options["vendor_hints_json"]; ok && v != "" {
			_ = json.Unmarshal([]byte(v), &vendorHints)
		}
		defaultPartyType := options["default_party_type"]
		n, errs, err := importPartiesRows(userID, content, vendorHints, defaultPartyType, progress)
		return map[string]interface{}{"imported": n}, errs, err
	})
	register("purchase-bills", func(userID uuid.UUID, content []byte, options map[string]string, progress services.ProgressFunc) (map[string]interface{}, []string, error) {
		snapshotHTML := options["snapshot_html"] == "true"
		defaultVendor := options["default_vendor"]
		n, errs, err := importPurchaseBillsRows(userID, content, snapshotHTML, defaultVendor, progress)
		return map[string]interface{}{"imported": n}, errs, err
	})
	register("sales", func(userID uuid.UUID, content []byte, options map[string]string, progress services.ProgressFunc) (map[string]interface{}, []string, error) {
		n, errs, err := importSalesRows(userID, content, progress)
		return map[string]interface{}{"imported": n}, errs, err
	})
	register("payments", func(userID uuid.UUID, content []byte, options map[string]string, progress services.ProgressFunc) (map[string]interface{}, []string, error) {
		res, err := importPaymentsRows(userID, content, progress)
		if err != nil {
			return nil, nil, err
		}
		errs, _ := res["errors"].([]string)
		return res, errs, nil
	})
	register("expenses", func(userID uuid.UUID, content []byte, options map[string]string, progress services.ProgressFunc) (map[string]interface{}, []string, error) {
		n, errs, err := importExpensesRows(userID, content, progress)
		return map[string]interface{}{"imported": n}, errs, err
	})
	register("mybillbook", func(userID uuid.UUID, content []byte, options map[string]string, progress services.ProgressFunc) (map[string]interface{}, []string, error) {
		// ZIP import: content is the raw ZIP body. The runner returns the
		// steps summary as the result payload.
		result, errs, err := importMyBillBookZIPRows(userID, content, options, progress)
		return result, errs, err
	})

	// --- Generic invoice importer (invoice_import.go) ---
	register("invoices", func(userID uuid.UUID, content []byte, options map[string]string, progress services.ProgressFunc) (map[string]interface{}, []string, error) {
		userName := options["user_name"]
		def := invoiceImportDefaults{
			status:  strings.TrimSpace(options["default_status"]),
			unit:    strings.TrimSpace(options["default_unit"]),
			taxRate: parseFloat(options["default_tax_rate"]),
		}
		// client IP / user agent are not available in the background context;
		// pass empty strings (they are only used for audit logging).
		return importInvoicesRows(userID, userName, content, def, "", "", progress)
	})

	// --- Products importer (product.go) ---
	register("products", func(userID uuid.UUID, content []byte, options map[string]string, progress services.ProgressFunc) (map[string]interface{}, []string, error) {
		_ = utils.EnsureDefaultCategories(utils.DB, userID)
		return importProductsRows(userID, content, progress)
	})
}

// -----------------------------------------------------------------------------
// HTTP handlers
// -----------------------------------------------------------------------------

// EnqueueMigrationJobHandler — POST /api/v1/migration/jobs
//
// Form fields:
//   - file:        the uploaded CSV or ZIP file (required)
//   - kind:        the migration kind (required; e.g. "parties", "sales",
//     "stock-summary", "mybillbook", "invoices", ...)
//   - <option>:   any additional form fields are forwarded to the runner as
//     options (e.g. default_status, snapshot_html, default_role)
//
// Returns 202 with { "job_id": "<uuid>", "status": "queued" } on success.
func EnqueueMigrationJobHandler(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	kind := strings.TrimSpace(c.PostForm("kind"))
	if kind == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "kind is required"})
		return
	}

	file, err := c.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no file uploaded"})
		return
	}
	src, err := file.Open()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "failed to open uploaded file"})
		return
	}
	defer src.Close()
	content, err := io.ReadAll(src)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "failed to read uploaded file"})
		return
	}

	// Collect all other form fields as options (skip the framework-reserved
	// ones and the file itself).
	options := map[string]string{}
	for key, vals := range c.Request.PostForm {
		if key == "kind" || key == "file" || len(vals) == 0 {
			continue
		}
		options[key] = vals[0]
	}
	// Carry the uploader's display name for invoice audit logging.
	if name, exists := c.Get("user_name"); exists {
		if s, ok := name.(string); ok && s != "" {
			options["user_name"] = s
		}
	}

	jobID, err := services.EnqueueMigrationJob(utils.DB, userID, kind, file.Filename, content, options)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"job_id": jobID, "status": "queued"})
}

// GetMigrationJobHandler — GET /api/v1/migration/jobs/:id
//
// Returns the current state of a migration job. For running jobs the live
// in-memory state is returned (may be ahead of the persisted DB row).
func GetMigrationJobHandler(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	jobID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid job id"})
		return
	}

	job, ok := services.GetMigrationJob(utils.DB, jobID, userID)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "job not found"})
		return
	}
	c.JSON(http.StatusOK, job)
}

// ListMigrationJobsHandler — GET /api/v1/migration/jobs
//
// Returns recent migration jobs for the authenticated user, newest first.
// Optional query: ?limit=N (default 50, max 100).
func ListMigrationJobsHandler(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	limit := 50
	if v := strings.TrimSpace(c.Query("limit")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}

	jobs, err := services.ListMigrationJobs(utils.DB, userID, limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to list jobs: %v", err)})
		return
	}
	c.JSON(http.StatusOK, gin.H{"jobs": jobs})
}

// ensure models import is referenced (used by services for the MigrationJob
// type, but kept here so this file is self-contained for tooling).
var _ = models.MigrationJob{}
