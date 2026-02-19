package health

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestSetOrReplaceEnvVar_ReplacesAndDeduplicates(t *testing.T) {
	env := []string{
		"PATH=/usr/bin",
		"XDG_RUNTIME_DIR=",
		"OTHER=value",
		"XDG_RUNTIME_DIR=/tmp/old",
	}

	got := setOrReplaceEnvVar(env, "XDG_RUNTIME_DIR", "/run/user/1000")
	want := []string{
		"PATH=/usr/bin",
		"XDG_RUNTIME_DIR=/run/user/1000",
		"OTHER=value",
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected env slice:\n got: %#v\nwant: %#v", got, want)
	}
}

func TestSetOrReplaceEnvVar_AppendsWhenMissing(t *testing.T) {
	env := []string{"PATH=/usr/bin"}
	got := setOrReplaceEnvVar(env, "DBUS_SESSION_BUS_ADDRESS", "unix:path=/run/user/1000/bus")
	if len(got) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(got))
	}
	if got[1] != "DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1000/bus" {
		t.Fatalf("unexpected appended entry: %q", got[1])
	}
}

func TestIsUserBusUnavailableError(t *testing.T) {
	cases := []struct {
		name   string
		output string
		want   bool
	}{
		{
			name:   "user scope bus undefined",
			output: "Failed to connect to user scope bus via local transport: $DBUS_SESSION_BUS_ADDRESS and $XDG_RUNTIME_DIR not defined",
			want:   true,
		},
		{
			name:   "no medium found",
			output: "Failed to connect to bus: No medium found",
			want:   true,
		},
		{
			name:   "different error",
			output: "Unit openclaw-gateway.service could not be found.",
			want:   false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isUserBusUnavailableError(tc.output); got != tc.want {
				t.Fatalf("isUserBusUnavailableError(%q) = %v, want %v", tc.output, got, tc.want)
			}
		})
	}
}

func TestSystemctlMachineUser_PrecedenceAndFallback(t *testing.T) {
	t.Setenv("CORTEX_SYSTEMCTL_USER", "opsbot")
	t.Setenv("SUDO_USER", "ubuntu")
	t.Setenv("LOGNAME", "ubuntu")
	t.Setenv("USER", "ubuntu")
	t.Setenv("HOME", "/home/ubuntu")

	if got := systemctlMachineUser(); got != "opsbot" {
		t.Fatalf("expected CORTEX_SYSTEMCTL_USER to win, got %q", got)
	}
}

func TestSystemctlMachineUser_UsesHomeWhenUserIsRoot(t *testing.T) {
	t.Setenv("CORTEX_SYSTEMCTL_USER", "")
	t.Setenv("SUDO_USER", "")
	t.Setenv("LOGNAME", "root")
	t.Setenv("USER", "root")
	t.Setenv("HOME", "/home/ubuntu")

	if got := systemctlMachineUser(); got != "ubuntu" {
		t.Fatalf("expected HOME-derived username ubuntu, got %q", got)
	}
}

func TestSystemctlMachineUser_EmptyWhenNoCandidate(t *testing.T) {
	t.Setenv("CORTEX_SYSTEMCTL_USER", "")
	t.Setenv("SUDO_USER", "")
	t.Setenv("LOGNAME", "root")
	t.Setenv("USER", "root")
	t.Setenv("HOME", "/root")

	if got := systemctlMachineUser(); got != "" {
		t.Fatalf("expected empty machine user, got %q", got)
	}
}

func TestSystemctlCmdSetsBusEnvWhenEmpty_NoDuplicates(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", "")
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "")

	cmd := systemctlCmd(context.Background(), true, "", "is-active", "openclaw-gateway.service")
	env := envMap(cmd.Env)

	uid := os.Getuid()
	wantRuntime := "/run/user/" + strconv.Itoa(uid)
	if got := env["XDG_RUNTIME_DIR"]; got != wantRuntime {
		t.Fatalf("XDG_RUNTIME_DIR=%q, want %q", got, wantRuntime)
	}
	wantBus := "unix:path=" + wantRuntime + "/bus"
	if got := env["DBUS_SESSION_BUS_ADDRESS"]; got != wantBus {
		t.Fatalf("DBUS_SESSION_BUS_ADDRESS=%q, want %q", got, wantBus)
	}

	if countKey(cmd.Env, "XDG_RUNTIME_DIR") != 1 {
		t.Fatalf("expected single XDG_RUNTIME_DIR entry, got %d", countKey(cmd.Env, "XDG_RUNTIME_DIR"))
	}
	if countKey(cmd.Env, "DBUS_SESSION_BUS_ADDRESS") != 1 {
		t.Fatalf("expected single DBUS_SESSION_BUS_ADDRESS entry, got %d", countKey(cmd.Env, "DBUS_SESSION_BUS_ADDRESS"))
	}
}

func TestRunSystemctlFallsBackToMachineUser(t *testing.T) {
	dir := t.TempDir()
	systemctlPath := filepath.Join(dir, "systemctl")
	script := `#!/bin/sh
for arg in "$@"; do
  case "$arg" in
    --machine=*)
      echo active
      exit 0
      ;;
  esac
done
echo "Failed to connect to user scope bus via local transport: \$DBUS_SESSION_BUS_ADDRESS and \$XDG_RUNTIME_DIR not defined"
exit 1
`
	if err := os.WriteFile(systemctlPath, []byte(script), 0755); err != nil {
		t.Fatalf("write fake systemctl: %v", err)
	}

	t.Setenv("PATH", dir)
	t.Setenv("SUDO_USER", "ubuntu")
	t.Setenv("CORTEX_SYSTEMCTL_USER", "")
	t.Setenv("USER", "root")
	t.Setenv("LOGNAME", "root")
	t.Setenv("HOME", "/root")

	output, err := runSystemctl(context.Background(), true, "is-active", "openclaw-gateway.service")
	if err != nil {
		t.Fatalf("runSystemctl error: %v (output=%q)", err, output)
	}
	if strings.TrimSpace(output) != "active" {
		t.Fatalf("unexpected output: %q", output)
	}
}

func TestRunSystemctlFallsBackToUserScopeWhenSystemUnitMissing(t *testing.T) {
	dir := t.TempDir()
	systemctlPath := filepath.Join(dir, "systemctl")
	script := `#!/bin/sh
if [ "$1" = "--user" ]; then
  echo active
  exit 0
fi
echo "Unit $2 could not be found."
exit 1
`
	if err := os.WriteFile(systemctlPath, []byte(script), 0755); err != nil {
		t.Fatalf("write fake systemctl: %v", err)
	}

	t.Setenv("PATH", dir)
	t.Setenv("CORTEX_SYSTEMCTL_USER", "")
	t.Setenv("SUDO_USER", "")
	t.Setenv("LOGNAME", "")
	t.Setenv("USER", "ubuntu")
	t.Setenv("HOME", "/home/ubuntu")

	output, err := runSystemctl(context.Background(), false, "is-active", "openclaw-gateway.service")
	if err != nil {
		t.Fatalf("runSystemctl error: %v (output=%q)", err, output)
	}
	if strings.TrimSpace(output) != "active" {
		t.Fatalf("unexpected output: %q", output)
	}
}

func envMap(env []string) map[string]string {
	out := make(map[string]string, len(env))
	for _, entry := range env {
		parts := strings.SplitN(entry, "=", 2)
		if len(parts) != 2 {
			continue
		}
		out[parts[0]] = parts[1]
	}
	return out
}

func countKey(env []string, key string) int {
	prefix := key + "="
	count := 0
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			count++
		}
	}
	return count
}
