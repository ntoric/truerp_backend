package routes

import (
	"net/http"
	"truerp/apidocs"
	"truerp/controllers"
	"truerp/middleware"
	"truerp/utils"

	"github.com/gin-gonic/gin"
)

// setupPublicAPIRoutes mounts the public integration API, its documentation
// and the super-admin integration management endpoints.
func setupPublicAPIRoutes(r *gin.Engine) {
	// ---- documentation (no auth) ----
	r.GET("/api/public/docs", apidocs.ServeReference)
	r.GET("/api/public/docs/explorer", apidocs.ServeExplorer)
	r.GET("/api/public/v1/openapi.yaml", apidocs.ServeSpecYAML)
	r.GET("/api/public/v1/openapi.json", apidocs.ServeSpecJSON)

	// ---- public API ----
	v1 := r.Group("/api/public/v1")
	v1.Use(limitBody(2<<20), middleware.PublicAPIAuth())
	{
		scope := middleware.RequireScope
		idem := controllers.WithIdempotency

		v1.GET("/me", controllers.PublicGetMe)

		v1.GET("/parties", scope(utils.ScopePartiesRead), controllers.PublicListParties)
		v1.POST("/parties", scope(utils.ScopePartiesWrite), idem(controllers.PublicCreateParty))
		v1.GET("/parties/:id", scope(utils.ScopePartiesRead), controllers.PublicGetParty)
		v1.PATCH("/parties/:id", scope(utils.ScopePartiesWrite), idem(controllers.PublicUpdateParty))

		v1.GET("/products", scope(utils.ScopeProductsRead), controllers.PublicListProducts)
		v1.POST("/products", scope(utils.ScopeProductsWrite), idem(controllers.PublicCreateProduct))
		v1.GET("/products/:id", scope(utils.ScopeProductsRead), controllers.PublicGetProduct)
		v1.PATCH("/products/:id", scope(utils.ScopeProductsWrite), idem(controllers.PublicUpdateProduct))
		v1.GET("/products/:id/stock", scope(utils.ScopeStockRead), controllers.PublicGetProductStock)

		v1.GET("/stock", scope(utils.ScopeStockRead), controllers.PublicListStock)
		v1.GET("/warehouses", scope(utils.ScopeStockRead), controllers.PublicListWarehouses)

		v1.GET("/invoices", scope(utils.ScopeInvoicesRead), controllers.PublicListInvoices)
		v1.POST("/invoices", scope(utils.ScopeInvoicesWrite), idem(controllers.PublicCreateInvoice))
		v1.GET("/invoices/:id", scope(utils.ScopeInvoicesRead), controllers.PublicGetInvoice)
		v1.GET("/invoices/:id/pdf", scope(utils.ScopeInvoicesRead), controllers.PublicGetInvoicePDF)
		v1.POST("/invoices/:id/cancel", scope(utils.ScopeInvoicesWrite), idem(controllers.PublicCancelInvoice))

		v1.GET("/payments", scope(utils.ScopePaymentsRead), controllers.PublicListPayments)
		v1.POST("/payments", scope(utils.ScopePaymentsWrite), idem(controllers.PublicCreatePayment))
		v1.GET("/payments/:id", scope(utils.ScopePaymentsRead), controllers.PublicGetPayment)

		v1.GET("/purchase-bills", scope(utils.ScopePurchasesRead), controllers.PublicListPurchaseBills)
		v1.POST("/purchase-bills", scope(utils.ScopePurchasesWrite), idem(controllers.PublicCreatePurchaseBill))
		v1.GET("/purchase-bills/:id", scope(utils.ScopePurchasesRead), controllers.PublicGetPurchaseBill)

		v1.GET("/event-types", controllers.PublicListEventTypes)
		v1.GET("/events", scope(utils.ScopeEventsRead), controllers.PublicListEvents)
		v1.GET("/events/:id", scope(utils.ScopeEventsRead), controllers.PublicGetEvent)

		wh := scope(utils.ScopeWebhooksManage)
		v1.GET("/webhook-endpoints", wh, controllers.PublicListWebhooks)
		v1.POST("/webhook-endpoints", wh, idem(controllers.PublicCreateWebhook))
		v1.GET("/webhook-endpoints/:id", wh, controllers.PublicGetWebhook)
		v1.PATCH("/webhook-endpoints/:id", wh, controllers.PublicUpdateWebhook)
		v1.DELETE("/webhook-endpoints/:id", wh, controllers.PublicDeleteWebhook)
		v1.POST("/webhook-endpoints/:id/rotate-secret", wh, controllers.PublicRotateWebhookSecret)
		v1.POST("/webhook-endpoints/:id/test", wh, controllers.PublicTestWebhook)
		v1.GET("/webhook-endpoints/:id/deliveries", wh, controllers.PublicListWebhookDeliveries)
	}

	// ---- super admin: API management & permissions ----
	admin := r.Group("/api/v1/admin/integrations")
	admin.Use(middleware.AuthRequired(), middleware.SuperAdminRequired())
	{
		admin.GET("/catalog", controllers.AdminIntegrationCatalog)
		admin.GET("/overview", controllers.AdminIntegrationOverview)

		admin.GET("/api-keys", controllers.AdminListAPIKeys)
		admin.POST("/api-keys", controllers.AdminCreateAPIKey)
		admin.PATCH("/api-keys/:id", controllers.AdminUpdateAPIKey)
		admin.POST("/api-keys/:id/revoke", controllers.AdminRevokeAPIKey)
		admin.DELETE("/api-keys/:id", controllers.AdminDeleteAPIKey)

		admin.GET("/policies", controllers.AdminListIntegrationPolicies)
		admin.PUT("/policies/:store_id", controllers.AdminUpdateIntegrationPolicy)

		admin.GET("/oauth-clients", controllers.ListOAuthClients)
		admin.POST("/oauth-clients", controllers.CreateOAuthClient)
		admin.PATCH("/oauth-clients/:id", controllers.UpdateOAuthClient)
		admin.POST("/oauth-clients/:id/rotate-secret", controllers.RotateOAuthClientSecret)
		admin.POST("/oauth-clients/:id/revoke-tokens", controllers.RevokeOAuthClientTokens)
		admin.DELETE("/oauth-clients/:id", controllers.DeleteOAuthClient)

		admin.GET("/webhooks", controllers.AdminListWebhooks)
		admin.POST("/webhooks", controllers.AdminCreateWebhook)
		admin.PATCH("/webhooks/:id", controllers.AdminUpdateWebhook)
		admin.DELETE("/webhooks/:id", controllers.AdminDeleteWebhook)
		admin.POST("/webhooks/:id/test", controllers.AdminTestWebhook)
		admin.GET("/webhooks/:id/deliveries", controllers.AdminListWebhookDeliveries)
		admin.POST("/webhooks/:id/deliveries/:delivery_id/retry", controllers.AdminRetryWebhookDelivery)

		admin.GET("/logs", controllers.AdminListAPIRequestLogs)
	}
}

// limitBody caps request bodies for the public API.
func limitBody(max int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.ContentLength > max {
			utils.PublicAPIError(c, http.StatusRequestEntityTooLarge, "body_too_large", "Request body is too large")
			return
		}
		if c.Request.Body != nil {
			c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, max)
		}
		c.Next()
	}
}
