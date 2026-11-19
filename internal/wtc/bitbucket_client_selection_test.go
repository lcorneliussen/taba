package wtc

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func clearBitbucketClientEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{"TABA_BITBUCKET_TRANSPORT", "WTC_BITBUCKET_TRANSPORT"} {
		t.Setenv(key, "")
		os.Unsetenv(key)
	}
}

// TestBitbucketClientMachineDefaults follows the machine defaults precedence
// of the TWG site default: the environment wins even when empty, then
// taba.env (or legacy wtc.env) in the control root, then the bb default.
func TestBitbucketClientMachineDefaults(t *testing.T) {
	for _, tc := range []struct {
		name, file, body, env string
		envSet                bool
		want                  bitbucketClient
		source                string
		err                   error
	}{
		{name: "no file", want: bitbucketClientBB, source: "default"},
		{name: "taba.env twg", file: "taba.env", body: "TABA_BITBUCKET_TRANSPORT=twg\n", want: bitbucketClientTWG, source: "taba.env"},
		{name: "taba.env quoted export", file: "taba.env", body: "export TABA_BITBUCKET_TRANSPORT='twg'\n", want: bitbucketClientTWG, source: "taba.env"},
		{name: "legacy wtc.env", file: "wtc.env", body: "WTC_BITBUCKET_TRANSPORT=twg\n", want: bitbucketClientTWG, source: "wtc.env"},
		{name: "file bb", file: "taba.env", body: "TABA_BITBUCKET_TRANSPORT=bb\n", want: bitbucketClientBB, source: "taba.env"},
		{name: "file without key", file: "taba.env", body: "TABA_TWG_SITE=example\n", want: bitbucketClientBB, source: "default"},
		{name: "environment wins", file: "taba.env", body: "TABA_BITBUCKET_TRANSPORT=twg\n", env: "bb", envSet: true, want: bitbucketClientBB, source: "environment"},
		{name: "explicit empty environment wins", file: "taba.env", body: "TABA_BITBUCKET_TRANSPORT=twg\n", env: "", envSet: true, want: bitbucketClientBB, source: "environment"},
		{name: "invalid in file", file: "taba.env", body: "TABA_BITBUCKET_TRANSPORT=secret-value\n", source: "taba.env", err: errBitbucketClientSelection},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clearBitbucketClientEnv(t)
			root := t.TempDir()
			if tc.file != "" {
				fixtureFile(t, filepath.Join(root, tc.file), tc.body, 0600)
			}
			if tc.envSet {
				t.Setenv("TABA_BITBUCKET_TRANSPORT", tc.env)
			}
			got, err := selectedBitbucketClient(root)
			if !errors.Is(err, tc.err) || got.Client != tc.want || got.Source != tc.source {
				t.Fatalf("got %+v, %v; want %s from %s, %v", got, err, tc.want, tc.source, tc.err)
			}
		})
	}
}

// TestBitbucketClientDefaultsFailClosed keeps an unreadable defaults file from
// silently meaning "use the default client".
func TestBitbucketClientDefaultsFailClosed(t *testing.T) {
	clearBitbucketClientEnv(t)
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "taba.env"), 0700); err != nil {
		t.Fatal(err)
	}
	if got, err := selectedBitbucketClient(root); !errors.Is(err, errBitbucketClientDefaults) || got.Client != "" {
		t.Fatalf("directory defaults: %+v, %v", got, err)
	}

	outside := t.TempDir()
	fixtureFile(t, filepath.Join(outside, "taba.env"), "TABA_BITBUCKET_TRANSPORT=twg\n", 0600)
	root = t.TempDir()
	if err := os.Symlink(filepath.Join(outside, "taba.env"), filepath.Join(root, "taba.env")); err != nil {
		t.Fatal(err)
	}
	if got, err := selectedBitbucketClient(root); !errors.Is(err, errBitbucketClientDefaults) || got.Client != "" {
		t.Fatalf("defaults outside the control root: %+v, %v", got, err)
	}
}

func TestBitbucketClientReport(t *testing.T) {
	for _, tc := range []struct {
		name, env, version string
		install            bool
		ok                 bool
		want               string
	}{
		{name: "default", ok: true, want: "bb (default)"},
		{name: "bb", env: "bb", ok: true, want: "bb from environment"},
		{name: "twg supported", env: "twg", version: "1.4.4", install: true, ok: true, want: "twg from environment; version supported"},
		{name: "twg old", env: "twg", version: "1.4.3", install: true, want: "version is unsupported"},
		{name: "twg missing", env: "twg", want: "twg is not installed"},
		{name: "invalid", env: "secret-value", want: "unsupported TABA_BITBUCKET_TRANSPORT value from environment"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clearBitbucketClientEnv(t)
			if tc.env != "" {
				t.Setenv("TABA_BITBUCKET_TRANSPORT", tc.env)
			}
			calls := ""
			if tc.install {
				calls = fakeTWG(t, tc.version, nil)
			} else {
				t.Setenv("PATH", t.TempDir())
				twgVersionChecks.Range(func(key, _ any) bool { twgVersionChecks.Delete(key); return true })
			}
			ok, detail := BitbucketClientReport(t.TempDir())
			if ok != tc.ok || !strings.Contains(detail, tc.want) {
				t.Fatalf("report = %v %q; want %v containing %q", ok, detail, tc.ok, tc.want)
			}
			if !tc.ok && !strings.HasPrefix(detail, "advisory: ") {
				t.Fatalf("failing report is not marked advisory: %q", detail)
			}
			if strings.Contains(detail, "secret-value") || strings.Contains(detail, "/") {
				t.Fatalf("report echoes a value or path: %q", detail)
			}
			if tc.install {
				lines := readCalls(t, calls)
				if len(lines) != 1 || !strings.Contains(lines[0], "|--version|") {
					t.Fatalf("doctor ran more than the version check: %q", lines)
				}
			}
		})
	}
}

// TestCatchUpUsesMachineDefaultSelectionPerOperation selects TWG through the
// machine defaults file, then switches it back: the next operation uses the
// newly selected client, and nothing read through the previous client is
// reused.
func TestCatchUpUsesMachineDefaultSelectionPerOperation(t *testing.T) {
	clearBitbucketClientEnv(t)
	c, bbCalls, twgCalls := catchUpBitbucketFixture(t, "pr-open.json")
	defaults := filepath.Join(c.ConfigRoot, "taba.env")
	target := catchUpTarget{collection: "main", path: c.Harness, repo: "agent-harness", harness: true}

	fixtureFile(t, defaults, "TABA_BITBUCKET_TRANSPORT=twg\n", 0600)
	if got := c.catchUpPRState(target, "topic"); got.state != "OPEN" || got.base != "main" {
		t.Fatalf("twg from defaults: %+v", got)
	}
	if len(readCalls(t, twgCalls)) == 0 || len(readCalls(t, bbCalls)) != 0 {
		t.Fatal("defaults selection did not route the read through twg alone")
	}

	fixtureFile(t, defaults, "TABA_BITBUCKET_TRANSPORT=bb\n", 0600)
	twgBefore := len(readCalls(t, twgCalls))
	if got := c.catchUpPRState(target, "topic"); got.state != "OPEN" || got.base != "main" {
		t.Fatalf("bb after switch: %+v", got)
	}
	if len(readCalls(t, bbCalls)) != 1 || len(readCalls(t, twgCalls)) != twgBefore {
		t.Fatal("changed selection did not take effect on the next operation")
	}

	fixtureFile(t, defaults, "TABA_BITBUCKET_TRANSPORT=secret-value\n", 0600)
	got := c.catchUpPRState(target, "topic")
	if got.state != "UNKNOWN" || !strings.Contains(got.lookupFailure, "unsupported TABA_BITBUCKET_TRANSPORT value in taba.env") || strings.Contains(got.lookupFailure, "secret-value") {
		t.Fatalf("invalid defaults selection: %+v", got)
	}
	if len(readCalls(t, bbCalls)) != 1 || len(readCalls(t, twgCalls)) != twgBefore {
		t.Fatal("a client ran despite an invalid defaults selection")
	}
}
