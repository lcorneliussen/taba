package wtc

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"taba.sh/taba/internal/envcompat"
)

type GeneratedEnvRepair struct {
	Collection string `json:"collection"`
	Changed    bool   `json:"changed"`
	DryRun     bool   `json:"dry_run"`
	Env        string `json:"env"`
}

// Only generated literal assignments are repairable without evaluating shell
// code. User-local configuration is validated and preserved byte for byte.
var generatedAssignment = regexp.MustCompile("^(?:export )?([A-Za-z_][A-Za-z0-9_]*)=(?:'[^'\\r\\n]*'|\"[^\"$`\\\\\\r\\n]*\"|[A-Za-z0-9_./:-]*)$")

// RepairGeneratedEnvironment changes only one existing generated environment.
// It never opens a normal Context, reads secret directories, or executes code.
func RepairGeneratedEnvironment(start string, dryRun bool) (GeneratedEnvRepair, error) {
	var result GeneratedEnvRepair
	location, err := DiscoverLocation(start)
	if err != nil {
		return result, err
	}
	if location.Collection == "" {
		return result, fmt.Errorf("generated-root repair requires an actual collection")
	}
	result.Collection, result.DryRun = location.Collection, dryRun
	receiver := ReceivingContext{Project: location.Project, Collection: location.Collection}
	scope, err := receiver.resolveEnvironment(false, false)
	if err != nil {
		return result, err
	}
	path := filepath.Join(result.Collection, ".env.collection")
	old, err := os.ReadFile(path)
	if err != nil {
		return result, fmt.Errorf("generated-root repair requires an existing regular generated environment")
	}
	var out bytes.Buffer
	for _, line := range strings.Split(strings.TrimSuffix(string(old), "\n"), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			fmt.Fprintln(&out, line)
			continue
		}
		match := generatedAssignment.FindStringSubmatch(trimmed)
		if match == nil {
			return result, fmt.Errorf("generated-root repair refuses nonliteral shell configuration; no files changed")
		}
		switch match[1] {
		case "TABA_CONFIG_ROOT", "WTC_CONFIG_ROOT", "GH_CONFIG_DIR", "JIRA_CONFIG_FILE", "TWG_SITE":
			// Derived provider hints must not retain the old source. Ordinary env
			// setup can regenerate them after the invoking shell is refreshed.
			continue
		}
		fmt.Fprintln(&out, line)
	}
	value, err := quotedValue(scope.Root)
	if err != nil {
		return result, err
	}
	for _, setting := range envcompat.Both("WTC_CONFIG_ROOT", value) {
		fmt.Fprintln(&out, setting)
	}
	data := out.Bytes()
	result.Changed, result.Env = !bytes.Equal(old, data), string(data)
	if dryRun || !result.Changed {
		return result, nil
	}
	tmp, err := os.CreateTemp(result.Collection, ".env.collection.tmp-")
	if err != nil {
		return result, err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return result, err
	}
	if err := tmp.Chmod(0644); err != nil {
		tmp.Close()
		return result, err
	}
	if err := tmp.Close(); err != nil {
		return result, err
	}
	// Re-discover association and re-read policy/local overrides immediately
	// before the atomic write, without granting authority to caller aliases.
	currentLocation, err := DiscoverLocation(result.Collection)
	if err != nil {
		return result, err
	}
	current, err := (ReceivingContext{Project: currentLocation.Project, Collection: result.Collection}).resolveEnvironment(false, false)
	if err != nil {
		return result, err
	}
	if current != scope || currentLocation.Collection != result.Collection {
		return result, fmt.Errorf("receiving policy changed during repair; retry; no files changed")
	}
	unchanged, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(old, unchanged) {
		return result, fmt.Errorf("generated environment changed during repair; retry; no files changed")
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return result, err
	}
	return result, nil
}
