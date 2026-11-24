package controllers

// Shared plumbing for the public integration API (/api/public/v1).
//
// Design:
//   - Reads query the database directly and map rows to stable public
//     representations (public_representations.go), so internal model changes
//     do not leak to integrators.
//   - Writes translate the public request into the internal handler's input
//     and run that handler in-process (callInternal). Stock movements, party
//     balances, ledgers, numbering and audit logging therefore behave exactly
//     as they do for the web and mobile apps.

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"time"
	"truerp/models"
	"truerp/utils"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

const (
	publicDefaultLimit = 25
	publicMaxLimit     = 100
	idempotencyTTL     = 24 * time.Hour
)

// ---------- dates & numbers ----------

// publicLocation is the timezone used to render calendar dates.
func publicLocation(c *gin.Context) *time.Location {
	loc, _ := ConfiguredLocationForUser(c.MustGet("user_id").(uuid.UUID))
	if loc == nil {
		return time.Local
	}
	return loc
}

func publicDate(t time.Time, loc *time.Location) string {
	if t.IsZero() {
		return ""
	}
	return t.In(loc).Format("2006-01-02")
}

func publicDatePtr(t *time.Time, loc *time.Location) *string {
	if t == nil || t.IsZero() {
		return nil
	}
	s := publicDate(*t, loc)
	return &s
}

func publicTimestamp(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}

func publicTimestampPtr(t *time.Time) *string {
	if t == nil || t.IsZero() {
		return nil
	}
	s := publicTimestamp(*t)
	return &s
}

// parsePublicDate accepts YYYY-MM-DD (or RFC 3339) and returns the value the
// internal handlers expect: UTC midnight of that calendar day, the same thing
// the web app sends.
func parsePublicDate(raw string) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if d, err := time.Parse("2006-01-02", raw); err == nil {
		return d, nil
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC), nil
	}
	return time.Time{}, fmt.Errorf("invalid date %q: use YYYY-MM-DD", raw)
}

func round2(v float64) float64 {
	return math.Round(v*100) / 100
}

// ---------- pagination ----------

type publicPage struct {
	limit        int
	cursorTime   time.Time
	cursorID     uuid.UUID
	hasCursor    bool
	updatedSince *time.Time
	createdSince *time.Time
}

func encodeCursor(t time.Time, id uuid.UUID) string {
	raw := t.UTC().Format(time.RFC3339Nano) + "|" + id.String()
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeCursor(cursor string) (time.Time, uuid.UUID, error) {
	b, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return time.Time{}, uuid.Nil, errors.New("invalid cursor")
	}
	parts := strings.SplitN(string(b), "|", 2)
	if len(parts) != 2 {
		return time.Time{}, uuid.Nil, errors.New("invalid cursor")
	}
	t, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return time.Time{}, uuid.Nil, errors.New("invalid cursor")
	}
	id, err := uuid.Parse(parts[1])
	if err != nil {
		return time.Time{}, uuid.Nil, errors.New("invalid cursor")
	}
	return t, id, nil
}

func parseTimestampParam(c *gin.Context, name string) (*time.Time, bool) {
	raw := strings.TrimSpace(c.Query(name))
	if raw == "" {
		return nil, true
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return &t, true
	}
	if d, err := time.Parse("2006-01-02", raw); err == nil {
		return &d, true
	}
	utils.PublicAPIError(c, http.StatusBadRequest, "invalid_parameter", fmt.Sprintf("%s must be an RFC 3339 timestamp or YYYY-MM-DD date", name), gin.H{"param": name})
	return nil, false
}

// readPageParams parses limit, starting_after (cursor), updated_since and
// created_since. It writes the error response itself and returns false on
// invalid input.
func readPageParams(c *gin.Context) (publicPage, bool) {
	p := publicPage{limit: publicDefaultLimit}
	if raw := c.Query("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > publicMaxLimit {
			utils.PublicAPIError(c, http.StatusBadRequest, "invalid_parameter", fmt.Sprintf("limit must be between 1 and %d", publicMaxLimit), gin.H{"param": "limit"})
			return p, false
		}
		p.limit = n
	}
	if cur := c.Query("cursor"); cur != "" {
		t, id, err := decodeCursor(cur)
		if err != nil {
			utils.PublicAPIError(c, http.StatusBadRequest, "invalid_parameter", "cursor is invalid; pass next_cursor from the previous page", gin.H{"param": "cursor"})
			return p, false
		}
		// Compare in server-local time: SQLite stores timestamps as text in
		// the zone they were written in (Postgres compares instants).
		p.cursorTime, p.cursorID, p.hasCursor = t.In(time.Local), id, true
	}
	var ok bool
	if p.updatedSince, ok = parseTimestampParam(c, "updated_since"); !ok {
		return p, false
	}
	if p.createdSince, ok = parseTimestampParam(c, "created_since"); !ok {
		return p, false
	}
	return p, true
}

// applyPage orders newest first by (created_at, id) and applies the cursor.
// The table name qualifies columns so joins stay unambiguous.
func applyPage(q *gorm.DB, table string, p publicPage) *gorm.DB {
	if p.updatedSince != nil {
		q = q.Where(table+".updated_at >= ?", *p.updatedSince)
	}
	if p.createdSince != nil {
		q = q.Where(table+".created_at >= ?", *p.createdSince)
	}
	if p.hasCursor {
		q = q.Where("("+table+".created_at < ?) OR ("+table+".created_at = ? AND "+table+".id < ?)", p.cursorTime, p.cursorTime, p.cursorID)
	}
	return q.Order(table + ".created_at DESC").Order(table + ".id DESC").Limit(p.limit + 1)
}

// listEnvelope is the standard list response.
type listEnvelope struct {
	Object     string      `json:"object"`
	Data       interface{} `json:"data"`
	HasMore    bool        `json:"has_more"`
	NextCursor *string     `json:"next_cursor"`
}

// writeList trims the look-ahead row and writes the list envelope. keyOf
// returns (created_at, id) of item i.
func writeList(c *gin.Context, items []interface{}, limit int, keyOf func(i int) (time.Time, uuid.UUID)) {
	env := listEnvelope{Object: "list", Data: items}
	if len(items) > limit {
		env.HasMore = true
		env.Data = items[:limit]
		t, id := keyOf(limit - 1)
		cur := encodeCursor(t, id)
		env.NextCursor = &cur
	}
	if env.Data == nil {
		env.Data = []interface{}{}
	}
	c.JSON(http.StatusOK, env)
}

// ---------- path params ----------

func publicPathID(c *gin.Context, name string) (uuid.UUID, bool) {
	id, err := uuid.Parse(c.Param(name))
	if err != nil {
		utils.PublicAPIError(c, http.StatusNotFound, "resource_missing", "No such resource: "+c.Param(name))
		return uuid.Nil, false
	}
	return id, true
}

func notFound(c *gin.Context, kind string, id uuid.UUID) {
	utils.PublicAPIError(c, http.StatusNotFound, "resource_missing", fmt.Sprintf("No such %s: %s", kind, id))
}

func scopeUserID(c *gin.Context) uuid.UUID {
	return c.MustGet("user_id").(uuid.UUID)
}

func publicStoreID(c *gin.Context) uuid.UUID {
	id, _ := currentStoreID(c)
	return id
}

// bindPublicJSON decodes a request body strictly (unknown fields rejected)
// so integrators find typos instead of silently losing data.
func bindPublicJSON(c *gin.Context, dst interface{}) bool {
	dec := json.NewDecoder(c.Request.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		msg := err.Error()
		var syntaxErr *json.SyntaxError
		var typeErr *json.UnmarshalTypeError
		extra := gin.H{}
		switch {
		case errors.As(err, &syntaxErr):
			msg = "Request body is not valid JSON"
		case errors.As(err, &typeErr):
			msg = fmt.Sprintf("Field %q must be of type %s", typeErr.Field, typeErr.Type.String())
			extra["param"] = typeErr.Field
		case strings.HasPrefix(msg, "json: unknown field "):
			field := strings.Trim(strings.TrimPrefix(msg, "json: unknown field "), `"`)
			msg = fmt.Sprintf("Unknown field %q", field)
			extra["param"] = field
		case errors.Is(err, io.EOF):
			msg = "Request body is required"
		}
		utils.PublicAPIError(c, http.StatusBadRequest, "invalid_body", msg, extra)
		return false
	}
	return true
}

func paramError(c *gin.Context, param, message string) {
	utils.PublicAPIError(c, http.StatusBadRequest, "parameter_invalid", message, gin.H{"param": param})
}

// ---------- delegation to internal handlers ----------

type internalResult struct {
	Status int
	Body   []byte
}

func (r internalResult) ok() bool { return r.Status >= 200 && r.Status < 300 }

// callInternal runs an internal /api/v1 handler in-process with the current
// caller's identity and store scope, and captures its response.
func callInternal(c *gin.Context, handler gin.HandlerFunc, method string, params gin.Params, body interface{}) internalResult {
	var reader *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		reader = bytes.NewReader(b)
	} else {
		reader = bytes.NewReader(nil)
	}
	rec := httptest.NewRecorder()
	ictx, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(method, c.Request.URL.Path, reader)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", c.GetHeader("User-Agent"))
	req.RemoteAddr = c.Request.RemoteAddr
	if fwd := c.GetHeader("X-Forwarded-For"); fwd != "" {
		req.Header.Set("X-Forwarded-For", fwd)
	}
	ictx.Request = req.WithContext(c.Request.Context())
	ictx.Params = params
	for k, v := range c.Keys {
		ictx.Set(k, v)
	}
	handler(ictx)
	return internalResult{Status: rec.Code, Body: rec.Body.Bytes()}
}

// relayInternalError converts an internal handler's error response into the
// public error envelope.
func relayInternalError(c *gin.Context, r internalResult) {
	var payload struct {
		Error  string            `json:"error"`
		Fields map[string]string `json:"fields"`
	}
	_ = json.Unmarshal(r.Body, &payload)
	msg := strings.TrimSpace(payload.Error)
	if msg == "" {
		msg = http.StatusText(r.Status)
	}
	extra := gin.H{}
	if len(payload.Fields) > 0 {
		extra["fields"] = payload.Fields
		for k := range payload.Fields {
			extra["param"] = k
			break
		}
	}
	code := "request_failed"
	switch {
	case r.Status == http.StatusNotFound:
		code = "resource_missing"
	case r.Status == http.StatusConflict:
		code = "resource_conflict"
	case r.Status >= 500:
		code = "internal_error"
		msg = "An internal error occurred. Retry with the same Idempotency-Key."
	}
	utils.PublicAPIError(c, r.Status, code, msg, extra)
}

// createdID extracts the "id" of the resource an internal create returned.
func createdID(r internalResult) (uuid.UUID, error) {
	var payload struct {
		ID uuid.UUID `json:"id"`
	}
	if err := json.Unmarshal(r.Body, &payload); err != nil || payload.ID == uuid.Nil {
		return uuid.Nil, errors.New("internal handler returned no id")
	}
	return payload.ID, nil
}

// ---------- idempotency ----------

// WithIdempotency wraps a write handler. When the request carries an
// Idempotency-Key, the first response is stored for 24 hours and replayed for
// retries with the same key and body; reusing a key with a different body is
// rejected.
func WithIdempotency(handler gin.HandlerFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
		key := strings.TrimSpace(c.GetHeader("Idempotency-Key"))
		if key == "" {
			handler(c)
			return
		}
		if len(key) > 255 {
			utils.PublicAPIError(c, http.StatusBadRequest, "idempotency_key_invalid", "Idempotency-Key must be at most 255 characters")
			return
		}
		body, _ := readAndRestoreBody(c)
		sum := sha256.Sum256(append([]byte(c.Request.Method+" "+c.Request.URL.Path+"\n"), body...))
		reqHash := hex.EncodeToString(sum[:])
		storeID := publicStoreID(c)
		scopedKey := c.GetString(utils.CtxCredentialID) + ":" + key

		var existing models.APIIdempotencyRecord
		err := utils.DB.Where("store_id = ? AND key = ?", storeID, scopedKey).First(&existing).Error
		if err == nil && time.Since(existing.CreatedAt) < idempotencyTTL {
			if existing.RequestHash != reqHash {
				utils.PublicAPIError(c, http.StatusUnprocessableEntity, "idempotency_key_reused",
					"This Idempotency-Key was already used with a different request.")
				return
			}
			c.Header("Idempotent-Replayed", "true")
			c.Data(existing.StatusCode, "application/json; charset=utf-8", []byte(existing.ResponseBody))
			return
		}
		if err == nil {
			utils.DB.Delete(&existing) // expired
		}

		// Pass the key down so internal handlers with their own UUID-based
		// idempotency (invoices, purchase bills) also deduplicate.
		c.Set("public_idempotency_uuid", idempotencyUUID(storeID, scopedKey))

		w := &capturingWriter{ResponseWriter: c.Writer}
		c.Writer = w
		handler(c)
		status := w.Status()
		if status >= 500 {
			return // allow retry
		}
		utils.DB.Create(&models.APIIdempotencyRecord{
			StoreID:      storeID,
			Key:          scopedKey,
			RequestHash:  reqHash,
			StatusCode:   status,
			ResponseBody: w.buf.String(),
			CreatedAt:    time.Now(),
		})
	}
}

// idempotencyUUID derives a stable UUID from an idempotency key.
func idempotencyUUID(storeID uuid.UUID, key string) uuid.UUID {
	return uuid.NewSHA1(storeID, []byte(key))
}

type capturingWriter struct {
	gin.ResponseWriter
	buf bytes.Buffer
}

func (w *capturingWriter) Write(b []byte) (int, error) {
	w.buf.Write(b)
	return w.ResponseWriter.Write(b)
}

func (w *capturingWriter) WriteString(s string) (int, error) {
	w.buf.WriteString(s)
	return w.ResponseWriter.WriteString(s)
}

func readAndRestoreBody(c *gin.Context) ([]byte, error) {
	if c.Request.Body == nil {
		return nil, nil
	}
	var buf bytes.Buffer
	_, err := buf.ReadFrom(http.MaxBytesReader(c.Writer, c.Request.Body, 2<<20))
	b := buf.Bytes()
	c.Request.Body = io.NopCloser(bytes.NewReader(b))
	return b, err
}
