package beads

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildDepGraph(t *testing.T) {
	beads := []Bead{
		{ID: "a", Title: "Task A", Status: "open", DependsOn: nil},
		{ID: "b", Title: "Task B", Status: "open", DependsOn: []string{"a"}},
		{ID: "c", Title: "Task C", Status: "open", DependsOn: []string{"a", "b"}},
	}

	g := BuildDepGraph(beads)

	if len(g.Nodes()) != 3 {
		t.Errorf("expected 3 nodes, got %d", len(g.Nodes()))
	}

	// b depends on a
	deps := g.DependsOnIDs("b")
	if len(deps) != 1 || deps[0] != "a" {
		t.Errorf("b depends on %v, want [a]", deps)
	}

	// a blocks b and c
	blocks := g.BlocksIDs("a")
	if len(blocks) != 2 {
		t.Errorf("a blocks %v, want 2 items", blocks)
	}

	// c depends on a and b
	deps = g.DependsOnIDs("c")
	if len(deps) != 2 {
		t.Errorf("c depends on %v, want 2 items", deps)
	}
}

func TestFilterUnblockedOpen_AllDepsClosed(t *testing.T) {
	beads := []Bead{
		{ID: "a", Title: "Task A", Status: "closed"},
		{ID: "b", Title: "Task B", Status: "open", DependsOn: []string{"a"}, Priority: 1, EstimateMinutes: 30},
	}

	g := BuildDepGraph(beads)
	result := FilterUnblockedOpen(beads, g)

	if len(result) != 1 {
		t.Fatalf("expected 1 unblocked bead, got %d", len(result))
	}
	if result[0].ID != "b" {
		t.Errorf("expected bead b, got %s", result[0].ID)
	}
}

func TestFilterUnblockedOpen_SomeDepsOpen(t *testing.T) {
	beads := []Bead{
		{ID: "a", Title: "Task A", Status: "open"},
		{ID: "b", Title: "Task B", Status: "open", DependsOn: []string{"a"}},
	}

	g := BuildDepGraph(beads)
	result := FilterUnblockedOpen(beads, g)

	// Only a should be unblocked (no deps); b is blocked by a
	if len(result) != 1 {
		t.Fatalf("expected 1 unblocked bead, got %d", len(result))
	}
	if result[0].ID != "a" {
		t.Errorf("expected bead a, got %s", result[0].ID)
	}
}

func TestFilterUnblockedOpen_ExcludesEpics(t *testing.T) {
	beads := []Bead{
		{ID: "e1", Title: "Epic", Status: "open", Type: "epic"},
		{ID: "t1", Title: "Task", Status: "open", Type: "task"},
	}

	g := BuildDepGraph(beads)
	result := FilterUnblockedOpen(beads, g)

	if len(result) != 1 {
		t.Fatalf("expected 1 non-epic bead, got %d", len(result))
	}
	if result[0].ID != "t1" {
		t.Errorf("expected t1, got %s", result[0].ID)
	}
}

func TestFilterUnblockedOpen_PrioritySorting(t *testing.T) {
	beads := []Bead{
		{ID: "low", Title: "Low", Status: "open", Priority: 3, EstimateMinutes: 10},
		{ID: "high", Title: "High", Status: "open", Priority: 0, EstimateMinutes: 60},
		{ID: "med", Title: "Med", Status: "open", Priority: 1, EstimateMinutes: 30},
		{ID: "med2", Title: "Med2", Status: "open", Priority: 1, EstimateMinutes: 15},
	}

	g := BuildDepGraph(beads)
	result := FilterUnblockedOpen(beads, g)

	if len(result) != 4 {
		t.Fatalf("expected 4 beads, got %d", len(result))
	}

	expected := []string{"high", "med2", "med", "low"}
	for i, id := range expected {
		if result[i].ID != id {
			t.Errorf("position %d: expected %s, got %s", i, id, result[i].ID)
		}
	}
}

func TestFilterUnblockedOpen_EmptyList(t *testing.T) {
	g := BuildDepGraph(nil)
	result := FilterUnblockedOpen(nil, g)
	if len(result) != 0 {
		t.Errorf("expected empty result, got %d", len(result))
	}
}

func TestFilterUnblockedOpen_NoDeps(t *testing.T) {
	beads := []Bead{
		{ID: "a", Title: "A", Status: "open", Priority: 2},
		{ID: "b", Title: "B", Status: "open", Priority: 1},
	}

	g := BuildDepGraph(beads)
	result := FilterUnblockedOpen(beads, g)

	if len(result) != 2 {
		t.Fatalf("expected 2 beads, got %d", len(result))
	}
	if result[0].ID != "b" {
		t.Errorf("expected b first (priority 1), got %s", result[0].ID)
	}
}

func TestClaimBeadOwnershipCtx(t *testing.T) {
	projectDir := t.TempDir()
	beadsDir := filepath.Join(projectDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir beads dir: %v", err)
	}
	logPath := filepath.Join(projectDir, "args.log")

	fakeBin := t.TempDir()
	bdPath := filepath.Join(fakeBin, "bd")
	script := "#!/bin/sh\n" +
		"echo \"$@\" >> \"$BD_ARGS_LOG\"\n" +
		"echo \"ok\"\n"
	if err := os.WriteFile(bdPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake bd: %v", err)
	}

	t.Setenv("BD_ARGS_LOG", logPath)
	t.Setenv("PATH", fakeBin+":"+os.Getenv("PATH"))

	if err := ClaimBeadOwnershipCtx(context.Background(), beadsDir, "cortex-123"); err != nil {
		t.Fatalf("ClaimBeadOwnershipCtx failed: %v", err)
	}

	args, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read args log: %v", err)
	}
	got := string(args)
	if !strings.Contains(got, "update cortex-123 --claim --status open") {
		t.Fatalf("unexpected bd args: %q", got)
	}
}

func TestClaimBeadOwnershipCtxAlreadyClaimed(t *testing.T) {
	projectDir := t.TempDir()
	beadsDir := filepath.Join(projectDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir beads dir: %v", err)
	}

	fakeBin := t.TempDir()
	bdPath := filepath.Join(fakeBin, "bd")
	script := "#!/bin/sh\n" +
		"echo \"Error claiming $2: already claimed by Someone\"\n" +
		"exit 0\n"
	if err := os.WriteFile(bdPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake bd: %v", err)
	}

	t.Setenv("PATH", fakeBin+":"+os.Getenv("PATH"))

	err := ClaimBeadOwnershipCtx(context.Background(), beadsDir, "cortex-123")
	if err == nil {
		t.Fatal("expected conflict error, got nil")
	}
	if !IsAlreadyClaimed(err) {
		t.Fatalf("expected ErrBeadAlreadyClaimed, got: %v", err)
	}
}

func TestReleaseBeadOwnershipCtx(t *testing.T) {
	projectDir := t.TempDir()
	beadsDir := filepath.Join(projectDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir beads dir: %v", err)
	}
	logPath := filepath.Join(projectDir, "args.log")

	fakeBin := t.TempDir()
	bdPath := filepath.Join(fakeBin, "bd")
	script := "#!/bin/sh\n" +
		"echo \"$@\" >> \"$BD_ARGS_LOG\"\n" +
		"echo \"ok\"\n"
	if err := os.WriteFile(bdPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake bd: %v", err)
	}

	t.Setenv("BD_ARGS_LOG", logPath)
	t.Setenv("PATH", fakeBin+":"+os.Getenv("PATH"))

	if err := ReleaseBeadOwnershipCtx(context.Background(), beadsDir, "cortex-456"); err != nil {
		t.Fatalf("ReleaseBeadOwnershipCtx failed: %v", err)
	}

	args, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read args log: %v", err)
	}
	got := string(args)
	if !strings.Contains(got, "update cortex-456 --assignee=") {
		t.Fatalf("unexpected bd args: %q", got)
	}
}

func TestListBeadsCtxUsesAllFlag(t *testing.T) {
	projectDir := t.TempDir()
	beadsDir := filepath.Join(projectDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir beads dir: %v", err)
	}
	logPath := filepath.Join(projectDir, "args.log")

	fakeBin := t.TempDir()
	bdPath := filepath.Join(fakeBin, "bd")
	script := "#!/bin/sh\n" +
		"echo \"$@\" >> \"$BD_ARGS_LOG\"\n" +
		"echo '[{\"id\":\"cortex-closed\",\"title\":\"Closed bug\",\"status\":\"closed\",\"priority\":1,\"issue_type\":\"bug\"}]'\n"
	if err := os.WriteFile(bdPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake bd: %v", err)
	}

	t.Setenv("BD_ARGS_LOG", logPath)
	t.Setenv("PATH", fakeBin+":"+os.Getenv("PATH"))

	beadList, err := ListBeadsCtx(context.Background(), beadsDir)
	if err != nil {
		t.Fatalf("ListBeadsCtx failed: %v", err)
	}
	if len(beadList) != 1 {
		t.Fatalf("expected 1 bead, got %d", len(beadList))
	}
	if beadList[0].Status != "closed" {
		t.Fatalf("expected closed status, got %q", beadList[0].Status)
	}

	args, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read args log: %v", err)
	}
	got := string(args)
	if !strings.Contains(got, "list --all --limit 0 --json --quiet") {
		t.Fatalf("expected bd list to include --all --limit 0, got %q", got)
	}
}

func TestListBeadsCtxFallsBackWhenAllUnsupported(t *testing.T) {
	projectDir := t.TempDir()
	beadsDir := filepath.Join(projectDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir beads dir: %v", err)
	}
	logPath := filepath.Join(projectDir, "args.log")

	fakeBin := t.TempDir()
	bdPath := filepath.Join(fakeBin, "bd")
	script := "#!/bin/sh\n" +
		"echo \"$@\" >> \"$BD_ARGS_LOG\"\n" +
		"case \"$*\" in\n" +
		"  *\"--all\"*)\n" +
		"    echo 'unknown flag: --all' >&2\n" +
		"    exit 1\n" +
		"    ;;\n" +
		"  *\"--json --quiet\"*)\n" +
		"    echo '[{\"id\":\"cortex-open\",\"title\":\"Open task\",\"status\":\"open\",\"priority\":2,\"issue_type\":\"task\"}]'\n" +
		"    ;;\n" +
		"  *)\n" +
		"    echo '[]'\n" +
		"    ;;\n" +
		"esac\n"
	if err := os.WriteFile(bdPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake bd: %v", err)
	}

	t.Setenv("BD_ARGS_LOG", logPath)
	t.Setenv("PATH", fakeBin+":"+os.Getenv("PATH"))

	beadList, err := ListBeadsCtx(context.Background(), beadsDir)
	if err != nil {
		t.Fatalf("ListBeadsCtx failed: %v", err)
	}
	if len(beadList) != 1 {
		t.Fatalf("expected fallback to return 1 bead, got %d", len(beadList))
	}
	if beadList[0].ID != "cortex-open" {
		t.Fatalf("expected fallback bead cortex-open, got %q", beadList[0].ID)
	}

	args, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read args log: %v", err)
	}
	got := string(args)
	if !strings.Contains(got, "list --all --limit 0 --json --quiet") {
		t.Fatalf("expected initial --all call, got %q", got)
	}
	if !strings.Contains(got, "list --limit 0 --json --quiet") {
		t.Fatalf("expected fallback without --all to include --limit 0, got %q", got)
	}
	if strings.Contains(got, "--format=json") {
		t.Fatalf("expected no --format=json fallback usage, got %q", got)
	}
}

func TestListBeadsCtxRecoversFromOutOfSyncDatabase(t *testing.T) {
	projectDir := t.TempDir()
	beadsDir := filepath.Join(projectDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir beads dir: %v", err)
	}
	logPath := filepath.Join(projectDir, "args.log")
	attemptPath := filepath.Join(projectDir, "list-attempts")

	fakeBin := t.TempDir()
	bdPath := filepath.Join(fakeBin, "bd")
	script := "#!/bin/sh\n" +
		"echo \"$@\" >> \"$BD_ARGS_LOG\"\n" +
		"case \"$*\" in\n" +
		"  *\"sync --import-only\"*)\n" +
		"    echo 'ok'\n" +
		"    exit 0\n" +
		"    ;;\n" +
		"  *\"list\"*)\n" +
		"    n=0\n" +
		"    if [ -f \"$BD_LIST_ATTEMPTS\" ]; then n=$(cat \"$BD_LIST_ATTEMPTS\"); fi\n" +
		"    n=$((n+1))\n" +
		"    echo \"$n\" > \"$BD_LIST_ATTEMPTS\"\n" +
		"    if [ \"$n\" -eq 1 ]; then\n" +
		"      echo 'Database out of sync with JSONL. run \"bd sync --import-only\"' >&2\n" +
		"      exit 1\n" +
		"    fi\n" +
		"    echo '[{\"id\":\"cortex-sync\",\"title\":\"Recovered task\",\"status\":\"open\",\"priority\":2,\"issue_type\":\"task\"}]'\n" +
		"    ;;\n" +
		"  *)\n" +
		"    echo '[]'\n" +
		"    ;;\n" +
		"esac\n"
	if err := os.WriteFile(bdPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake bd: %v", err)
	}

	t.Setenv("BD_ARGS_LOG", logPath)
	t.Setenv("BD_LIST_ATTEMPTS", attemptPath)
	t.Setenv("PATH", fakeBin+":"+os.Getenv("PATH"))

	beadList, err := ListBeadsCtx(context.Background(), beadsDir)
	if err != nil {
		t.Fatalf("ListBeadsCtx recovery failed: %v", err)
	}
	if len(beadList) != 1 || beadList[0].ID != "cortex-sync" {
		t.Fatalf("expected recovered bead cortex-sync, got %+v", beadList)
	}

	args, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read args log: %v", err)
	}
	got := string(args)
	if strings.Count(got, "list ") < 2 {
		t.Fatalf("expected list to be retried after recovery sync, got %q", got)
	}
	if !strings.Contains(got, "sync --import-only") {
		t.Fatalf("expected recovery sync call, got %q", got)
	}
}

func TestSyncImportCtxUsesImportOnly(t *testing.T) {
	projectDir := t.TempDir()
	beadsDir := filepath.Join(projectDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir beads dir: %v", err)
	}
	logPath := filepath.Join(projectDir, "args.log")

	fakeBin := t.TempDir()
	bdPath := filepath.Join(fakeBin, "bd")
	script := "#!/bin/sh\n" +
		"echo \"$@\" >> \"$BD_ARGS_LOG\"\n" +
		"echo 'ok'\n"
	if err := os.WriteFile(bdPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake bd: %v", err)
	}

	t.Setenv("BD_ARGS_LOG", logPath)
	t.Setenv("PATH", fakeBin+":"+os.Getenv("PATH"))

	if err := SyncImportCtx(context.Background(), beadsDir); err != nil {
		t.Fatalf("SyncImportCtx failed: %v", err)
	}

	args, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read args log: %v", err)
	}
	got := string(args)
	if !strings.Contains(got, "sync --import-only") {
		t.Fatalf("expected bd sync to include --import-only, got %q", got)
	}
}

func TestSyncImportCtxFallsBackWithoutImportOnly(t *testing.T) {
	projectDir := t.TempDir()
	beadsDir := filepath.Join(projectDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("mkdir beads dir: %v", err)
	}
	logPath := filepath.Join(projectDir, "args.log")

	fakeBin := t.TempDir()
	bdPath := filepath.Join(fakeBin, "bd")
	script := "#!/bin/sh\n" +
		"echo \"$@\" >> \"$BD_ARGS_LOG\"\n" +
		"case \"$*\" in\n" +
		"  *\"sync --import-only\"*)\n" +
		"    echo 'unknown flag: --import-only' >&2\n" +
		"    exit 1\n" +
		"    ;;\n" +
		"  *\"sync\"*)\n" +
		"    echo 'ok'\n" +
		"    ;;\n" +
		"  *)\n" +
		"    echo 'ok'\n" +
		"    ;;\n" +
		"esac\n"
	if err := os.WriteFile(bdPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake bd: %v", err)
	}

	t.Setenv("BD_ARGS_LOG", logPath)
	t.Setenv("PATH", fakeBin+":"+os.Getenv("PATH"))

	if err := SyncImportCtx(context.Background(), beadsDir); err != nil {
		t.Fatalf("SyncImportCtx fallback failed: %v", err)
	}

	args, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read args log: %v", err)
	}
	got := string(args)
	if !strings.Contains(got, "sync --import-only") {
		t.Fatalf("expected initial sync with --import-only, got %q", got)
	}
	if !strings.Contains(got, "sync\n") && !strings.Contains(got, "sync\r\n") {
		t.Fatalf("expected fallback sync call, got %q", got)
	}
}
