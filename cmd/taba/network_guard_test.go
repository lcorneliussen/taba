package main

import (
	"fmt"
	"os"
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
//
// A test binary that a test starts again as a child process inherits the
// guard (its proxy, git and Go settings, and the log through
// TEST_NETWORK_GUARD_LOG) and keeps the PATH its parent test chose for
// it: a fixture's own fake client first. The parent checks the shared log.
func installTestNetworkGuard() (check func() error) {
	if inherited := os.Getenv("TEST_NETWORK_GUARD_LOG"); inherited != "" {
		networkGuardLog = inherited
		return func() error { return nil }
	}
	dir, err := os.MkdirTemp("", "taba-network-guard-")
	if err != nil {
		panic(err)
	}
	log := filepath.Join(dir, "attempts.log")
	networkGuardLog = log
	os.Setenv("TEST_NETWORK_GUARD_LOG", log)
	gitConfig := filepath.Join(dir, "gitconfig")
	if err := os.WriteFile(gitConfig, []byte("[credential]\n\thelper =\n[user]\n\tname = Synthetic Test\n\temail = test@example.invalid\n"), 0o600); err != nil {
		panic(err)
	}
	os.Setenv("GIT_CONFIG_GLOBAL", gitConfig)
	os.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	os.Setenv("GIT_TERMINAL_PROMPT", "0")
	for _, tool := range []string{"twg", "bb"} {
		// The record names the launch and its parent (a test binary's -test.run
		// or a taba subcommand), so the test that missed a fake can be found.
		script := "#!/bin/sh\nprintf '%s %s (from: %s)\\n' \"$(basename \"$0\")\" \"$*\" \"$(ps -o args= -p $PPID 2>/dev/null | cut -c1-160)\" >> '" + log + "'\necho 'network guard: real " + tool + " is not available to tests' >&2\nexit 97\n"
		if err := os.WriteFile(filepath.Join(dir, tool), []byte(script), 0o755); err != nil {
			panic(err)
		}
	}
	// Builds that tests start use the local module and build caches, found
	// from the environment before a fixture moves HOME (never by running go,
	// which could itself fetch a toolchain), and never download modules or
	// toolchains.
	home := os.Getenv("HOME")
	if os.Getenv("GOMODCACHE") == "" && home != "" {
		gopath := os.Getenv("GOPATH")
		if gopath == "" {
			gopath = filepath.Join(home, "go")
		}
		os.Setenv("GOMODCACHE", filepath.Join(strings.Split(gopath, string(os.PathListSeparator))[0], "pkg", "mod"))
	}
	if os.Getenv("GOCACHE") == "" {
		if cache, err := os.UserCacheDir(); err == nil {
			os.Setenv("GOCACHE", filepath.Join(cache, "go-build"))
		}
	}
	os.Setenv("GOPROXY", "off")
	os.Setenv("GOTOOLCHAIN", "local")
	os.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	for _, name := range []string{"HTTPS_PROXY", "HTTP_PROXY", "https_proxy", "http_proxy", "ALL_PROXY"} {
		os.Setenv(name, "http://127.0.0.1:9")
	}
	// Loopback stays direct for every tool, not only Go: tests run local
	// servers and runtimes.
	os.Setenv("NO_PROXY", "127.0.0.1,localhost,::1")
	os.Setenv("no_proxy", "127.0.0.1,localhost,::1")
	return func() error {
		defer os.RemoveAll(dir)
		if raw, _ := os.ReadFile(log); len(raw) > 0 {
			return fmt.Errorf("tests invoked a real forge client; give them a fake earlier on PATH:\n%s", raw)
		}
		return nil
	}
}

// networkGuardFixture installs the network guard as a testenv fixture (#471).
// Fixtures written against bb select it explicitly, the deprecated
// compatibility client; no-bb fixtures clear this and run the twg default.
// Its cleanup fails the run when a real forge client was launched.
func networkGuardFixture() (cleanup func() error) {
	check := installTestNetworkGuard()
	os.Setenv("TABA_BITBUCKET_TRANSPORT", "bb")

	return check
}
