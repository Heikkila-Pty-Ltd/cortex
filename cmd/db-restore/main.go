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
		backupPath = flag.String("backup", "", "backup file path (required)")
		dbPath     = flag.String("db", "", "target database path (required)")
		verify     = flag.Bool("verify", true, "verify restore integrity")
		dryRun     = flag.Bool("dry-run", false, "validate backup without actually restoring")
		force      = flag.Bool("force", false, "overwrite existing database")
	)
	flag.Parse()

	if *backupPath == "" {
		die("--backup path is required")
	}
	if *dbPath == "" {
		die("--db path is required")
	}

	*backupPath = expandPath(*backupPath)
	*dbPath = expandPath(*dbPath)

	fmt.Printf("SQLite Restore Tool\n")
	fmt.Printf("Backup: %s\n", *backupPath)
	fmt.Printf("Target: %s\n", *dbPath)

	// Verify backup exists and is readable
	if _, err := os.Stat(*backupPath); os.IsNotExist(err) {
		die("backup file does not exist: %s", *backupPath)
	}

	// Verify backup integrity first
	fmt.Printf("Verifying backup integrity...\n")
	backupInfo, err := verifyBackupIntegrity(*backupPath)
	if err != nil {
		die("backup verification failed: %v", err)
	}
	fmt.Printf("Backup verification passed: %v\n", backupInfo)

	if *dryRun {
		fmt.Printf("✅ Dry run completed - backup is valid\n")
		return
	}

	// Check if target exists
	if _, err := os.Stat(*dbPath); err == nil && !*force {
		die("target database exists (use --force to overwrite): %s", *dbPath)
	}

	// Create target directory
	if err := os.MkdirAll(filepath.Dir(*dbPath), 0o755); err != nil {
		die("create target directory: %v", err)
	}

	// Create safety backup of existing DB if it exists
	var safetyBackup string
	if _, err := os.Stat(*dbPath); err == nil {
		safetyBackup = *dbPath + ".pre-restore-" + time.Now().Format("20060102-150405")
		fmt.Printf("Creating safety backup: %s\n", safetyBackup)
		if err := copyFile(*dbPath, safetyBackup); err != nil {
			die("create safety backup: %v", err)
		}
	}

	// Perform restore
	fmt.Printf("Restoring database...\n")
	start := time.Now()
	
	if err := performRestore(*backupPath, *dbPath); err != nil {
		// Attempt rollback if we have safety backup
		if safetyBackup != "" {
			fmt.Printf("Restore failed, attempting rollback...\n")
			if rollbackErr := copyFile(safetyBackup, *dbPath); rollbackErr != nil {
				die("restore failed AND rollback failed: %v (original error: %v)", rollbackErr, err)
			}
			fmt.Printf("Rollback completed\n")
		}
		die("restore failed: %v", err)
	}
	
	duration := time.Since(start)
	fmt.Printf("Restore completed in %v\n", duration)

	// Verify restored database
	if *verify {
		fmt.Printf("Verifying restored database...\n")
		if err := verifyRestoredDatabase(*dbPath); err != nil {
			die("restored database verification failed: %v", err)
		}
		fmt.Printf("Restored database verification successful\n")
	}

	// Clean up safety backup on success
	if safetyBackup != "" {
		if err := os.Remove(safetyBackup); err != nil {
			fmt.Printf("Warning: could not clean up safety backup %s: %v\n", safetyBackup, err)
		} else {
			fmt.Printf("Safety backup cleaned up\n")
		}
	}

	fmt.Printf("✅ Restore completed successfully\n")
}

func verifyBackupIntegrity(backupPath string) (map[string]interface{}, error) {
	db, err := sql.Open("sqlite", backupPath+"?mode=ro")
	if err != nil {
		return nil, fmt.Errorf("open backup: %v", err)
	}
	defer db.Close()

	info := make(map[string]interface{})

	// Integrity check
	var integrityResult string
	if err := db.QueryRow("PRAGMA integrity_check").Scan(&integrityResult); err != nil {
		return nil, fmt.Errorf("integrity check: %v", err)
	}
	if integrityResult != "ok" {
		return nil, fmt.Errorf("integrity check failed: %s", integrityResult)
	}
	info["integrity"] = "ok"

	// Get table counts
	tables := []string{"dispatches", "health_events"}
	counts := make(map[string]int)
	for _, table := range tables {
		var count int
		query := fmt.Sprintf("SELECT COUNT(*) FROM %s", table)
		if err := db.QueryRow(query).Scan(&count); err != nil {
			// Don't fail if table doesn't exist, just note it
			counts[table] = -1
		} else {
			counts[table] = count
		}
	}
	info["table_counts"] = counts

	// Get schema version if available
	var schemaVersion int
	if err := db.QueryRow("PRAGMA schema_version").Scan(&schemaVersion); err == nil {
		info["schema_version"] = schemaVersion
	}

	return info, nil
}

func performRestore(backupPath, dbPath string) error {
	// Simple file copy for SQLite restore
	return copyFile(backupPath, dbPath)
}

func verifyRestoredDatabase(dbPath string) error {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return fmt.Errorf("open restored db: %v", err)
	}
	defer db.Close()

	// Test basic connectivity
	if err := db.Ping(); err != nil {
		return fmt.Errorf("ping restored db: %v", err)
	}

	// Run integrity check
	var result string
	if err := db.QueryRow("PRAGMA integrity_check").Scan(&result); err != nil {
		return fmt.Errorf("integrity check: %v", err)
	}
	if result != "ok" {
		return fmt.Errorf("integrity check failed: %s", result)
	}

	// Verify basic table structure
	tables := []string{"dispatches", "health_events"}
	for _, table := range tables {
		var count int
		query := fmt.Sprintf("SELECT COUNT(*) FROM %s", table)
		if err := db.QueryRow(query).Scan(&count); err != nil {
			fmt.Printf("Warning: could not query %s: %v\n", table, err)
		} else {
			fmt.Printf("Restored table %s: %d rows\n", table, count)
		}
	}

	return nil
}

func copyFile(src, dst string) error {
	srcFile, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open source: %v", err)
	}
	defer srcFile.Close()

	dstFile, err := os.Create(dst)
	if err != nil {
		return fmt.Errorf("create destination: %v", err)
	}
	defer dstFile.Close()

	buf := make([]byte, 1024*1024) // 1MB buffer
	for {
		n, err := srcFile.Read(buf)
		if n > 0 {
			if _, err := dstFile.Write(buf[:n]); err != nil {
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

	return dstFile.Sync()
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