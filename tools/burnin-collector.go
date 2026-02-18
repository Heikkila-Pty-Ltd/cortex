package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/antigravity-dev/cortex/internal/config"
	"github.com/antigravity-dev/cortex/internal/monitoring"
)

const dateLayout = "2006-01-02"

func main() {
	var (
		configPath = flag.String("config", "cortex.toml", "path to cortex config for default DB lookup")
		dbPath     = flag.String("db", "", "path to sqlite state db (overrides --config)")
		startDate  = flag.String("start-date", "", "window start date in YYYY-MM-DD (inclusive)")
		endDate    = flag.String("end-date", "", "window end date in YYYY-MM-DD (exclusive)")
		project    = flag.String("project", "", "optional project filter")
		outPath    = flag.String("out", "-", "output path for JSON ('-' for stdout)")
	)
	flag.Parse()

	start, end, err := resolveWindow(*startDate, *endDate)
	if err != nil {
		die("resolve date window: %v", err)
	}

	resolvedDB, err := resolveDBPath(*dbPath, *configPath)
	if err != nil {
		die("resolve db path: %v", err)
	}

	db, err := sql.Open("sqlite", resolvedDB)
	if err != nil {
		die("open db: %v", err)
	}
	defer db.Close()

	if err := db.Ping(); err != nil {
		die("ping db %q: %v", resolvedDB, err)
	}

	metrics, err := monitoring.CollectBurninRawMetrics(context.Background(), db, start, end, *project)
	if err != nil {
		die("collect burn-in metrics: %v", err)
	}

	payload, err := json.MarshalIndent(metrics, "", "  ")
	if err != nil {
		die("encode json: %v", err)
	}

	if *outPath == "-" {
		fmt.Printf("%s\n", payload)
		return
	}

	if err := os.MkdirAll(filepath.Dir(*outPath), 0o755); err != nil {
		die("create output directory: %v", err)
	}
	if err := os.WriteFile(*outPath, payload, 0o644); err != nil {
		die("write output: %v", err)
	}
	fmt.Fprintf(os.Stderr, "Burn-in metrics written to %s\n", *outPath)
}

func resolveWindow(startDate, endDate string) (time.Time, time.Time, error) {
	startDate = stringsTrim(startDate)
	endDate = stringsTrim(endDate)

	if (startDate == "") != (endDate == "") {
		return time.Time{}, time.Time{}, fmt.Errorf("--start-date and --end-date must be provided together")
	}
	if startDate != "" && endDate != "" {
		start, err := parseUTCDate(startDate)
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("invalid --start-date: %w", err)
		}
		end, err := parseUTCDate(endDate)
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("invalid --end-date: %w", err)
		}
		if !end.After(start) {
			return time.Time{}, time.Time{}, fmt.Errorf("--end-date must be after --start-date")
		}
		return start, end, nil
	}

	// Default: most recent completed 7-day window in UTC.
	todayUTC := time.Now().UTC().Truncate(24 * time.Hour)
	return todayUTC.AddDate(0, 0, -7), todayUTC, nil
}

func resolveDBPath(dbPath, configPath string) (string, error) {
	if stringsTrim(dbPath) != "" {
		return config.ExpandHome(stringsTrim(dbPath)), nil
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		return "", fmt.Errorf("load config %q: %w (or set --db)", configPath, err)
	}

	stateDB := stringsTrim(cfg.General.StateDB)
	if stateDB == "" {
		home, homeErr := os.UserHomeDir()
		if homeErr != nil {
			return "", fmt.Errorf("state_db empty and cannot resolve home directory: %w", homeErr)
		}
		stateDB = filepath.Join(home, ".cortex", "cortex.db")
	}
	return config.ExpandHome(stateDB), nil
}

func parseUTCDate(raw string) (time.Time, error) {
	ts, err := time.Parse(dateLayout, raw)
	if err != nil {
		return time.Time{}, err
	}
	return time.Date(ts.Year(), ts.Month(), ts.Day(), 0, 0, 0, 0, time.UTC), nil
}

func stringsTrim(value string) string {
	return strings.TrimSpace(value)
}

func die(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
