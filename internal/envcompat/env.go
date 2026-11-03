// Package envcompat reads product settings during the wtc-to-taba transition.
package envcompat

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"taba.sh/taba/internal/branding"
)

type lookupFunc func(string) (string, bool)

type resolver struct {
	warned sync.Map
	warn   func(string)
}

var process = resolver{warn: func(message string) { fmt.Fprintln(os.Stderr, message) }}

// Getenv accepts either spelling of a product setting. Other environment
// variables, including internal test and development controls, keep their names.
func Getenv(name string) string {
	value, _ := LookupEnv(name)
	return value
}

// LookupEnv distinguishes an explicit empty value from an unset setting.
func LookupEnv(name string) (string, bool) {
	return Lookup(name, os.LookupEnv)
}

// Lookup applies the same precedence to a single source, such as a settings
// file. Callers retain control of precedence between environment and file.
func Lookup(name string, lookup lookupFunc) (string, bool) {
	return process.lookup(name, lookup)
}

func (r *resolver) lookup(name string, lookup lookupFunc) (string, bool) {
	suffix := name
	if strings.HasPrefix(name, "WTC_") {
		suffix = strings.TrimPrefix(name, "WTC_")
	} else if strings.HasPrefix(name, "TABA_") {
		suffix = strings.TrimPrefix(name, "TABA_")
	}
	if suffix == name || suffix == "" || strings.HasPrefix(suffix, "TEST_") || strings.HasPrefix(suffix, "DEV_") {
		return lookup(name)
	}
	current, legacy := "TABA_"+suffix, "WTC_"+suffix
	value, set := lookup(current)
	old, oldSet := lookup(legacy)
	if !set {
		return old, oldSet
	}
	if oldSet && value != old {
		if _, warned := r.warned.LoadOrStore(suffix, true); !warned {
			r.warn(fmt.Sprintf(branding.Text("{{command}}: %s and %s differ; using %s"), current, legacy, current))
		}
	}
	return value, true
}

// Assignments reads literal KEY=value settings, with optional export and
// matching outer quotes. It never evaluates shell expressions or commands.
// The last assignment of each spelling wins, independent of prefix precedence.
func Assignments(data []byte) map[string]string {
	values := make(map[string]string)
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimPrefix(strings.TrimSpace(line), "export ")
		key, value, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		if len(value) >= 2 && (value[0] == '\'' && value[len(value)-1] == '\'' || value[0] == '"' && value[len(value)-1] == '"') {
			value = value[1 : len(value)-1]
		}
		values[key] = value
	}
	return values
}

// Both returns a generated setting under its legacy WTC_ name and its TABA_
// name, legacy first. Generated environments and child processes carry both
// spellings with one value: readers prefer TABA_, so a stale TABA_ value
// inherited from another collection must never outlive a fresh WTC_ one.
func Both(legacy, value string) []string {
	if !strings.HasPrefix(legacy, "WTC_") {
		panic("envcompat.Both: " + legacy + " is not a WTC_ setting")
	}
	return []string{legacy + "=" + value, "TABA_" + strings.TrimPrefix(legacy, "WTC_") + "=" + value}
}

// LookupMap reads settings previously parsed by Assignments.
func LookupMap(name string, values map[string]string) (string, bool) {
	return Lookup(name, func(key string) (string, bool) { value, ok := values[key]; return value, ok })
}
