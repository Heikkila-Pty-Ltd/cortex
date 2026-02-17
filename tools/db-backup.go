package main

import (
	"database/sql"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

func main() {
	var (
		dbPath     = flag.String("db", "", "source database path (required)")
		backupPath = flag.String("backup", "", "backup destination path (optional, auto-generated if not provided)")
		verify     = flag.Bool("verify", true, "run integrity check on backup")
		compress   = flag.Bool("compress", false, "compress backup with gzip")
		checkpoint = flag.Bool("checkpoint", true, "run checkpoint before backup to merge WAL")
	)
	flag.Parse()

	if *dbPath == "" {
		die("--db path is required")
	}

	// Expand tilde in paths
	*dbPath = expandPath(*dbPath)
	
	// Auto-generate backup path if not provided
	if *backupPath == "" {
		timestamp := time.Now().Format("20060102-150405")
		base := strings.TrimSuffix(filepath.Base(*dbPath), filepath.Ext(*dbPath))
		ext := ".db"
		if *compress {
			ext = ".db.gz"
		}
		*backupPath = fmt.Sprintf("%s-backup-%s%s", base, timestamp, ext)
	}
	*backupPath = expandPath(*backupPath)

	fmt.Printf("SQLite Backup Tool\n")
	fmt.Printf("Source: %s\n", *dbPath)
	fmt.Printf("Destination: %s\n", *backupPath)

	// Ensure backup directory exists
	if err := os.MkdirAll(filepath.Dir(*backupPath), 0o755); err != nil {
		die("create backup directory: %v", err)
	}

	// Open source database
	db, err := sql.Open("sqlite", *dbPath+"?mode=ro")
	if err != nil {
		die("open source database: %v", err)
	}
	defer db.Close()

	// Run checkpoint if requested (flushes WAL to main DB)
	if *checkpoint {
		fmt.Printf("Running WAL checkpoint...\n")
		if _, err := db.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
			fmt.Printf("Warning: checkpoint failed: %v\n", err)
		}
	}

	// Perform backup using SQLite's backup API
	fmt.Printf("Creating backup...\n")
	start := time.Now()
	
	if err := performBackup(*dbPath, *backupPath, *compress); err != nil {
		die("backup failed: %v", err)
	}
	
	duration := time.Since(start)
	fmt.Printf("Backup completed in %v\n", duration)

	// Verify backup integrity
	if *verify {
		fmt.Printf("Verifying backup integrity...\n")
		if err := verifyBackup(*backupPath, *compress); err != nil {
			die("backup verification failed: %v", err)
		}
		fmt.Printf("Backup verification successful\n")
	}

	// Show backup info
	info, err := os.Stat(*backupPath)
	if err == nil {
		fmt.Printf("Backup size: %d bytes (%.2f MB)\n", info.Size(), float64(info.Size())/1024/1024)
	}

	fmt.Printf("✅ Backup completed successfully\n")
}

func performBackup(srcPath, dstPath string, compress bool) error {
	// For SQLite, the most reliable backup is using the .backup command via sqlite3
	// or copying the file after checkpoint. We'll use file copy for simplicity.
	
	src, err := os.Open(srcPath)
	if err != nil {
		return fmt.Errorf("open source: %v", err)
	}
	defer src.Close()

	dst, err := os.Create(dstPath)
	if err != nil {
		return fmt.Errorf("create destination: %v", err)
	}
	defer dst.Close()

	if compress {
		// TODO: Add gzip compression if needed
		return fmt.Errorf("compression not implemented yet")
	}

	// Simple file copy for now
	buf := make([]byte, 1024*1024) // 1MB buffer
	for {
		n, err := src.Read(buf)
		if n > 0 {
			if _, err := dst.Write(buf[:n]); err != nil {
				return fmt.Errorf("write: %v", err)
			}
		}
		if err != nil {
			if err.Error() == "EOF" {
				break
			}
			return fmt.Errorf("read: %v", err)
		}
	}

	return dst.Sync()
}

func verifyBackup(backupPath string, compress bool) error {
	if compress {
		return fmt.Errorf("compressed backup verification not implemented")
	}

	// Open backup and run integrity check
	db, err := sql.Open("sqlite", backupPath+"?mode=ro")
	if err != nil {
		return fmt.Errorf("open backup: %v", err)
	}
	defer db.Close()

	// Run PRAGMA integrity_check
	var result string
	if err := db.QueryRow("PRAGMA integrity_check").Scan(&result); err != nil {
		return fmt.Errorf("integrity check query: %v", err)
	}

	if result != "ok" {
		return fmt.Errorf("integrity check failed: %s", result)
	}

	// Quick sanity check - verify we can read some basic tables
	tables := []string{"dispatches", "health_events"}
	for _, table := range tables {
		var count int
		query := fmt.Sprintf("SELECT COUNT(*) FROM %s", table)
		if err := db.QueryRow(query).Scan(&count); err != nil {
			fmt.Printf("Warning: could not count rows in %s: %v\n", table, err)
		} else {
			fmt.Printf("Verified table %s: %d rows\n", table, count)
		}
	}

	return nil
}

func expandPath(path string) string {
	if strings.HasPrefix(path, "~/") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, path[2:])
	}
	return path
}

func die(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}