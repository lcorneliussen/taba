package main

import (
	"fmt"
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
	guard := installTestNetworkGuard()
	// Fixtures written against bb select it explicitly (the deprecated
	// compatibility client, #471); tests of the twg default clear it.
	os.Setenv("TABA_BITBUCKET_TRANSPORT", "bb")
	code := m.Run()
	if err := guard(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		code = 1
	}
	os.Exit(code)
}
