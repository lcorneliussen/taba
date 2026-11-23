package wtc

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The guard is evidence only if it demonstrably stops an unmocked call. This
// test makes each kind of call a careless fixture could make and shows it is
// stopped on this host, before any real client or remote is reached.
func TestNetworkGuardStopsUnmockedCalls(t *testing.T) {
	t.Cleanup(func() { os.WriteFile(networkGuardLog, nil, 0o600) })
	t.Setenv("TABA_BITBUCKET_TRANSPORT", "") // the twg default

	// 1. An unmocked TWG read through the production adapter: the shadow
	// runs instead of the installed client and the read fails.
	path, err := exec.LookPath("twg")
	if err != nil || filepath.Dir(path) != filepath.Dir(networkGuardLog) {
		t.Fatalf("twg resolves to %q, not the guard", path)
	}
	twgVersionChecks.Clear()
	_, err = bitbucketReadPR(bitbucketReadOperation(context.Background(), ""), "acme/unmocked", "1")
	if err == nil {
		t.Fatal("an unmocked TWG read succeeded")
	}
	raw, _ := os.ReadFile(networkGuardLog)
	if !strings.Contains(string(raw), "twg --version") {
		t.Fatalf("the guard did not intercept the TWG launch: %q", raw)
	}
	if bb, _ := exec.LookPath("bb"); filepath.Dir(bb) != filepath.Dir(networkGuardLog) {
		t.Fatalf("bb resolves to %q, not the guard", bb)
	}

	// 2. Git credential helpers: no configured helper may answer. Checked
	// before any fill, so a misconfigured guard never queries a real store.
	out, _ := exec.Command("git", "config", "--show-scope", "--get-all", "credential.helper").Output()
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if fields := strings.Fields(line); len(fields) > 1 {
			t.Fatalf("a credential helper is configured in scope %s; refusing to run a fill", fields[0])
		}
	}
	bitbucketHostCredentials.Clear()
	if _, err := gitCredentialFill(context.Background(), "acme", "unmocked"); err == nil {
		t.Fatal("a git credential fill answered inside the guard")
	}

	// 3. REST: the status root is a closed local port, and any other HTTP(S)
	// request goes through the closed local proxy.
	if !strings.HasPrefix(statusBBAPI, "http://127.0.0.1:9/") {
		t.Fatalf("status REST root is %q", statusBBAPI)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.bitbucket.invalid/2.0/user", nil)
	if response, err := http.DefaultClient.Do(request); err == nil {
		response.Body.Close()
		t.Fatal("an HTTPS request left the guard")
	} else if errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "proxyconnect") {
		t.Fatalf("the HTTPS request was not refused by the local proxy: %v", err)
	}
}
