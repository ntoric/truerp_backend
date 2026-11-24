// Package apidocs serves the public API's OpenAPI specification and its
// human-readable documentation.
//
//   - GET /api/public/docs               — reference (Redoc)
//   - GET /api/public/docs/explorer      — interactive explorer (Swagger UI)
//   - GET /api/public/v1/openapi.yaml    — OpenAPI 3.1 spec (YAML)
//   - GET /api/public/v1/openapi.json    — OpenAPI 3.1 spec (JSON)
//
// The spec embeds the placeholder __BASE_URL__, replaced at serve time with
// PUBLIC_API_BASE_URL (or the request's scheme and host).
package apidocs

import (
	_ "embed"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/goccy/go-yaml"
)

//go:embed openapi.yaml
var specYAML string

//go:embed reference.html
var referenceHTML string

//go:embed explorer.html
var explorerHTML string

const basePlaceholder = "__BASE_URL__"

// SpecYAML returns the raw embedded specification (with the placeholder).
func SpecYAML() string { return specYAML }

func baseURL(c *gin.Context) string {
	if base := strings.TrimRight(strings.TrimSpace(os.Getenv("PUBLIC_API_BASE_URL")), "/"); base != "" {
		return base
	}
	scheme := "http"
	if c.Request.TLS != nil || strings.EqualFold(c.GetHeader("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	return fmt.Sprintf("%s://%s", scheme, c.Request.Host)
}

func render(c *gin.Context, s string) string {
	return strings.ReplaceAll(s, basePlaceholder, baseURL(c))
}

var (
	jsonOnce sync.Once
	specJSON string
	jsonErr  error
)

// SpecJSON converts the YAML spec to JSON once.
func SpecJSON() (string, error) {
	jsonOnce.Do(func() {
		b, err := yaml.YAMLToJSON([]byte(specYAML))
		specJSON, jsonErr = string(b), err
	})
	return specJSON, jsonErr
}

func docHeaders(c *gin.Context) {
	c.Header("Cache-Control", "public, max-age=300")
	c.Header("Access-Control-Allow-Origin", "*")
}

func ServeSpecYAML(c *gin.Context) {
	docHeaders(c)
	c.Data(http.StatusOK, "application/yaml; charset=utf-8", []byte(render(c, specYAML)))
}

func ServeSpecJSON(c *gin.Context) {
	js, err := SpecJSON()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "spec unavailable"})
		return
	}
	docHeaders(c)
	c.Data(http.StatusOK, "application/json; charset=utf-8", []byte(render(c, js)))
}

func ServeReference(c *gin.Context) {
	c.Header("Cache-Control", "public, max-age=300")
	c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(render(c, referenceHTML)))
}

func ServeExplorer(c *gin.Context) {
	c.Header("Cache-Control", "public, max-age=300")
	c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(render(c, explorerHTML)))
}
