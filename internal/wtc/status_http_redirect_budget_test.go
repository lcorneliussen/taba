package wtc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestStatusHTTPRedirectOneToken(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("WTC_FORGE_MIN_INTERVAL", "60")
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path == "/start" {
			w.Header().Set("Location", "/end")
			w.WriteHeader(302)
			return
		}
		w.Write([]byte(`{"values":[]}`))
	}))
	defer server.Close()
	_, live, err := statusWithForgeBudget("bitbucket.org", nil, func() ([]byte, error) { return statusBBGET(server.URL+"/start", "example", "secret") })
	if calls.Load() != 1 || live || !errors.Is(err, errForgeBudget) {
		t.Fatalf("wire requests=%d live=%v error=%v; want 1, false, budget", calls.Load(), live, err)
	}
}

func redirectBudget(t *testing.T, tokens int) {
	t.Helper()
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("WTC_FORGE_MIN_INTERVAL", "")
	statusWriteForgeEntry("budget", "bitbucket.org", "", "", []byte(fmt.Sprintf("%d %d\n", tokens, time.Now().UnixNano())))
}

func TestStatusHTTPRedirectAllowed(t *testing.T) {
	for _, code := range []int{301, 302, 303, 307, 308} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			redirectBudget(t, 3)
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				u, p, ok := r.BasicAuth()
				if !ok || u != "example" || p != "secret" || r.Header.Get("Accept") != "application/json" {
					t.Error("same-origin credentials/headers lost")
				}
				if r.URL.Path != "/end" {
					w.Header().Set("Location", "end")
					w.WriteHeader(code)
					return
				}
				fmt.Fprint(w, `{"values":[]}`)
			}))
			defer server.Close()
			raw, live, err := statusWithForgeBudget("bitbucket.org", nil, func() ([]byte, error) { return statusBBGET(server.URL+"/start", "example", "secret") })
			if err != nil || !live || string(raw) != `{"values":[]}` || calls.Load() != 2 {
				t.Fatalf("requests=%d live=%v body=%s err=%v", calls.Load(), live, raw, err)
			}
			// Two dispatches leave exactly one token. The caller's initial debit is not repeated.
			if statusTakeForgeToken(context.Background(), "bitbucket.org") != nil || statusTakeForgeToken(context.Background(), "bitbucket.org") == nil {
				t.Fatal("incorrect redirect accounting")
			}
		})
	}
}

func TestStatusHTTPRedirectCooling(t *testing.T) {
	redirectBudget(t, 2)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		statusForgeCool("bitbucket.org", time.Minute)
		w.Header().Set("Location", "/end")
		w.WriteHeader(302)
	}))
	defer server.Close()
	_, live, err := statusWithForgeBudget("bitbucket.org", nil, func() ([]byte, error) { return statusBBGET(server.URL, "example", "secret") })
	if !errors.Is(err, errForgeCooling) || live || calls.Load() != 1 {
		t.Fatalf("requests=%d live=%v err=%v", calls.Load(), live, err)
	}
}

func TestStatusHTTPRedirectUnsafe(t *testing.T) {
	var leaked atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked.Add(1); t.Error("unsafe target reached") }))
	defer target.Close()
	for _, location := range []string{target.URL + "/private?token=sentinel", "//example.invalid/private?token=sentinel", "http://user:sentinel@SAME/end", "/%zz?token=sentinel", "file:///sentinel", ""} {
		t.Run(location, func(t *testing.T) {
			redirectBudget(t, 3)
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Location", strings.ReplaceAll(location, "SAME", r.Host))
				w.WriteHeader(302)
			}))
			defer server.Close()
			_, _, err := statusWithForgeBudget("bitbucket.org", nil, func() ([]byte, error) { return statusBBGET(server.URL, "example", "secret") })
			if err == nil || calls.Load() != 1 || leaked.Load() != 0 {
				t.Fatalf("requests=%d leaks=%d err=%v", calls.Load(), leaked.Load(), err)
			}
			if strings.Contains(err.Error(), "sentinel") || strings.Contains(err.Error(), server.URL) || strings.Contains(err.Error(), "secret") {
				t.Fatalf("sensitive redirect error: %v", err)
			}
			first, second := statusTakeForgeToken(context.Background(), "bitbucket.org") == nil, statusTakeForgeToken(context.Background(), "bitbucket.org") == nil
			if !first || !second {
				t.Fatal("unsafe redirect spent allowance")
			}
		})
	}
}

func TestStatusHTTPRedirectDowngrade(t *testing.T) {
	redirectBudget(t, 3)
	var leaked atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked.Add(1) }))
	defer target.Close()
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Location", target.URL+"/?token=sentinel")
		w.WriteHeader(302)
	}))
	defer server.Close()
	old := http.DefaultTransport
	http.DefaultTransport = server.Client().Transport
	t.Cleanup(func() { http.DefaultTransport = old })
	_, _, err := statusWithForgeBudget("bitbucket.org", nil, func() ([]byte, error) { return statusBBGET(server.URL, "example", "secret") })
	if err == nil || calls.Load() != 1 || leaked.Load() != 0 || strings.Contains(err.Error(), "sentinel") {
		t.Fatalf("requests=%d leaks=%d err=%v", calls.Load(), leaked.Load(), err)
	}
}

func TestStatusHTTPRedirectLoopBound(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("WTC_FORGE_MIN_INTERVAL", "0")
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Location", "/loop?token=sentinel")
		w.WriteHeader(302)
	}))
	defer server.Close()
	_, _, err := statusWithForgeBudget("bitbucket.org", nil, func() ([]byte, error) { return statusBBGET(server.URL, "example", "secret") })
	if err == nil || calls.Load() != 10 || strings.Contains(err.Error(), "sentinel") {
		t.Fatalf("requests=%d err=%v", calls.Load(), err)
	}
}

func TestStatusHTTPRedirectRateLimit(t *testing.T) {
	redirectBudget(t, 3)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/end" {
			w.Header().Set("Location", "/end")
			w.WriteHeader(302)
			return
		}
		w.WriteHeader(429)
		fmt.Fprint(w, "Too Many Requests")
	}))
	defer server.Close()
	raw, live, err := statusWithForgeBudget("bitbucket.org", []byte("stale"), func() ([]byte, error) { return statusBBGET(server.URL, "example", "secret") })
	if err != nil || live || string(raw) != "stale" || calls.Load() != 2 || !statusForgeNow().Before(statusForgeCoolingUntil("bitbucket.org")) {
		t.Fatalf("requests=%d live=%v body=%s err=%v", calls.Load(), live, raw, err)
	}
}

func redirectAPI(t *testing.T, endpoint string) {
	t.Helper()
	old := statusBBAPI
	statusBBAPI = endpoint
	t.Cleanup(func() { statusBBAPI = old })
	statusHideBitbucketLogin(t)
	t.Setenv("BB_USERNAME", "example")
	t.Setenv("BB_API_TOKEN", "secret")
}

func TestStatusHTTPRedirectPagination(t *testing.T) {
	for _, tokens := range []int{1, 3, 4} {
		t.Run(fmt.Sprint(tokens), func(t *testing.T) {
			redirectBudget(t, tokens)
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				switch r.URL.Path {
				case "/page2":
					w.Header().Set("Location", "/end")
					w.WriteHeader(302)
				case "/end":
					fmt.Fprint(w, `{"values":[{"id":8}]}`)
				default:
					fmt.Fprintf(w, `{"values":[{"id":7}],"next":"http://%s/page2"}`, r.Host)
				}
			}))
			defer server.Close()
			redirectAPI(t, server.URL)
			raw, live, err := statusWithForgeBudget("bitbucket.org", nil, func() ([]byte, error) { return statusFetchBBOpenList("example/widget") })
			if err != nil || !live {
				t.Fatalf("live=%v err=%v", live, err)
			}
			prs, partial, _, err := statusParseOpenPRs(raw)
			wantCount, wantCalls, wantPartial := 2, int32(3), false
			if tokens == 1 {
				wantCount, wantCalls, wantPartial = 1, 1, true
			}
			if len(prs) != wantCount || calls.Load() != wantCalls || partial != wantPartial || err != nil {
				t.Fatalf("items=%d partial=%v requests=%d err=%v", len(prs), partial, calls.Load(), err)
			}
		})
	}
	// Refusal on a later redirect preserves the first page as partial evidence.
	t.Run("redirect-refused", func(t *testing.T) {
		redirectBudget(t, 2)
		var calls atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			if r.URL.Path == "/page2" {
				w.Header().Set("Location", "/end")
				w.WriteHeader(302)
				return
			}
			fmt.Fprintf(w, `{"values":[{"id":7}],"next":"http://%s/page2"}`, r.Host)
		}))
		defer server.Close()
		redirectAPI(t, server.URL)
		raw, _, err := statusWithForgeBudget("bitbucket.org", nil, func() ([]byte, error) { return statusFetchBBOpenList("example/widget") })
		prs, partial, _, parseErr := statusParseOpenPRs(raw)
		if err != nil || parseErr != nil || !partial || len(prs) != 1 || calls.Load() != 2 {
			t.Fatalf("items=%d partial=%v requests=%d err=%v parse=%v", len(prs), partial, calls.Load(), err, parseErr)
		}
	})
}

func TestStatusHTTPRedirectCheckPages(t *testing.T) {
	for _, tokens := range []int{1, 2} {
		t.Run(fmt.Sprint(tokens), func(t *testing.T) {
			redirectBudget(t, tokens)
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.URL.Path != "/end" {
					w.Header().Set("Location", "/end")
					w.WriteHeader(302)
					return
				}
				fmt.Fprint(w, `{"values":[{"state":"SUCCESSFUL"}]}`)
			}))
			defer server.Close()
			redirectAPI(t, server.URL)
			consumed := 0
			complete := fetchBBCheckPages(server.URL, "example", "secret", func(json.RawMessage) error { consumed++; return nil })
			if calls.Load() != int32(tokens) || complete != (tokens == 2) || consumed != tokens-1 {
				t.Fatalf("requests=%d complete=%v consumed=%d", calls.Load(), complete, consumed)
			}
		})
	}
}

func TestStatusHTTPRedirectFallbackDeferred(t *testing.T) {
	redirectBudget(t, 1)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Location", "/end")
		w.WriteHeader(302)
	}))
	defer server.Close()
	redirectAPI(t, server.URL)
	bin := t.TempDir()
	marker := filepath.Join(bin, "called")
	t.Setenv("WTC_TEST_REDIRECT_CLI_MARKER", marker)
	if err := os.WriteFile(filepath.Join(bin, "bb"), []byte("#!/bin/sh\n: > \"$WTC_TEST_REDIRECT_CLI_MARKER\"\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	detail := statusEnrichRecordMetadata(PRRecord{Number: "7"}, "example/widget", "bitbucket.org")
	if detail.State != "" || detail.Freshness != "deferred" || calls.Load() != 1 {
		t.Fatalf("requests=%d detail=%+v", calls.Load(), detail)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("fallback CLI dispatched: %v", err)
	}
}

func TestStatusHTTPRedirectBranchDeferred(t *testing.T) {
	redirectBudget(t, 1)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Location", "/end")
		w.WriteHeader(302)
	}))
	defer server.Close()
	redirectAPI(t, server.URL)
	number, err := statusDiscoverBitbucketBranch("example/widget", "topic")
	if number != "" || !errors.Is(err, errForgeBudget) || calls.Load() != 1 {
		t.Fatalf("requests=%d number=%s err=%v", calls.Load(), number, err)
	}
}

func TestStatusHTTPRedirectDirectRead(t *testing.T) {
	redirectBudget(t, 1)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); fmt.Fprint(w, `{"values":[]}`) }))
	defer server.Close()
	_, live, err := statusWithForgeBudget("bitbucket.org", nil, func() ([]byte, error) { return statusBBGET(server.URL, "example", "secret") })
	if err != nil || !live || calls.Load() != 1 {
		t.Fatalf("requests=%d live=%v err=%v", calls.Load(), live, err)
	}
	if statusTakeForgeToken(context.Background(), "bitbucket.org") == nil {
		t.Fatal("initial request not charged")
	}
}
