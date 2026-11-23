package wtc

import (
	"fmt"
	"net/url"
	"os"
	"path"
	"regexp"
	"strings"
)

// Where taba finds a repository's service declarations and which URL path
// checks a service's health are set in the harness [runtime] table and can be
// overridden from the environment:
//
//	[runtime]
//	declarations = ["dekit.yaml", ".harness/dekit.tasks.yaml"]
//	health_path  = "/health"
//
// TABA_RUNTIME_DECLARATIONS (comma-separated) and TABA_RUNTIME_HEALTH_PATH win
// over the file, and WTC_ spellings are accepted; an empty value counts as
// unset. Only the process environment is read, never collection env files.

const (
	runtimeDeclarationsVariable = "TABA_RUNTIME_DECLARATIONS"
	runtimeHealthPathVariable   = "TABA_RUNTIME_HEALTH_PATH"
	runtimeMaxDeclarations      = 32
)

// runtimeDefaultDeclarations are read when nothing is configured: a native
// dekit project at the repository root and taba's committed task file.
var runtimeDefaultDeclarations = []string{"dekit.yaml", ".harness/dekit.tasks.yaml"}

// runtimeDeclarationFile is one configured declaration file. A file named
// dekit.yaml is a native dekit project; any other is a taba task file.
type runtimeDeclarationFile struct {
	rel    string // repository-relative, slash-separated
	native bool
}

// runtimeEnvSetting returns the first non-empty of TABA_<name> and WTC_<name>
// with the variable it came from. An empty value counts as unset, so it never
// hides the other spelling or the harness setting.
func runtimeEnvSetting(variable string) (string, string, bool) {
	suffix := strings.TrimPrefix(variable, "TABA_")
	for _, name := range []string{"TABA_" + suffix, "WTC_" + suffix} {
		// Each spelling is read on its own: envcompat would let an empty
		// TABA_ value hide WTC_.
		if value, set := os.LookupEnv(name); set && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value), name, true
		}
	}
	return "", "", false
}

func runtimeSetting(variable string, configured string) (string, string) {
	if value, name, set := runtimeEnvSetting(variable); set {
		return value, name
	}
	return configured, "[runtime]"
}

// runtimeDeclarationFiles returns the effective declaration files. A
// configured list replaces the default one; it is never appended to it.
func (c *Context) runtimeDeclarationFiles() ([]runtimeDeclarationFile, error) {
	entries := c.Config.Runtime.Declarations
	origin := "[runtime] declarations"
	if value, name, set := runtimeEnvSetting(runtimeDeclarationsVariable); set {
		entries, origin = nil, name
		for _, entry := range strings.Split(value, ",") {
			entries = append(entries, strings.TrimSpace(entry))
		}
	} else if entries == nil {
		entries = runtimeDefaultDeclarations
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("%s names no file; leave it unset for the default %s", origin, strings.Join(runtimeDefaultDeclarations, ", "))
	}
	if len(entries) > runtimeMaxDeclarations {
		return nil, fmt.Errorf("%s names more than %d files", origin, runtimeMaxDeclarations)
	}
	seen := map[string]bool{}
	files := make([]runtimeDeclarationFile, 0, len(entries))
	for _, entry := range entries {
		if err := runtimeDeclarationPathValid(entry); err != nil {
			return nil, fmt.Errorf("%s: %q %v", origin, entry, err)
		}
		if seen[entry] {
			return nil, fmt.Errorf("%s names %s twice", origin, entry)
		}
		seen[entry] = true
		files = append(files, runtimeDeclarationFile{rel: entry, native: path.Base(entry) == "dekit.yaml"})
	}
	return files, nil
}

func runtimeDeclarationPathValid(entry string) error {
	switch {
	case entry == "":
		return fmt.Errorf("is empty")
	case strings.ContainsAny(entry, "\\*?[]{}\x00\r\n\t"):
		return fmt.Errorf("must be a plain file path, without glob or control characters")
	case strings.HasPrefix(entry, "/") || strings.HasPrefix(entry, "~"):
		return fmt.Errorf("must be relative to the repository")
	case path.Clean(entry) != entry || strings.HasSuffix(entry, "/"):
		return fmt.Errorf("must be a clean path to a file")
	}
	for _, part := range strings.Split(entry, "/") {
		if part == "." || part == ".." {
			return fmt.Errorf("must stay inside the repository")
		}
		if runtimeGitDir(part) {
			return fmt.Errorf("must not be inside .git")
		}
	}
	return nil
}

// runtimeHealthPathPattern is a URL path of RFC 3986 path characters.
var runtimeHealthPathPattern = regexp.MustCompile(`^/[A-Za-z0-9._~!$&'()*+,;=:@%/-]{0,255}$`)

// runtimeHealthPath returns the URL path, such as /health, that taba appends to
// a service's local address when it makes the readiness check itself. Empty
// means none: readiness is the port accepting connections.
func (c *Context) runtimeHealthPath() (string, error) {
	value, origin := runtimeSetting(runtimeHealthPathVariable, c.Config.Runtime.HealthPath)
	if value == "" {
		return "", nil
	}
	if err := runtimeHealthPathValid(value); err != nil {
		if origin == "[runtime]" {
			origin = "[runtime] health_path"
		}
		return "", fmt.Errorf("%s: %q %v", origin, value, err)
	}
	return value, nil
}

func runtimeHealthPathValid(value string) error {
	if !runtimeHealthPathPattern.MatchString(value) || strings.HasPrefix(value, "//") {
		return fmt.Errorf("must be a URL path such as /health: no scheme, host, credentials, query or fragment")
	}
	if _, err := url.PathUnescape(value); err != nil {
		return fmt.Errorf("has an invalid percent-escape")
	}
	for _, part := range strings.Split(value, "/") {
		if part == "." || part == ".." {
			return fmt.Errorf("must not contain . or .. segments")
		}
	}
	return nil
}
