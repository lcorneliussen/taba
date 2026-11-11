package main

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"taba.sh/taba/internal/openclaw"
)

func TestSandboxChecks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/wtc/session" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"repo":"api","branch":"openclaw/12-x","base":"main","repos":["api","web"]}`))
	}))
	defer srv.Close()
	gw := openclaw.GatewayFromEnv(func(string) string { return srv.URL })
	nothing := func(name string, args ...string) (string, error) { return "", errors.New("no") }

	checks := sandboxChecks(gw, func(string) string { return "" }, nothing)
	if len(checks) != 3 || !checks[0].OK || !strings.Contains(checks[0].Detail, "2 repos: api, web") || !checks[1].OK {
		t.Fatalf("a clean sandbox: %+v", checks)
	}
	if checks[2].Name != "projects" || checks[2].OK || checks[2].Required || !strings.Contains(checks[2].Detail, "discovery unavailable") {
		t.Fatalf("old gateway without project discovery: %+v", checks[2])
	}

	env := map[string]string{"GH_TOKEN": "x"}
	helper := func(name string, args ...string) (string, error) {
		if name == "git" {
			return "store\n", nil
		}
		return "", errors.New("no")
	}
	checks = sandboxChecks(gw, func(k string) string { return env[k] }, helper)
	if checks[1].OK || !strings.Contains(checks[1].Detail, "GH_TOKEN") || !strings.Contains(checks[1].Detail, "credential.helper") {
		t.Fatalf("a sandbox with credentials: %+v", checks[1])
	}

	srv.Close()
	if checks := sandboxChecks(gw, func(string) string { return "" }, nothing); checks[0].OK {
		t.Fatalf("an unreachable service: %+v", checks[0])
	}
}

func TestSandboxCatchUpTakesOnlyContinue(t *testing.T) {
	t.Setenv(openclaw.GatewayGitEnv, "http://127.0.0.1:1")
	for _, args := range [][]string{{"--from", "a"}, {"--dest", "develop"}, {"--all"}, {"other-collection"}} {
		var asJSON bool
		root := &cobra.Command{Use: "wtc", SilenceUsage: true, SilenceErrors: true}
		addCatchUpCommand(root, &asJSON)
		root.SetArgs(append([]string{"catch-up"}, args...))
		root.SetOut(io.Discard)
		if err := root.Execute(); err == nil || !strings.Contains(err.Error(), "takes only --continue") {
			t.Errorf("catch-up %v in a sandbox: %v", args, err)
		}
	}
}

func TestSandboxChecksFindAGhLogin(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"repo":"api","branch":"openclaw/12-x","base":"main","repos":["api"]}`))
	}))
	defer srv.Close()
	gw := openclaw.GatewayFromEnv(func(string) string { return srv.URL })
	gh := func(name string, args ...string) (string, error) {
		if name == "gh" {
			return "gho_x\n", nil
		}
		return "", errors.New("no")
	}
	if checks := sandboxChecks(gw, func(string) string { return "" }, gh); checks[1].OK || !strings.Contains(checks[1].Detail, "gh login") {
		t.Fatalf("a gh login in the sandbox: %+v", checks[1])
	}
}
