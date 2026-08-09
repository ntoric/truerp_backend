package main

import (
	"database/sql"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

	"truerp/utils"
)

func main() {
	log.SetFlags(log.LstdFlags)

	var (
		sqlitePath  = flag.String("sqlite", "", "Path to SQLite database file (default: DATABASE_PATH or data/truerp.db)")
		postgresURL = flag.String("postgres", "", "PostgreSQL URL (default: DATABASE_URL env)")
		prepare     = flag.Bool("prepare-schema", false, "Create Postgres schema via GORM AutoMigrate")
		copyData    = flag.Bool("copy", false, "Copy all shared tables from SQLite to Postgres")
		validate    = flag.Bool("validate", false, "Compare row counts and financial totals")
		all         = flag.Bool("all", false, "Run prepare-schema, copy (with truncate), and validate")
		truncate    = flag.Bool("truncate", false, "Truncate Postgres tables before copy")
		dryRun      = flag.Bool("dry-run", false, "Show planned actions without writing")
		batchSize   = flag.Int("batch-size", 500, "Insert batch size")
	)
	flag.Parse()

	if *all {
		*prepare = true
		*copyData = true
		*validate = true
		*truncate = true
	}

	if !*prepare && !*copyData && !*validate {
		flag.Usage()
		os.Exit(2)
	}

	sqlite := resolveSQLitePath(*sqlitePath)
	pgURL := resolvePostgresURL(*postgresURL)
	if sqlite == "" {
		log.Fatal("SQLite path is required (-sqlite or DATABASE_PATH)")
	}
	if pgURL == "" {
		log.Fatal("PostgreSQL URL is required (-postgres or DATABASE_URL)")
	}

	log.Printf("Source SQLite: %s", sqlite)
	log.Printf("Target Postgres: %s", redactDSN(pgURL))

	if *prepare {
		if *dryRun {
			log.Println("[dry-run] would prepare postgres schema")
		} else {
			db, err := utils.OpenPostgresForMigration(pgURL)
			if err != nil {
				log.Fatalf("prepare schema: open postgres: %v", err)
			}
			if err := utils.PreparePostgresSchema(db); err != nil {
				log.Fatalf("prepare schema: %v", err)
			}
			sqlDB, _ := db.DB()
			sqlDB.Close()
			log.Println("Postgres schema ready")
		}
	}

	if *copyData {
		report, err := utils.CopySQLiteToPostgres(utils.SQLiteToPostgresOptions{
			SQLitePath:  sqlite,
			PostgresURL: pgURL,
			BatchSize:   *batchSize,
			Truncate:    *truncate,
			DryRun:      *dryRun,
		})
		if err != nil {
			log.Fatalf("copy failed: %v", err)
		}
		log.Printf("Copy complete: %d rows copied", report.RowsCopied)
		if len(report.Errors) > 0 {
			log.Println("Copy errors:")
			for _, e := range report.Errors {
				log.Printf("  - %s", e)
			}
			os.Exit(1)
		}
	}

	if *validate {
		counts, totals, mismatches, err := utils.ValidateSQLitePostgres(sqlite, pgURL)
		if err != nil {
			log.Fatalf("validate failed: %v", err)
		}

		fmt.Println("\n=== Row counts ===")
		for _, c := range counts {
			status := "OK"
			if c.SQLiteRows != c.PostgresRows {
				status = "MISMATCH"
			}
			fmt.Printf("%-40s sqlite=%6d postgres=%6d  %s\n", c.Table, c.SQLiteRows, c.PostgresRows, status)
		}

		fmt.Println("\n=== Financial totals (SQLite) ===")
		printNullFloat("Invoices total", totals.InvoiceTotal)
		printNullFloat("Payments total", totals.PaymentTotal)
		printNullFloat("Expenses total", totals.ExpenseTotal)
		printNullInt("Parties", totals.PartyCount)
		printNullInt("Users", totals.UserCount)

		if len(mismatches) > 0 {
			fmt.Println("\n=== Validation failures ===")
			for _, m := range mismatches {
				fmt.Printf("  - %s\n", m)
			}
			os.Exit(1)
		}
		fmt.Println("\nValidation passed")
	}
}

func resolveSQLitePath(flagPath string) string {
	if p := strings.TrimSpace(flagPath); p != "" {
		return p
	}
	if p := strings.TrimSpace(os.Getenv("DATABASE_PATH")); p != "" {
		return p
	}
	if _, err := os.Stat("data/truerp.db"); err == nil {
		return "data/truerp.db"
	}
	if _, err := os.Stat("data/billbook.db"); err == nil {
		return "data/billbook.db"
	}
	return "data/truerp.db"
}

func resolvePostgresURL(flagURL string) string {
	if u := strings.TrimSpace(flagURL); u != "" {
		return u
	}
	return strings.TrimSpace(os.Getenv("DATABASE_URL"))
}

func redactDSN(dsn string) string {
	if strings.Contains(dsn, "@") {
		parts := strings.SplitN(dsn, "@", 2)
		if len(parts) == 2 {
			scheme := parts[0]
			if idx := strings.Index(scheme, "://"); idx >= 0 {
				return scheme[:idx+3] + "***@" + parts[1]
			}
		}
	}
	return dsn
}

func printNullFloat(label string, v sql.NullFloat64) {
	if v.Valid {
		fmt.Printf("  %s: %.2f\n", label, v.Float64)
	}
}

func printNullInt(label string, v sql.NullInt64) {
	if v.Valid {
		fmt.Printf("  %s: %d\n", label, v.Int64)
	}
}
