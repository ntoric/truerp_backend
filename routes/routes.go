package routes

import (
	"truerp/controllers"
	"truerp/middleware"

	"github.com/gin-gonic/gin"
)

func SetupRoutes(r *gin.Engine) {
	// Health check
	r.GET("/health", func(c *gin.Context) {
		c.JSON(200, gin.H{"status": "ok"})
	})

	// Auth routes
	auth := r.Group("/api/v1/auth")
	{
		auth.POST("/register", controllers.Register)
		auth.POST("/login", controllers.Login)
		auth.POST("/forgot-password", controllers.ForgotPassword)
		auth.POST("/verify-reset-otp", controllers.VerifyResetOTP)
		auth.POST("/reset-password", controllers.ResetPassword)
		auth.POST("/set-password", middleware.AuthRequired(), controllers.SetPassword)
		auth.GET("/profile", middleware.AuthRequired(), controllers.GetProfile)
		auth.PUT("/profile", middleware.AuthRequired(), controllers.UpdateProfile)
		auth.GET("/my-stores", middleware.AuthRequired(), controllers.MyStores)
	}

	// Store management (super admin)
	stores := r.Group("/api/v1/stores")
	stores.Use(middleware.AuthRequired(), middleware.SuperAdminRequired())
	{
		stores.GET("", controllers.ListStores)
		stores.POST("", controllers.CreateStore)
		stores.GET("/:id", controllers.GetStore)
		stores.PUT("/:id", controllers.UpdateStore)
		stores.DELETE("/:id", controllers.DeleteStore)
		stores.POST("/:id/reset", controllers.ResetStore)
		stores.GET("/:id/users", controllers.ListStoreUsers)
		stores.POST("/:id/users", controllers.CreateStoreUser)
	}

	// Business routes
	business := r.Group("/api/v1/business")
	business.Use(middleware.AuthRequired())
	{
		business.GET("", controllers.GetBusiness)
		business.PUT("", controllers.UpdateBusiness)
		business.POST("/upload-logo", controllers.UploadLogo)
		business.DELETE("/logo", controllers.RemoveLogo)
		business.POST("/upload-signature", controllers.UploadSignature)
	}

	// Invoice routes
	invoices := r.Group("/api/v1/invoices")
	invoices.Use(middleware.AuthRequired())
	{
		invoices.GET("", controllers.GetInvoices)
		invoices.POST("", controllers.CreateInvoice)
		invoices.POST("/import/csv", controllers.ImportInvoicesCSV)
		invoices.GET("/stats", controllers.GetInvoiceStats)
		invoices.GET("/next-number", controllers.GetNextInvoiceNumber)
		invoices.GET("/:id/status-history", controllers.GetInvoiceStatusHistory)
		invoices.PATCH("/:id/status", controllers.UpdateInvoiceStatus)
		invoices.GET("/:id/attachments", controllers.GetInvoiceAttachments)
		invoices.POST("/:id/attachments", controllers.UploadInvoiceAttachment)
		invoices.DELETE("/:id/attachments/:attachmentId", controllers.DeleteInvoiceAttachment)
		invoices.GET("/:id/pdf", controllers.GenerateInvoicePDF)
		invoices.GET("/:id", controllers.GetInvoice)
		invoices.PUT("/:id", controllers.UpdateInvoice)
		invoices.DELETE("/:id", controllers.DeleteInvoice)
	}

	invoiceTemplates := r.Group("/api/v1/invoice-templates")
	invoiceTemplates.Use(middleware.AuthRequired())
	{
		invoiceTemplates.GET("", controllers.GetSavedInvoiceTemplates)
		invoiceTemplates.POST("", controllers.CreateSavedInvoiceTemplate)
		invoiceTemplates.PUT("/:id", controllers.UpdateSavedInvoiceTemplate)
		invoiceTemplates.DELETE("/:id", controllers.DeleteSavedInvoiceTemplate)
	}

	// Quotation routes
	quotations := r.Group("/api/v1/quotations")
	quotations.Use(middleware.AuthRequired())
	{
		quotations.GET("", controllers.GetQuotations)
		quotations.POST("", controllers.CreateQuotation)
		quotations.GET("/:id", controllers.GetQuotation)
		quotations.PUT("/:id", controllers.UpdateQuotation)
		quotations.DELETE("/:id", controllers.DeleteQuotation)
		quotations.GET("/next-number", controllers.GetNextQuotationNumber)
		quotations.POST("/:id/approve", controllers.ApproveQuotation)
		quotations.POST("/:id/convert", controllers.ConvertToInvoice)
		quotations.GET("/:id/versions", controllers.GetQuotationVersions)
		quotations.POST("/:id/send", controllers.SendQuotation)
		quotations.POST("/:id/accept", controllers.AcceptQuotation)
		quotations.POST("/:id/reject", controllers.RejectQuotation)
		quotations.GET("/:id/pdf", controllers.GenerateQuotationPDF)
	}

	// Customer Statement routes
	statements := r.Group("/api/v1/customer-statements")
	statements.Use(middleware.AuthRequired())
	{
		statements.POST("", controllers.GenerateCustomerStatement)
		statements.GET("", controllers.GetCustomerStatements)
		statements.GET("/:id", controllers.GetCustomerStatement)
		statements.DELETE("/:id", controllers.DeleteCustomerStatement)
		statements.GET("/:id/pdf", controllers.GenerateStatementPDF)
		statements.GET("/aging-report", controllers.GetAgingReport)
		statements.GET("/parties/:id/aging", controllers.GetPartyAging)
	}

	// Payment routes
	payments := r.Group("/api/v1/payments")
	payments.Use(middleware.AuthRequired())
	{
		payments.GET("", controllers.GetPayments)
		payments.POST("", controllers.CreatePayment)
		payments.DELETE("/:id", controllers.DeletePayment)
		payments.GET("/:id/pdf", controllers.GenerateReceiptPDF)
	}

	// Document generation routes
	documents := r.Group("/api/v1/documents")
	documents.Use(middleware.AuthRequired())
	{
		documents.POST("/bulk-export", controllers.BulkExportPDFs)
		documents.POST("/barcode", controllers.GenerateBarcode)
		documents.POST("/qrcode", controllers.GenerateQRCode)
	}

	// Payment Out routes
	paymentOuts := r.Group("/api/v1/payment-outs")
	paymentOuts.Use(middleware.AuthRequired())
	{
		paymentOuts.GET("", controllers.GetPaymentOuts)
		paymentOuts.POST("", controllers.CreatePaymentOut)
		paymentOuts.DELETE("/:id", controllers.DeletePaymentOut)
	}

	// Expense routes
	expenses := r.Group("/api/v1/expenses")
	expenses.Use(middleware.AuthRequired())
	{
		expenses.GET("", controllers.GetExpenses)
		expenses.POST("", controllers.CreateExpense)
		expenses.GET("/next-number", controllers.GetNextExpenseNumber)
		expenses.PUT("/:id", controllers.UpdateExpense)
		expenses.DELETE("/:id", controllers.DeleteExpense)
	}

	// Expense category routes (separate from product categories)
	expenseCategories := r.Group("/api/v1/expense-categories")
	expenseCategories.Use(middleware.AuthRequired())
	{
		expenseCategories.GET("", controllers.GetExpenseCategories)
		expenseCategories.POST("", controllers.CreateExpenseCategory)
		expenseCategories.GET("/:id", controllers.GetExpenseCategory)
		expenseCategories.PUT("/:id", controllers.UpdateExpenseCategory)
		expenseCategories.DELETE("/:id", controllers.DeleteExpenseCategory)
	}

	// Dashboard routes
	dashboard := r.Group("/api/v1/dashboard")
	dashboard.Use(middleware.AuthRequired())
	{
		dashboard.GET("/stats", controllers.GetDashboardStats)
		dashboard.GET("/recent-invoices", controllers.GetRecentInvoices)
		dashboard.GET("/recent-payments", controllers.GetRecentPayments)
		dashboard.GET("/sales-report", controllers.GetSalesReport)
		dashboard.GET("/gst-report", controllers.GetGSTReport)
		dashboard.GET("/top-parties", controllers.GetTopParties)
		dashboard.GET("/daily-report", controllers.GetDailyReport)
		dashboard.GET("/daily-report/export", controllers.ExportDailyReportCSV)
		dashboard.GET("/daily-report/pdf", controllers.ExportDailyReportPDF)
		dashboard.GET("/periodic-report", controllers.GetPeriodicReport)
		dashboard.GET("/periodic-report/export", controllers.ExportPeriodicReportCSV)
		dashboard.GET("/periodic-report/pdf", controllers.ExportPeriodicReportPDF)
	}

	reports := r.Group("/api/v1/reports")
	reports.Use(middleware.AuthRequired())
	{
		reports.GET("/widgets", controllers.GetReportWidgets)
		reports.GET("/sales", controllers.GetSalesReportDetailed)
		reports.GET("/tax", controllers.GetTaxReport)
		reports.GET("/revenue", controllers.GetRevenueReport)
		reports.GET("/outstanding", controllers.GetOutstandingInvoicesReport)
		reports.GET("/customers", controllers.GetCustomerWiseReport)
		reports.GET("/products", controllers.GetProductWiseReport)
		reports.GET("/categories", controllers.GetCategoryWiseReport)
		reports.GET("/payments", controllers.GetPaymentsReport)
		reports.GET("/inventory", controllers.GetInventoryReport)
		reports.GET("/custom", controllers.GetCustomReport)
	}

	// Category routes
	categories := r.Group("/api/v1/categories")
	categories.Use(middleware.AuthRequired())
	{
		categories.GET("", controllers.GetCategories)
		categories.POST("", controllers.CreateCategory)
		categories.POST("/bulk/delete", controllers.BulkDeleteCategories)
		categories.POST("/bulk/update-status", controllers.BulkUpdateCategoryStatus)
		categories.GET("/:id", controllers.GetCategory)
		categories.PUT("/:id", controllers.UpdateCategory)
		categories.DELETE("/:id", controllers.DeleteCategory)
	}

	// POS Session routes
	posSessions := r.Group("/api/v1/pos/sessions")
	posSessions.Use(middleware.AuthRequired())
	{
		posSessions.GET("", controllers.GetPOSSessions)
		posSessions.POST("", controllers.OpenPOSSession)
		posSessions.GET("/active", controllers.GetActivePOSSession)
		posSessions.GET("/:id", controllers.GetPOSSession)
		posSessions.POST("/:id/close", controllers.ClosePOSSession)
		posSessions.GET("/:id/summary", controllers.GetPOSSessionSummary)
		posSessions.GET("/:id/cash-movements", controllers.GetCashMovements)
		posSessions.POST("/:id/cash-movements", controllers.AddCashMovement)
	}

	// POS Draft routes for multi-tab and draft support
	posDrafts := r.Group("/api/v1/pos/drafts")
	posDrafts.Use(middleware.AuthRequired())
	{
		posDrafts.GET("", controllers.GetPOSDrafts)
		posDrafts.POST("", controllers.CreatePOSDraft)
		posDrafts.GET("/:id", controllers.GetPOSDraft)
		posDrafts.PUT("/:id", controllers.UpdatePOSDraft)
		posDrafts.DELETE("/:id", controllers.DeletePOSDraft)
		posDrafts.POST("/:id/convert", controllers.ConvertDraftToInvoice)
	}

	// Inventory routes
	inventory := r.Group("/api/v1/inventory")
	inventory.Use(middleware.AuthRequired())
	{
		// Stock Balance & Valuation
		inventory.GET("/balance", controllers.GetStockBalance)
		inventory.GET("/valuation", controllers.GetInventoryValuation)

		// Stock Entries
		inventory.GET("/entries", controllers.GetStockEntries)
		inventory.POST("/entries", controllers.CreateStockEntry)
		inventory.POST("/entries/approve-all", controllers.ApproveAllPendingStockEntries)
		inventory.PUT("/entries/:id", controllers.UpdateStockEntry)
		inventory.POST("/entries/:id/approve", controllers.ApproveStockEntry)
		inventory.POST("/entries/:id/reject", controllers.RejectStockEntry)

		// Stock Transfers
		inventory.GET("/transfers", controllers.GetStockTransfers)
		inventory.POST("/transfers", controllers.CreateStockTransfer)
		inventory.GET("/transfers/:id", controllers.GetStockTransfer)
		inventory.POST("/transfers/:id/submit", controllers.SubmitStockTransfer)
		inventory.POST("/transfers/:id/receive", controllers.ReceiveStockTransfer)

		// Inventory Stock Management
		inventory.GET("/stocks", controllers.GetInventoryStocks)
		inventory.GET("/stocks/products/:id", controllers.GetProductStock)
		inventory.GET("/stocks/search", controllers.SearchByItemCode)
		inventory.POST("/stocks/adjust", controllers.AdjustStock)
		inventory.POST("/stocks/bulk-update/csv", controllers.BulkUpdateStockCSV)
		inventory.POST("/stocks/bulk-update/excel", controllers.BulkUpdateStockExcel)
		inventory.POST("/stocks/reserve", controllers.ReserveStock)
		inventory.POST("/stocks/release", controllers.ReleaseStock)
		inventory.GET("/alerts/low-stock", controllers.GetLowStockAlerts)

		// Inventory Items for dropdown selection
		inventory.GET("/items", controllers.GetInventoryItems)
	}

	// Serial Number routes
	serialNumbers := r.Group("/api/v1/serial-numbers")
	serialNumbers.Use(middleware.AuthRequired())
	{
		serialNumbers.GET("", controllers.GetSerialNumbers)
		serialNumbers.POST("", controllers.CreateSerialNumber)
		serialNumbers.POST("/bulk", controllers.BulkCreateSerialNumbers)
		serialNumbers.GET("/:id", controllers.GetSerialNumber)
		serialNumbers.PUT("/:id", controllers.UpdateSerialNumber)
		serialNumbers.DELETE("/:id", controllers.DeleteSerialNumber)
	}

	// Warehouse routes
	warehouses := r.Group("/api/v1/warehouses")
	warehouses.Use(middleware.AuthRequired())
	{
		warehouses.GET("", controllers.GetWarehouses)
		warehouses.POST("", controllers.CreateWarehouse)
		warehouses.POST("/bulk/delete", controllers.BulkDeleteWarehouses)
		warehouses.POST("/bulk/update-status", controllers.BulkUpdateWarehouseStatus)
		warehouses.GET("/:id", controllers.GetWarehouse)
		warehouses.PUT("/:id", controllers.UpdateWarehouse)
		warehouses.DELETE("/:id", controllers.DeleteWarehouse)
		warehouses.GET("/:id/stock", controllers.GetWarehouseStock)
	}

	// Purchase routes
	purchase := r.Group("/api/v1/purchase")
	purchase.Use(middleware.AuthRequired())
	{
		purchase.GET("/orders", controllers.GetPurchaseOrders)
		purchase.POST("/orders", controllers.CreatePurchaseOrder)
		purchase.GET("/orders/:id", controllers.GetPurchaseOrder)
		purchase.PUT("/orders/:id", controllers.UpdatePurchaseOrder)
		purchase.POST("/orders/:id/submit", controllers.SubmitPurchaseOrder)
		purchase.DELETE("/orders/:id", controllers.DeletePurchaseOrder)
		purchase.GET("/receipts", controllers.GetPurchaseReceipts)
		purchase.POST("/receipts", controllers.CreatePurchaseReceipt)
		purchase.GET("/receipts/:id", controllers.GetPurchaseReceipt)
		purchase.POST("/receipts/:id/submit", controllers.SubmitPurchaseReceipt)
		purchase.GET("/bills", controllers.GetPurchaseBills)
		purchase.POST("/bills", controllers.CreatePurchaseBill)
		purchase.GET("/bills/stats", controllers.GetPurchaseBillStats)
		purchase.GET("/bills/:id", controllers.GetPurchaseBill)
		purchase.GET("/bills/:id/pdf", controllers.GeneratePurchaseBillPDF)
		purchase.GET("/bills/:id/download-pdf", controllers.DownloadPurchaseBillPDF)
		purchase.PUT("/bills/:id", controllers.UpdatePurchaseBill)
		purchase.DELETE("/bills/:id", controllers.DeletePurchaseBill)
		purchase.POST("/bills/labels", controllers.PrintPurchaseBillLabels)
		purchase.POST("/parse-bill-ai", controllers.ParseBillWithAI)
	}

	// GST routes
	gst := r.Group("/api/v1/gst")
	gst.Use(middleware.AuthRequired())
	{
		gst.GET("/summary", controllers.GetGSTSummary)
		gst.GET("/gstr1", controllers.GetGSTR1)
		gst.GET("/gstr2", controllers.GetGSTR2)
		gst.GET("/gstr3b", controllers.GetGSTR3B)
		gst.POST("/einvoice/generate", controllers.GenerateEInvoice)
		gst.POST("/einvoice/cancel", controllers.CancelEInvoice)
		gst.GET("/einvoice/history", controllers.GetEInvoiceHistory)
		gst.POST("/ewaybill/generate", controllers.GenerateEWayBill)
		gst.POST("/ewaybill/cancel", controllers.CancelEWayBill)
		gst.GET("/ewaybill/history", controllers.GetEWayBillHistory)
		gst.GET("/hsn-rates", controllers.GetHSNRates)
		gst.GET("/hsn-search", controllers.SearchHSNCodes)
		
		// GST Compliance Routes
		gst.POST("/validate-gstin", controllers.ValidateGSTIN)
		gst.GET("/state-codes", controllers.GetStateCodes)
		
		// Tax Period Management
		gst.POST("/tax-periods", controllers.CreateTaxPeriod)
		gst.GET("/tax-periods", controllers.GetTaxPeriods)
		gst.GET("/tax-periods/:id", controllers.GetTaxPeriod)
		gst.PUT("/tax-periods/:id", controllers.UpdateTaxPeriod)
		
		// Input Tax Credit (ITC)
		gst.POST("/itc", controllers.CreateITC)
		gst.GET("/itc", controllers.GetITC)
		gst.PUT("/itc/:id/utilize", controllers.UtilizeITC)
		
		// GST Filing
		gst.POST("/filing", controllers.RecordGSTFiling)
		gst.GET("/filing", controllers.GetGSTFilingStatus)
		
		// GST Portal Exports
		gst.GET("/export/gstr1", controllers.GenerateGSTR1Export)
		gst.GET("/export/gstr3b", controllers.GenerateGSTR3BExport)
		
		// Tax Calculation
		gst.POST("/calculate", controllers.CalculateTax)
		gst.POST("/convert-price", controllers.ConvertPrice)
	}

	// HSN Semantic Search routes (public, no auth required)
	hsn := r.Group("/api/v1/hsn")
	{
		hsn.POST("/search", controllers.SearchHSNHandler)
		hsn.GET("/health", controllers.HSNHealthHandler)
	}

	// Tax Exemption routes
	taxExemptions := r.Group("/api/v1/tax-exemptions")
	taxExemptions.Use(middleware.AuthRequired())
	{
		taxExemptions.GET("", controllers.GetTaxExemptions)
		taxExemptions.POST("", controllers.CreateTaxExemption)
		taxExemptions.GET("/:id", controllers.GetTaxExemption)
		taxExemptions.PUT("/:id", controllers.UpdateTaxExemption)
		taxExemptions.DELETE("/:id", controllers.DeleteTaxExemption)
	}

	// Tax Rule routes
	taxRules := r.Group("/api/v1/tax-rules")
	taxRules.Use(middleware.AuthRequired())
	{
		taxRules.GET("", controllers.GetTaxRules)
		taxRules.POST("", controllers.CreateTaxRule)
		taxRules.GET("/applicable", controllers.GetApplicableTaxRule)
		taxRules.GET("/:id", controllers.GetTaxRule)
		taxRules.PUT("/:id", controllers.UpdateTaxRule)
		taxRules.DELETE("/:id", controllers.DeleteTaxRule)
	}

	// Tax Rate routes
	taxRates := r.Group("/api/v1/tax-rates")
	taxRates.Use(middleware.AuthRequired())
	{
		taxRates.GET("", controllers.GetTaxRates)
		taxRates.POST("", controllers.CreateTaxRate)
		taxRates.GET("/default", controllers.GetDefaultTaxRate)
		taxRates.GET("/:id", controllers.GetTaxRate)
		taxRates.PUT("/:id", controllers.UpdateTaxRate)
		taxRates.DELETE("/:id", controllers.DeleteTaxRate)
	}

	// Accounting routes
	accounting := r.Group("/api/v1/accounting")
	accounting.Use(middleware.AuthRequired())
	{
		accounting.GET("/accounts", controllers.GetAccounts)
		accounting.POST("/accounts", controllers.CreateAccount)
		accounting.GET("/accounts/:id", controllers.GetAccount)
		accounting.PUT("/accounts/:id", controllers.UpdateAccount)
		accounting.DELETE("/accounts/:id", controllers.DeleteAccount)
		accounting.GET("/journal", controllers.GetJournalEntries)
		accounting.POST("/journal", controllers.CreateJournalEntry)
		accounting.GET("/journal/:id", controllers.GetJournalEntry)
		accounting.PUT("/journal/:id", controllers.UpdateJournalEntry)
		accounting.POST("/journal/:id/post", controllers.PostJournalEntry)
		accounting.DELETE("/journal/:id", controllers.DeleteJournalEntry)
		
		// Financial Reports
		accounting.GET("/trial-balance", controllers.GetTrialBalance)
		accounting.GET("/profit-loss", controllers.GetProfitLoss)
		accounting.GET("/balance-sheet", controllers.GetBalanceSheet)
		accounting.GET("/general-ledger/:id", controllers.GetGeneralLedger)
		accounting.GET("/ledgers", controllers.GetLedgers)
		
		// Bank Reconciliation
		accounting.POST("/bank-reconciliation", controllers.CreateBankReconciliation)
		accounting.GET("/bank-reconciliation", controllers.GetBankReconciliations)
		accounting.PUT("/bank-reconciliation/:id/complete", controllers.CompleteBankReconciliation)
	}

	// Notification routes
	notifications := r.Group("/api/v1/notifications")
	notifications.Use(middleware.AuthRequired())
	{
		notifications.GET("", controllers.GetNotifications)
		notifications.POST("", controllers.CreateNotification)
		notifications.GET("/:id", controllers.GetNotification)
		notifications.PUT("/:id/read", controllers.MarkAsRead)
		notifications.PUT("/read-all", controllers.MarkAllAsRead)
		notifications.POST("/:id/send", controllers.SendNotification)
		notifications.DELETE("/:id", controllers.DeleteNotification)
		
		// Notification Templates
		notifications.GET("/templates", controllers.GetNotificationTemplates)
		notifications.POST("/templates", controllers.CreateNotificationTemplate)
		notifications.PUT("/templates/:id", controllers.UpdateNotificationTemplate)
		notifications.DELETE("/templates/:id", controllers.DeleteNotificationTemplate)
		
		// Notification Preferences
		notifications.GET("/preferences", controllers.GetNotificationPreferences)
		notifications.PUT("/preferences/:type", controllers.UpdateNotificationPreference)
		
		// Automation Endpoints
		notifications.POST("/send-invoice-due-reminders", controllers.SendInvoiceDueReminders)
		notifications.POST("/send-payment-reminders", controllers.SendPaymentReminders)
		notifications.POST("/send-overdue-notifications", controllers.SendOverdueNotifications)
	}

	// Compliance & Security routes
	compliance := r.Group("/api/v1/compliance")
	compliance.Use(middleware.AuthRequired())
	{
		// Audit Logs
		compliance.GET("/audit-logs", controllers.GetComplianceAuditLogs)
		compliance.GET("/audit-logs/stats", controllers.GetAuditLogStats)
		
		// Role & Permission Management
		compliance.GET("/roles", controllers.GetRoles)
		compliance.POST("/roles", controllers.CreateRole)
		compliance.PUT("/roles/:id", controllers.UpdateRole)
		compliance.DELETE("/roles/:id", controllers.DeleteRole)
		compliance.GET("/permissions", controllers.GetPermissions)
		
		// IP Restrictions
		compliance.GET("/ip-restrictions", controllers.GetIPRestrictions)
		compliance.POST("/ip-restrictions", controllers.CreateIPRestriction)
		compliance.DELETE("/ip-restrictions/:id", controllers.DeleteIPRestriction)
		
		// Backup & Restore
		compliance.POST("/backups", controllers.CreateBackup)
		compliance.GET("/backups", controllers.GetBackups)
		compliance.POST("/backups/:id/restore", controllers.RestoreBackup)
		
		// GDPR Compliance
		compliance.POST("/gdpr-requests", controllers.CreateGDPRRequest)
		compliance.GET("/gdpr-requests", controllers.GetGDPRRequests)
		compliance.PUT("/gdpr-requests/:id/process", controllers.ProcessGDPRRequest)
	}

	// Loyalty program routes
	loyalty := r.Group("/api/v1/loyalty")
	loyalty.Use(middleware.AuthRequired())
	{
		loyalty.GET("/settings", controllers.GetLoyaltySettings)
		loyalty.PUT("/settings", controllers.UpdateLoyaltySettings)
		loyalty.GET("/stats", controllers.GetLoyaltyStats)
		loyalty.GET("/transactions", controllers.GetLoyaltyTransactions)
		loyalty.GET("/customers", controllers.GetLoyaltyCustomers)
		loyalty.GET("/parties/:party_id", controllers.GetPartyLoyaltyBalance)
		loyalty.POST("/parties/:party_id/adjust", controllers.AdjustPartyLoyalty)
		loyalty.POST("/calculate-redemption", controllers.CalculateLoyaltyRedemption)
	}

	// Party routes
	parties := r.Group("/api/v1/parties")
	parties.Use(middleware.AuthRequired())
	{
		parties.GET("", controllers.GetParties)
		parties.GET("/stats", controllers.GetPartyStats)
		parties.POST("", controllers.CreateParty)
		parties.GET("/:id", controllers.GetParty)
		parties.PUT("/:id", controllers.UpdateParty)
		parties.DELETE("/:id", controllers.DeleteParty)
		parties.POST("/bulk/delete", controllers.BulkDeleteParties)
		parties.POST("/bulk/update-category", controllers.BulkUpdatePartyCategory)
	}

	// Sales Return routes
	salesReturns := r.Group("/api/v1/sales-returns")
	salesReturns.Use(middleware.AuthRequired())
	{
		salesReturns.GET("", controllers.GetSalesReturns)
		salesReturns.POST("", controllers.CreateSalesReturn)
		salesReturns.GET("/:id", controllers.GetSalesReturn)
		salesReturns.PUT("/:id", controllers.UpdateSalesReturn)
		salesReturns.POST("/:id/process", controllers.ProcessSalesReturn)
		salesReturns.DELETE("/:id", controllers.DeleteSalesReturn)
	}

	// Delivery Challan routes
	deliveryChallans := r.Group("/api/v1/delivery-challans")
	deliveryChallans.Use(middleware.AuthRequired())
	{
		deliveryChallans.GET("", controllers.GetDeliveryChallans)
		deliveryChallans.POST("", controllers.CreateDeliveryChallan)
		deliveryChallans.GET("/next-number", controllers.GetNextChallanNumber)
		deliveryChallans.GET("/:id", controllers.GetDeliveryChallan)
		deliveryChallans.PUT("/:id", controllers.UpdateDeliveryChallan)
		deliveryChallans.DELETE("/:id", controllers.DeleteDeliveryChallan)
	}

	// Credit Note routes
	creditNotes := r.Group("/api/v1/credit-notes")
	creditNotes.Use(middleware.AuthRequired())
	{
		creditNotes.GET("", controllers.GetCreditNotes)
		creditNotes.POST("", controllers.CreateCreditNote)
		creditNotes.GET("/next-number", controllers.GetNextCreditNoteNumber)
		creditNotes.GET("/:id", controllers.GetCreditNote)
		creditNotes.PUT("/:id", controllers.UpdateCreditNote)
		creditNotes.POST("/:id/issue", controllers.IssueCreditNote)
		creditNotes.DELETE("/:id", controllers.DeleteCreditNote)
	}

	// Purchase Return routes
	purchaseReturns := r.Group("/api/v1/purchase-returns")
	purchaseReturns.Use(middleware.AuthRequired())
	{
		purchaseReturns.GET("", controllers.GetPurchaseReturns)
		purchaseReturns.POST("", controllers.CreatePurchaseReturn)
		purchaseReturns.GET("/:id", controllers.GetPurchaseReturn)
		purchaseReturns.PUT("/:id", controllers.UpdatePurchaseReturn)
		purchaseReturns.POST("/:id/process", controllers.ProcessPurchaseReturn)
		purchaseReturns.DELETE("/:id", controllers.DeletePurchaseReturn)
	}

	// Debit Note routes
	debitNotes := r.Group("/api/v1/debit-notes")
	debitNotes.Use(middleware.AuthRequired())
	{
		debitNotes.GET("", controllers.GetDebitNotes)
		debitNotes.POST("", controllers.CreateDebitNote)
		debitNotes.GET("/next-number", controllers.GetNextDebitNoteNumber)
		debitNotes.GET("/:id", controllers.GetDebitNote)
		debitNotes.PUT("/:id", controllers.UpdateDebitNote)
		debitNotes.POST("/:id/issue", controllers.IssueDebitNote)
		debitNotes.DELETE("/:id", controllers.DeleteDebitNote)
	}

	// Cash & Bank routes
	cashBank := r.Group("/api/v1/cash-bank")
	cashBank.Use(middleware.AuthRequired())
	{
		cashBank.GET("/summary", controllers.GetCashBankSummary)
		cashBank.GET("/accounts", controllers.GetBankAccounts)
		cashBank.POST("/accounts", controllers.CreateBankAccount)
		cashBank.GET("/accounts/:id", controllers.GetBankAccount)
		cashBank.PUT("/accounts/:id", controllers.UpdateBankAccount)
		cashBank.PUT("/accounts/:id/set-primary", controllers.SetPrimaryBankAccount)
		cashBank.DELETE("/accounts/:id", controllers.DeleteBankAccount)
		cashBank.GET("/payment-method-mappings", controllers.GetPaymentMethodMappings)
		cashBank.PUT("/payment-method-mappings", controllers.UpdatePaymentMethodMappings)
		cashBank.GET("/transactions", controllers.GetCashTransactions)
		cashBank.GET("/transactions/:id", controllers.GetCashTransaction)
		cashBank.POST("/transactions/add", controllers.AddMoney)
		cashBank.POST("/transactions/reduce", controllers.ReduceMoney)
		cashBank.POST("/transactions/transfer", controllers.TransferMoney)
		cashBank.DELETE("/transactions/:id", controllers.DeleteCashTransaction)
	}

	// Staff routes
	staff := r.Group("/api/v1/staff")
	staff.Use(middleware.AuthRequired())
	{
		staff.GET("", controllers.GetStaffs)
		staff.POST("", controllers.CreateStaff)
		staff.POST("/bulk/delete", controllers.BulkDeleteStaff)
		staff.POST("/bulk/update-status", controllers.BulkUpdateStaffStatus)
		staff.GET("/:id", controllers.GetStaff)
		staff.PUT("/:id", controllers.UpdateStaff)
		staff.DELETE("/:id", controllers.DeleteStaff)

		// Staff Deductions routes
		staff.GET("/deductions", controllers.GetStaffDeductions)
		staff.GET("/deductions/stats", controllers.GetStaffDeductionStats)
		staff.GET("/deductions/next-number", controllers.GetNextDeductionNumber)
		staff.POST("/deductions", controllers.CreateStaffDeduction)
		staff.GET("/deductions/:id", controllers.GetStaffDeduction)
		staff.PUT("/deductions/:id", controllers.UpdateStaffDeduction)
		staff.DELETE("/deductions/:id", controllers.DeleteStaffDeduction)

		// Staff Advance Payments routes
		staff.GET("/advances", controllers.GetStaffAdvancePayments)
		staff.GET("/advances/stats", controllers.GetStaffAdvanceStats)
		staff.GET("/advances/next-number", controllers.GetNextAdvanceNumber)
		staff.POST("/advances", controllers.CreateStaffAdvancePayment)
		staff.GET("/advances/:id", controllers.GetStaffAdvancePayment)
		staff.PUT("/advances/:id", controllers.UpdateStaffAdvancePayment)
		staff.POST("/advances/:id/recover", controllers.RecoverStaffAdvance)
		staff.DELETE("/advances/:id", controllers.DeleteStaffAdvancePayment)
	}

	// Attendance routes
	attendance := r.Group("/api/v1/attendance")
	attendance.Use(middleware.AuthRequired())
	{
		attendance.GET("", controllers.GetAttendance)
		attendance.GET("/stats", controllers.GetAttendanceStats)
		attendance.POST("", controllers.MarkAttendance)
		attendance.POST("/bulk", controllers.BulkMarkAttendance)
		attendance.POST("/bulk/delete", controllers.BulkDeleteAttendance)
		attendance.GET("/staff/:staff_id", controllers.GetStaffAttendance)
		attendance.DELETE("/:id", controllers.DeleteAttendance)
	}

	// Payroll routes
	payroll := r.Group("/api/v1/payroll")
	payroll.Use(middleware.AuthRequired())
	{
		payroll.GET("", controllers.GetPayrolls)
		payroll.GET("/stats", controllers.GetPayrollStats)
		payroll.GET("/next-number", controllers.GetNextPaymentNumber)
		payroll.POST("", controllers.CreatePayroll)
		payroll.POST("/bulk/delete", controllers.BulkDeletePayrolls)
		payroll.POST("/bulk/update-status", controllers.BulkUpdatePayrollStatus)
		payroll.GET("/:id", controllers.GetPayroll)
		payroll.PUT("/:id", controllers.UpdatePayroll)
		payroll.DELETE("/:id", controllers.DeletePayroll)
	}

	// Settings routes
	settings := r.Group("/api/v1/settings")
	settings.Use(middleware.AuthRequired())
	{
		// Invoice Settings
		settings.GET("/invoice", controllers.GetInvoiceSettings)
		settings.PUT("/invoice", controllers.UpdateInvoiceSettings)

		// App appearance (colour theme)
		settings.GET("/appearance", controllers.GetAppearanceSettings)
		settings.PUT("/appearance", controllers.UpdateAppearanceSettings)
		settings.GET("/invoice-custom-fields", controllers.GetInvoiceCustomFieldDefinitions)
		settings.POST("/invoice-custom-fields", controllers.CreateInvoiceCustomFieldDefinition)
		settings.PUT("/invoice-custom-fields/:id", controllers.UpdateInvoiceCustomFieldDefinition)
		settings.DELETE("/invoice-custom-fields/:id", controllers.DeleteInvoiceCustomFieldDefinition)
		
		// Print Settings
		settings.GET("/print", controllers.GetPrintSettings)
		settings.PUT("/print", controllers.UpdatePrintSettings)

		// Weighing scale settings
		settings.GET("/weighing-scale", controllers.GetWeighingScaleSettings)
		settings.PUT("/weighing-scale", controllers.UpdateWeighingScaleSettings)
		
		// Reminders
		settings.GET("/reminders", controllers.GetReminders)
		settings.POST("/reminders", controllers.CreateReminder)
		settings.PUT("/reminders/:id", controllers.UpdateReminder)
		settings.DELETE("/reminders/:id", controllers.DeleteReminder)
		
		// CA Report Sharing
		settings.GET("/ca-sharing", controllers.GetCAReportSharing)
		settings.POST("/ca-sharing", controllers.CreateCAReportSharing)
		settings.PUT("/ca-sharing/:id", controllers.UpdateCAReportSharing)
		settings.DELETE("/ca-sharing/:id", controllers.DeleteCAReportSharing)
		
		// Account Settings
		settings.POST("/change-password", controllers.ChangePassword)
		settings.POST("/users/:id/reset-password", controllers.ResetUserPassword)

		// User Management
		settings.GET("/users", controllers.GetBusinessUsers)
		settings.POST("/users", controllers.CreateBusinessUser)
		settings.PUT("/users/:id", controllers.UpdateBusinessUser)
		settings.DELETE("/users/:id", controllers.DeleteBusinessUser)
	}

	userMgmt := r.Group("/api/v1/user-management")
	userMgmt.Use(middleware.AuthRequired())
	{
		userMgmt.GET("/overview", controllers.GetUserManagementOverview)
		userMgmt.GET("/activity-logs", controllers.GetActivityLogs)
		userMgmt.GET("/2fa/status", controllers.GetTwoFactorStatus)
		userMgmt.POST("/2fa/setup", controllers.SetupTwoFactor)
		userMgmt.POST("/2fa/enable", controllers.EnableTwoFactor)
		userMgmt.POST("/2fa/disable", controllers.DisableTwoFactor)
	}

	// Products routes
	products := r.Group("/api/v1/products")
	products.Use(middleware.AuthRequired())
	{
		products.GET("", controllers.GetProducts)
		products.POST("", controllers.CreateProduct)
		products.GET("/generate-item-code", controllers.GenerateProductItemCode)
		products.GET("/next-plu", controllers.NextProductPLU)
		products.GET("/check-item-code", controllers.CheckProductItemCode)
		products.GET("/check-plu", controllers.CheckProductPLU)
		products.GET("/:id", controllers.GetProduct)
		products.PUT("/:id", controllers.UpdateProduct)
		products.DELETE("/:id", controllers.DeleteProduct)
		products.GET("/export/csv", controllers.ExportProductsCSV)
		products.GET("/export/excel", controllers.ExportProductsExcel)
		products.POST("/import/csv", controllers.ImportProductsCSV)
		products.POST("/import/excel", controllers.ImportProductsExcel)
		products.GET("/:id/print-label", controllers.PrintProductLabel)
		products.POST("/:id/print-label", controllers.PrintProductLabel)
		products.GET("/:id/images", controllers.GetProductImages)
		products.POST("/:id/images", controllers.CreateProductImage)
		products.GET("/:id/variants", controllers.GetProductVariants)
		products.POST("/:id/variants", controllers.CreateProductVariant)
	}

	// Product Image routes
	productImages := r.Group("/api/v1/product-images")
	productImages.Use(middleware.AuthRequired())
	{
		productImages.PUT("/:id", controllers.UpdateProductImage)
		productImages.DELETE("/:id", controllers.DeleteProductImage)
		productImages.POST("/:product-id/reorder", controllers.ReorderProductImages)
	}

	// Product Variant routes
	productVariants := r.Group("/api/v1/product-variants")
	productVariants.Use(middleware.AuthRequired())
	{
		productVariants.GET("/:id", controllers.GetProductVariant)
		productVariants.PUT("/:id", controllers.UpdateProductVariant)
		productVariants.DELETE("/:id", controllers.DeleteProductVariant)
	}

	// Drafts routes
	drafts := r.Group("/api/v1/drafts")
	drafts.Use(middleware.AuthRequired())
	{
		drafts.GET("", controllers.GetDrafts)
		drafts.POST("", controllers.CreateDraft)
		drafts.GET("/:id", controllers.GetDraft)
		drafts.PUT("/:id", controllers.UpdateDraft)
		drafts.DELETE("/:id", controllers.DeleteDraft)
	}

	// Sync routes for offline functionality
	sync := r.Group("/api/v1/sync")
	sync.Use(middleware.AuthRequired())
	{
		sync.POST("/queue", controllers.QueueOfflineOperation)
		sync.GET("/pending", controllers.GetPendingSyncs)
		sync.POST("/sync", controllers.SyncOfflineData)
		sync.POST("/clear", controllers.ClearSyncedItems)
		sync.GET("/status", controllers.GetSyncStatus)
	}

	// SMS Marketing routes
	smsMarketing := r.Group("/api/v1/sms-marketing")
	smsMarketing.Use(middleware.AuthRequired())
	{
		smsMarketing.GET("", controllers.GetSMSCampaigns)
		smsMarketing.GET("/stats", controllers.GetSMSStats)
		smsMarketing.POST("", controllers.CreateSMSCampaign)
		smsMarketing.GET("/:id", controllers.GetSMSCampaign)
		smsMarketing.PUT("/:id", controllers.UpdateSMSCampaign)
		smsMarketing.DELETE("/:id", controllers.DeleteSMSCampaign)
		smsMarketing.POST("/:id/send", controllers.SendSMSCampaign)
		smsMarketing.POST("/:id/schedule", controllers.ScheduleSMSCampaign)
	}

	// Email Marketing routes
	emailMarketing := r.Group("/api/v1/email-marketing")
	emailMarketing.Use(middleware.AuthRequired())
	{
		emailMarketing.GET("", controllers.GetEmailCampaigns)
		emailMarketing.GET("/stats", controllers.GetEmailStats)
		emailMarketing.POST("", controllers.CreateEmailCampaign)
		emailMarketing.GET("/:id", controllers.GetEmailCampaign)
		emailMarketing.PUT("/:id", controllers.UpdateEmailCampaign)
		emailMarketing.DELETE("/:id", controllers.DeleteEmailCampaign)
		emailMarketing.POST("/:id/send", controllers.SendEmailCampaign)
		emailMarketing.POST("/:id/schedule", controllers.ScheduleEmailCampaign)
	}

	// WhatsApp Marketing routes
	whatsappMarketing := r.Group("/api/v1/whatsapp-marketing")
	whatsappMarketing.Use(middleware.AuthRequired())
	{
		whatsappMarketing.GET("", controllers.GetWhatsAppCampaigns)
		whatsappMarketing.GET("/stats", controllers.GetWhatsAppStats)
		whatsappMarketing.POST("", controllers.CreateWhatsAppCampaign)
		whatsappMarketing.GET("/:id", controllers.GetWhatsAppCampaign)
		whatsappMarketing.PUT("/:id", controllers.UpdateWhatsAppCampaign)
		whatsappMarketing.DELETE("/:id", controllers.DeleteWhatsAppCampaign)
		whatsappMarketing.POST("/:id/send", controllers.SendWhatsAppCampaign)
		whatsappMarketing.POST("/:id/schedule", controllers.ScheduleWhatsAppCampaign)
	}

	// Developer Settings routes (super admin only)
	developerSettings := r.Group("/api/v1/developer-settings")
	developerSettings.Use(middleware.AuthRequired(), middleware.SuperAdminRequired())
	{
		developerSettings.GET("", controllers.GetDeveloperSettings)
		developerSettings.PUT("", controllers.UpdateDeveloperSettings)
		developerSettings.POST("/test-email", controllers.TestEmailConnection)
		developerSettings.POST("/test-whatsapp", controllers.TestWhatsAppConnection)
		developerSettings.POST("/test-sms", controllers.TestSMSConnection)
	}

	// Page / menu feature flags (read: any auth user; write: super admin)
	pageFeatures := r.Group("/api/v1/page-features")
	pageFeatures.Use(middleware.AuthRequired())
	{
		pageFeatures.GET("", controllers.GetPageFeatures)
		pageFeatures.PUT("", middleware.SuperAdminRequired(), controllers.UpdatePageFeatures)
	}

	// Audit Log routes (super admin only)
	audit := r.Group("/api/v1/audit")
	audit.Use(middleware.AuthRequired(), middleware.SuperAdminRequired())
	{
		audit.GET("/logs", controllers.GetAuditLogs)
		audit.GET("/logs/:id", controllers.GetAuditLog)
		audit.GET("/stats", controllers.GetAuditStats)
		audit.GET("/logs/export", controllers.ExportAuditLogs)
		audit.DELETE("/logs", controllers.DeleteAuditLogs)
		audit.GET("/retention", controllers.GetAuditRetentionSettings)
		audit.PUT("/retention", controllers.UpdateAuditRetentionSettings)
		audit.POST("/archive", controllers.ArchiveAuditLogs)
		audit.GET("/archives", controllers.GetArchivedAuditLogs)
	}

	// Printer routes (thermal + A4 document print)
	printer := r.Group("/api/v1/printer")
	printer.Use(middleware.AuthRequired())
	{
		printer.POST("/thermal", controllers.GenerateThermalPrint)
		printer.POST("/document", controllers.GenerateDocumentPrint)
		printer.GET("/thermal/preview", controllers.GetThermalPrintPreview)
		printer.GET("/barcode/preview", controllers.GetBarcodePrintPreview)
	}

	// Customer portal — business admin (super admin only)
	customerPortalAdmin := r.Group("/api/v1/customer-portal")
	customerPortalAdmin.Use(middleware.AuthRequired(), middleware.SuperAdminRequired())
	{
		customerPortalAdmin.GET("/settings", controllers.GetCustomerPortalSettings)
		customerPortalAdmin.PUT("/settings", controllers.UpdateCustomerPortalSettings)
		customerPortalAdmin.GET("/access", controllers.ListCustomerPortalAccess)
		customerPortalAdmin.PUT("/access/:party_id", controllers.UpsertCustomerPortalAccess)
		customerPortalAdmin.GET("/tickets", controllers.AdminListSupportTickets)
		customerPortalAdmin.PUT("/tickets/:id", controllers.AdminUpdateSupportTicket)
	}

	// Customer portal — public & customer session
	portalPublic := r.Group("/api/v1/portal")
	{
		portalPublic.GET("/public/:slug", controllers.GetPortalPublicInfo)
		portalPublic.POST("/login", controllers.PortalLogin)
	}

	portal := r.Group("/api/v1/portal")
	portal.Use(middleware.PortalAuthRequired())
	{
		portal.GET("/me", controllers.PortalGetProfile)
		portal.GET("/invoices", controllers.PortalListInvoices)
		portal.GET("/invoices/:id/pdf", controllers.PortalInvoicePDF)
		portal.GET("/invoices/:id", controllers.PortalGetInvoice)
		portal.GET("/payments", controllers.PortalListPayments)
		portal.GET("/loyalty", controllers.PortalGetLoyalty)
		portal.GET("/statements", controllers.PortalListStatements)
		portal.GET("/statements/:id/pdf", controllers.PortalStatementPDF)
		portal.GET("/tickets", controllers.PortalListTickets)
		portal.POST("/tickets", controllers.PortalCreateTicket)
	}
}
