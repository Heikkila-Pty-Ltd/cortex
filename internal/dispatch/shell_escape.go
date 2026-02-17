package dispatch

import (
	"strings"
	"unicode"
)

// ShellEscape properly escapes a string for safe use in shell commands.
// It handles all shell metacharacters that could cause command injection.
func ShellEscape(s string) string {
	if s == "" {
		return "''"
	}

	// If the string contains only safe characters, return as-is
	if isSafeForShell(s) {
		return s
	}

	// Use single quotes and escape any single quotes within
	// Replace ' with '\''
	escaped := strings.ReplaceAll(s, "'", "'\"'\"'")
	return "'" + escaped + "'"
}

// isSafeForShell returns true if the string contains only characters
// that are safe to use in shell commands without quoting
func isSafeForShell(s string) bool {
	if s == "" {
		return false
	}

	for _, r := range s {
		if !isSafeShellChar(r) {
			return false
		}
	}
	return true
}

// isSafeShellChar returns true if the rune is safe in shell commands
func isSafeShellChar(r rune) bool {
	if unicode.IsLetter(r) || unicode.IsDigit(r) {
		return true
	}

	// Safe punctuation characters
	switch r {
	case '-', '_', '.', '/', '+', '=', ':':
		return true
	default:
		return false
	}
}

// ShellEscapeArgs escapes a slice of arguments for shell use
func ShellEscapeArgs(args []string) []string {
	escaped := make([]string, len(args))
	for i, arg := range args {
		escaped[i] = ShellEscape(arg)
	}
	return escaped
}

// BuildShellCommand safely builds a shell command string from a program and arguments
func BuildShellCommand(program string, args ...string) string {
	if len(args) == 0 {
		return ShellEscape(program)
	}

	var parts []string
	parts = append(parts, ShellEscape(program))
	for _, arg := range args {
		parts = append(parts, ShellEscape(arg))
	}
	return strings.Join(parts, " ")
}

// isValidEnvVarName validates that a string is a valid environment variable name
// to prevent injection attacks via malformed variable names
func isValidEnvVarName(name string) bool {
	if len(name) == 0 {
		return false
	}
	
	// Must start with letter or underscore
	if !((name[0] >= 'A' && name[0] <= 'Z') || 
		 (name[0] >= 'a' && name[0] <= 'z') || 
		 name[0] == '_') {
		return false
	}
	
	// Rest can be letters, digits, or underscores
	for i := 1; i < len(name); i++ {
		c := name[i]
		if !((c >= 'A' && c <= 'Z') || 
			 (c >= 'a' && c <= 'z') || 
			 (c >= '0' && c <= '9') || 
			 c == '_') {
			return false
		}
	}
	
	return true
}