package utils

import (
	"fmt"
	"strings"
	"unicode"
)

// CompactSearchTerm lowercases s and strips all whitespace, so "Kit Kat" and
// "kitkat" compare equal. Pair it with SQLCompactColumn.
func CompactSearchTerm(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return unicode.ToLower(r)
	}, s)
}

// SQLCompactColumn returns a dialect-specific expression that lowercases a
// column and removes spaces and tabs, for whitespace-insensitive matching
// against a CompactSearchTerm value.
func SQLCompactColumn(column string) string {
	tab := "char(9)"
	if IsPostgres() {
		tab = "chr(9)"
	}
	return fmt.Sprintf("REPLACE(REPLACE(LOWER(%s), ' ', ''), %s, '')", column, tab)
}

// SQLCompactLike returns "<compact column> LIKE ?" for use with
// CompactLikePattern.
func SQLCompactLike(column string) string {
	return SQLCompactColumn(column) + " LIKE ?"
}

// CompactLikePattern builds the %term% pattern for SQLCompactLike. It returns
// "" when the search has no non-space characters.
func CompactLikePattern(search string) string {
	compact := CompactSearchTerm(search)
	if compact == "" {
		return ""
	}
	return "%" + compact + "%"
}
