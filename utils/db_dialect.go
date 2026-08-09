package utils

import (
	"fmt"
	"strings"

	"gorm.io/gorm"
)

type Dialect string

const (
	DialectSQLite   Dialect = "sqlite"
	DialectPostgres Dialect = "postgres"
)

var currentDialect Dialect

func SetDialect(d Dialect) {
	currentDialect = d
}

func CurrentDialect() Dialect {
	return currentDialect
}

func IsSQLite() bool {
	return currentDialect == DialectSQLite
}

func IsPostgres() bool {
	return currentDialect == DialectPostgres
}

// SQLDateExpr returns a dialect-specific expression that truncates a timestamp to a calendar date.
func SQLDateExpr(column string) string {
	if IsPostgres() {
		return fmt.Sprintf("(%s)::date", column)
	}
	return fmt.Sprintf("DATE(%s)", column)
}

// SQLDateEquals scopes rows to a single calendar day (parameter: YYYY-MM-DD).
func SQLDateEquals(column string) string {
	if IsPostgres() {
		return fmt.Sprintf("(%s)::date = ?::date", column)
	}
	return fmt.Sprintf("DATE(%s) = ?", column)
}

// SQLDateGTE / SQLDateLTE compare calendar dates (parameters: YYYY-MM-DD).
func SQLDateGTE(column string) string {
	if IsPostgres() {
		return fmt.Sprintf("(%s)::date >= ?::date", column)
	}
	return fmt.Sprintf("DATE(%s) >= ?", column)
}

func SQLDateLTE(column string) string {
	if IsPostgres() {
		return fmt.Sprintf("(%s)::date <= ?::date", column)
	}
	return fmt.Sprintf("DATE(%s) <= ?", column)
}

// SQLPeriodExpr returns a grouping expression for daily, weekly, monthly, or yearly buckets.
func SQLPeriodExpr(column, period string) string {
	switch period {
	case "daily":
		return SQLDateExpr(column)
	case "weekly":
		if IsPostgres() {
			return fmt.Sprintf("to_char(%s, 'IYYY-\"W\"IW')", column)
		}
		return fmt.Sprintf("strftime('%%Y-W%%W', %s)", column)
	case "yearly":
		if IsPostgres() {
			return fmt.Sprintf("to_char(%s, 'YYYY')", column)
		}
		return fmt.Sprintf("strftime('%%Y', %s)", column)
	default: // monthly
		if IsPostgres() {
			return fmt.Sprintf("to_char(%s, 'YYYY-MM')", column)
		}
		return fmt.Sprintf("strftime('%%Y-%%m', %s)", column)
	}
}

// SQLPeriodLimit returns a default row limit for report period granularity.
func SQLPeriodLimit(period string) string {
	switch period {
	case "daily":
		return "30"
	case "weekly":
		return "12"
	case "yearly":
		return "5"
	default:
		return "12"
	}
}

func tableColumnExists(db *gorm.DB, table, col string) bool {
	if IsPostgres() {
		var name string
		err := db.Raw(
			`SELECT column_name FROM information_schema.columns
			 WHERE table_schema = CURRENT_SCHEMA() AND table_name = ? AND column_name = ?`,
			strings.ToLower(table), strings.ToLower(col),
		).Scan(&name).Error
		return err == nil && name != ""
	}

	var name string
	if err := db.Raw(
		`SELECT name FROM pragma_table_info(?) WHERE name = ?`,
		table, col,
	).Scan(&name).Error; err != nil {
		return false
	}
	return name != ""
}
