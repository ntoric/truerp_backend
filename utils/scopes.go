package utils

import (
	"sort"
	"strings"
)

// Public API scopes. Each integration credential (API key or OAuth access
// token) carries a set of these; the effective permission for a request is
// the credential's scopes intersected with the store's integration policy.
//
// Legacy OAuth scopes:
//   - "erp" / "admin": full access to the internal /api/v1 API (used by the
//     MCP server). They also imply every public scope.
//   - "read" / "write": shorthands that expand to every *:read scope, and to
//     every scope respectively.
const (
	ScopePartiesRead    = "parties:read"
	ScopePartiesWrite   = "parties:write"
	ScopeProductsRead   = "products:read"
	ScopeProductsWrite  = "products:write"
	ScopeStockRead      = "stock:read"
	ScopeInvoicesRead   = "invoices:read"
	ScopeInvoicesWrite  = "invoices:write"
	ScopePaymentsRead   = "payments:read"
	ScopePaymentsWrite  = "payments:write"
	ScopePurchasesRead  = "purchases:read"
	ScopePurchasesWrite = "purchases:write"
	ScopeEventsRead     = "events:read"
	ScopeWebhooksManage = "webhooks:manage"

	ScopeLegacyRead  = "read"
	ScopeLegacyWrite = "write"
	ScopeInternalERP = "erp"
	ScopeAdmin       = "admin"
)

// ScopeInfo describes a public scope for documentation and admin UIs.
type ScopeInfo struct {
	Scope       string `json:"scope"`
	Resource    string `json:"resource"`
	Access      string `json:"access"` // read | write | manage
	Description string `json:"description"`
}

// PublicScopeCatalog lists every fine-grained public API scope.
var PublicScopeCatalog = []ScopeInfo{
	{ScopePartiesRead, "parties", "read", "List and read customers and vendors"},
	{ScopePartiesWrite, "parties", "write", "Create and update customers and vendors"},
	{ScopeProductsRead, "products", "read", "List and read products and services"},
	{ScopeProductsWrite, "products", "write", "Create and update products and services"},
	{ScopeStockRead, "stock", "read", "Read stock levels per product, warehouse and batch"},
	{ScopeInvoicesRead, "invoices", "read", "List and read sales invoices"},
	{ScopeInvoicesWrite, "invoices", "write", "Create and cancel sales invoices"},
	{ScopePaymentsRead, "payments", "read", "List and read payments received"},
	{ScopePaymentsWrite, "payments", "write", "Record payments received"},
	{ScopePurchasesRead, "purchases", "read", "List and read purchase bills"},
	{ScopePurchasesWrite, "purchases", "write", "Create purchase bills"},
	{ScopeEventsRead, "events", "read", "Poll the event feed"},
	{ScopeWebhooksManage, "webhooks", "manage", "Create, update and delete webhook endpoints"},
}

// AllPublicScopes returns every fine-grained public scope.
func AllPublicScopes() []string {
	out := make([]string, 0, len(PublicScopeCatalog))
	for _, s := range PublicScopeCatalog {
		out = append(out, s.Scope)
	}
	return out
}

// IsKnownScope reports whether s is a public scope or an accepted alias.
func IsKnownScope(s string) bool {
	switch s {
	case ScopeLegacyRead, ScopeLegacyWrite, ScopeInternalERP, ScopeAdmin:
		return true
	}
	for _, info := range PublicScopeCatalog {
		if info.Scope == s {
			return true
		}
	}
	return false
}

// ParseScopes splits a space- or comma-separated scope string.
func ParseScopes(raw string) []string {
	fields := strings.FieldsFunc(raw, func(r rune) bool { return r == ' ' || r == ',' || r == '\n' || r == '\t' })
	seen := map[string]bool{}
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		f = strings.TrimSpace(f)
		if f == "" || seen[f] {
			continue
		}
		seen[f] = true
		out = append(out, f)
	}
	return out
}

// JoinScopes renders scopes as a sorted, space-separated string.
func JoinScopes(scopes []string) string {
	cp := append([]string(nil), scopes...)
	sort.Strings(cp)
	return strings.Join(cp, " ")
}

// ExpandScopes resolves aliases into the set of fine-grained public scopes.
func ExpandScopes(scopes []string) map[string]bool {
	out := map[string]bool{}
	for _, s := range scopes {
		switch s {
		case ScopeInternalERP, ScopeAdmin, ScopeLegacyWrite:
			for _, all := range AllPublicScopes() {
				out[all] = true
			}
		case ScopeLegacyRead:
			for _, info := range PublicScopeCatalog {
				if info.Access == "read" {
					out[info.Scope] = true
				}
			}
		default:
			out[s] = true
		}
	}
	return out
}

// GrantsInternalAPI reports whether a scope set allows calling the internal
// /api/v1 API (the MCP server's full-access mode).
func GrantsInternalAPI(scopes []string) bool {
	for _, s := range scopes {
		if s == ScopeInternalERP || s == ScopeAdmin {
			return true
		}
	}
	return false
}

// IntersectScopes keeps the scopes in want that are also granted by allowed
// (after alias expansion of both sides). Aliases in want are expanded.
func IntersectScopes(want []string, allowed []string) []string {
	allowedSet := ExpandScopes(allowed)
	wantSet := ExpandScopes(want)
	out := []string{}
	for _, s := range AllPublicScopes() {
		if wantSet[s] && allowedSet[s] {
			out = append(out, s)
		}
	}
	return out
}
