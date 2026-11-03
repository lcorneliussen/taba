package main

import (
	"os"
	"strings"
	"testing"
)

// A collection shell carries generated TABA_ variables, and readers prefer
// them; tests that set only a WTC_ name must not read the developer's values.
func TestMain(m *testing.M) {
	for _, setting := range os.Environ() {
		if name, _, _ := strings.Cut(setting, "="); strings.HasPrefix(name, "TABA_") {
			os.Unsetenv(name)
		}
	}
	os.Exit(m.Run())
}
