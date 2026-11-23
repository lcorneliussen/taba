package wtc

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// networkGuardLog records intercepted client launches; the guard's own test
// clears what it provoked.
var networkGuardLog string

// installTestNetworkGuard keeps this package's tests off real forges (#471).
// The developer's own twg and bb are shadowed by scripts that record the
// attempt and fail; tests that need a client put their own fake earlier on
// PATH. HTTP(S) traffic goes through a closed local proxy, so a request that
// escapes a test's local server fails instead of reaching a real host
// (loopback servers are never proxied). check reports any recorded attempt.
func installTestNetworkGuard() (check func() error) {
	dir, err := os.MkdirTemp("", "taba-network-guard-")
	if err != nil {
		panic(err)
	}
	log := filepath.Join(dir, "attempts.log")
	networkGuardLog = log
	gitConfig := filepath.Join(dir, "gitconfig")
	if err := os.WriteFile(gitConfig, []byte("[credential]\n\thelper =\n[user]\n\tname = Synthetic Test\n\temail = test@example.invalid\n"), 0o600); err != nil {
		panic(err)
	}
	os.Setenv("GIT_CONFIG_GLOBAL", gitConfig)
	os.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	os.Setenv("GIT_TERMINAL_PROMPT", "0")
	for _, tool := range []string{"twg", "bb"} {
		script := "#!/bin/sh\nprintf '%s %s\\n' \"$(basename \"$0\")\" \"$*\" >> '" + log + "'\necho 'network guard: real " + tool + " is not available to tests' >&2\nexit 97\n"
		if err := os.WriteFile(filepath.Join(dir, tool), []byte(script), 0o755); err != nil {
			panic(err)
		}
	}
	// Builds that tests start use the local module and build caches, found
	// before a fixture moves HOME, and never download modules or toolchains.
	for _, name := range []string{"GOMODCACHE", "GOCACHE"} {
		if os.Getenv(name) == "" {
			if out, err := exec.Command("go", "env", name).Output(); err == nil && len(out) > 1 {
				os.Setenv(name, strings.TrimSpace(string(out)))
			}
		}
	}
	os.Setenv("GOPROXY", "off")
	os.Setenv("GOTOOLCHAIN", "local")
	os.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	for _, name := range []string{"HTTPS_PROXY", "HTTP_PROXY", "https_proxy", "http_proxy", "ALL_PROXY"} {
		os.Setenv(name, "http://127.0.0.1:9")
	}
	os.Unsetenv("NO_PROXY")
	os.Unsetenv("no_proxy")
	return func() error {
		defer os.RemoveAll(dir)
		if raw, _ := os.ReadFile(log); len(raw) > 0 {
			return fmt.Errorf("tests invoked a real forge client; give them a fake earlier on PATH:\n%s", raw)
		}
		return nil
	}
}
