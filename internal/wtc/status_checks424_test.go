package wtc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func checks424Seed(t *testing.T, head, result string) headCheckEvidence {
	t.Helper()
	e := headCheckEvidence{Head: head, Result: result, Complete: true, ObservedAt: time.Now().Add(-time.Hour).UTC().Format(time.RFC3339), Requiredness: "unknown"}
	raw, _ := json.Marshal(e)
	statusWriteForgeEntry("checks", "bitbucket.org", "example/widget", head, raw)
	return e
}

func TestStatusChecks424ForegroundStatesAndCost(t *testing.T) {
	for _, state := range []string{"OPEN", "DRAFT"} {
		for _, check := range []string{"FAILED", "INPROGRESS", "SUCCESSFUL", ""} {
			t.Run(state+"/"+check, func(t *testing.T) {
				calls := foreground424Fixture(t, fmt.Sprintf(`printf '%%s' '{"id":7,"state":"OPEN","draft":%v,"source":{"commit":{"hash":%q}}}'`, state == "DRAFT", checkHeadB))
				hits := 0
				setupHeadCheckServer(t, func(w http.ResponseWriter, r *http.Request) {
					hits++
					if strings.Contains(r.URL.Path, "/statuses") && check != "" {
						fmt.Fprintf(w, `{"values":[{"state":%q}]}`, check)
					} else {
						fmt.Fprint(w, `{"values":[]}`)
					}
				})
				// Restore real shared accounting after the server fixture's legacy override.
				t.Setenv("WTC_FORGE_MIN_INTERVAL", "")
				checks424Seed(t, checkHeadB, "FAILURE")
				foreground424Budget(t, "bitbucket.org", 8, time.Now())
				got, out := statusRefreshRecord(context.Background(), StatusRefreshForeground, PRRecord{Number: "7"}, "example/widget", "bitbucket.org")
				want := map[string]string{"FAILED": "FAILURE", "INPROGRESS": "PENDING", "SUCCESSFUL": "SUCCESS", "": "NONE"}[check]
				if state == "DRAFT" && want == "NONE" {
					want = "draft"
				}
				if got.Checks != want || got.State != state || hits != 2 || foreground424Count(calls) != 1 || out.Freshness != "live" {
					t.Fatalf("detail=%+v outcome=%+v hits=%d", got, out, hits)
				}
				raw, _ := os.ReadFile(statusForgeStatePath("budget", "bitbucket.org"))
				var tokens int
				fmt.Sscanf(string(raw), "%d", &tokens)
				if tokens != 5 {
					t.Fatalf("want exactly metadata+2 pages, budget=%s", raw)
				}
			})
		}
	}
}

func TestStatusChecks424IncompleteFallbackAndHeadIsolation(t *testing.T) {
	for _, head := range []string{checkHeadA, checkHeadB} {
		t.Run(head[:12], func(t *testing.T) {
			setupHeadCheckServer(t, func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprint(w, `{"values":[{"state":"SUCCESSFUL"}],"next":"https://unsafe.example.invalid/page"}`)
			})
			saved := checks424Seed(t, checkHeadB, "FAILURE")
			got, err := statusBBHeadChecksContext(context.Background(), StatusRefreshForeground, "example/widget", head, true)
			want := "UNKNOWN"
			if head == checkHeadB {
				want = "FAILURE"
			}
			if err == nil || got.Result != want || got.Complete {
				t.Fatalf("got=%+v err=%v", got, err)
			}
			if head == checkHeadB && got.ObservedAt != saved.ObservedAt {
				t.Fatal("retained failure age renewed")
			}
		})
	}
}

func TestStatusChecks424FallbackMetadataFailureKeepsChecks(t *testing.T) {
	foreground424Fixture(t, "printf 'synthetic unavailable' >&2; exit 1")
	hits := 0
	setupHeadCheckServer(t, func(w http.ResponseWriter, r *http.Request) { hits++; w.WriteHeader(503) })
	checks424Seed(t, checkHeadB, "FAILURE")
	raw := []byte(fmt.Sprintf(`{"id":7,"state":"OPEN","source":{"commit":{"hash":%q}}}`, checkHeadB))
	at := time.Now().Add(-time.Hour)
	statusWriteForgeEntrySinceObserved("pr", "bitbucket.org", "example/widget", "7", raw, at, at)
	got, out := statusRefreshRecord(context.Background(), StatusRefreshForeground, PRRecord{Number: "7"}, "example/widget", "bitbucket.org")
	if got.Checks != "FAILURE" || !got.ChecksUnsettled || out.Freshness == "live" || got.ObservedAt != at.UTC().Format(time.RFC3339Nano) || hits != 2 {
		t.Fatalf("got=%+v out=%+v hits=%d", got, out, hits)
	}
}

func TestStatusChecks424CancelBeforeLockAndHTTP(t *testing.T) {
	for _, where := range []string{"lock", "http", "queue"} {
		t.Run(where, func(t *testing.T) {
			entered := make(chan struct{}, 1)
			var hits atomic.Int32
			setupHeadCheckServer(t, func(w http.ResponseWriter, r *http.Request) { hits.Add(1); entered <- struct{}{}; <-r.Context().Done() })
			t.Setenv("WTC_FORGE_MIN_INTERVAL", "")
			checks424Seed(t, checkHeadB, "FAILURE")
			path := statusForgeEntryPath("checks", "bitbucket.org", "example/widget", checkHeadB)
			before, _ := os.ReadFile(path)
			var unlock func()
			if where == "lock" {
				var err error
				unlock, err = statusLockCache(path)
				if err != nil {
					t.Fatal(err)
				}
				defer unlock()
			}
			if where == "queue" {
				foreground424Budget(t, "bitbucket.org", 0, time.Now())
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				e, err := statusBBHeadChecksContext(ctx, StatusRefreshForeground, "example/widget", checkHeadB, true)
				if e.Result != "FAILURE" {
					done <- fmt.Errorf("lost known failure: %+v", e)
					return
				}
				done <- err
			}()
			if where == "http" {
				select {
				case <-entered:
				case <-time.After(2 * time.Second):
					t.Fatal("no HTTP request")
				}
			} else if where == "queue" {
				deadline := time.Now().Add(2 * time.Second)
				queued := false
				for time.Now().Before(deadline) {
					for _, p := range statusReadPendingConfirmations("bitbucket.org", time.Now()) {
						if p.Foreground {
							queued = true
						}
					}
					if queued {
						break
					}
					time.Sleep(5 * time.Millisecond)
				}
				if !queued {
					t.Fatal("no queued lease")
				}
			}
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("err=%v", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("cancel stalled")
			}
			after, _ := os.ReadFile(path)
			if string(after) != string(before) {
				t.Fatal("cancelled checks persisted")
			}
			if where != "http" && hits.Load() != 0 {
				t.Fatal("cancelled queued/locked request dispatched")
			}
			if where == "queue" {
				foreground424Budget(t, "bitbucket.org", 8, time.Now())
				for _, p := range statusReadPendingConfirmations("bitbucket.org", time.Now()) {
					if p.Foreground {
						t.Fatal("lease leaked")
					}
				}
			}
		})
	}
}

func TestStatusChecks424RedirectAndPrefixCost(t *testing.T) {
	hits := 0
	server := setupHeadCheckServer(t, func(w http.ResponseWriter, r *http.Request) {
		hits++
		switch {
		case strings.HasSuffix(r.URL.Path, "/commit/"+checkHeadB[:12]):
			fmt.Fprintf(w, `{"hash":%q}`, checkHeadB)
		case strings.Contains(r.URL.Path, "/statuses"):
			http.Redirect(w, r, "/redirected", http.StatusFound)
		default:
			fmt.Fprint(w, `{"values":[]}`)
		}
	})
	_ = server
	t.Setenv("WTC_FORGE_MIN_INTERVAL", "")
	foreground424Budget(t, "bitbucket.org", 8, time.Now())
	e, err := statusBBHeadChecksContext(context.Background(), StatusRefreshForeground, "example/widget", checkHeadB[:12], true)
	if err != nil || e.Head != checkHeadB || !e.Complete || hits != 4 {
		t.Fatalf("e=%+v err=%v hits=%d", e, err, hits)
	}
	raw, _ := os.ReadFile(statusForgeStatePath("budget", "bitbucket.org"))
	var tokens int
	fmt.Sscanf(string(raw), "%d", &tokens)
	if tokens != 4 {
		t.Fatalf("wrong per-hop cost:%s", raw)
	}
}

func TestStatusChecks424CooldownPreservesFailureAndNoLaterDispatch(t *testing.T) {
	hits := 0
	setupHeadCheckServer(t, func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(429)
	})
	t.Setenv("WTC_FORGE_MIN_INTERVAL", "")
	saved := checks424Seed(t, checkHeadB, "FAILURE")
	foreground424Budget(t, "bitbucket.org", 8, time.Now())
	got, err := statusBBHeadChecksContext(context.Background(), StatusRefreshForeground, "example/widget", checkHeadB, true)
	var admission *statusAdmissionError
	if !errors.As(err, &admission) || !errors.Is(err, errForgeCooling) || time.Until(admission.retry) < 55*time.Second || hits != 1 || got.Result != "FAILURE" || got.ObservedAt != saved.ObservedAt || got.Complete {
		t.Fatalf("got=%+v err=%v hits=%d", got, err, hits)
	}
	_, err = statusBBHeadChecksContext(context.Background(), StatusRefreshForeground, "example/widget", checkHeadB, true)
	if !errors.Is(err, errForgeCooling) || hits != 1 {
		t.Fatalf("cooldown bypassed err=%v hits=%d", err, hits)
	}
}

func TestStatusChecks424NewestWriterAndPartialFailure(t *testing.T) {
	for _, mode := range []string{"newer", "partial"} {
		t.Run(mode, func(t *testing.T) {
			setupHeadCheckServer(t, func(w http.ResponseWriter, r *http.Request) {
				if strings.Contains(r.URL.Path, "/statuses") {
					if mode == "newer" {
						e := headCheckEvidence{Head: checkHeadB, Result: "FAILURE", Complete: true, ObservedAt: time.Now().UTC().Format(time.RFC3339), Requiredness: "unknown"}
						raw, _ := json.Marshal(e)
						if !statusWriteForgeEntrySince("checks", "bitbucket.org", "example/widget", checkHeadB, raw, time.Now()) {
							t.Error("new writer did not commit")
						}
						fmt.Fprint(w, `{"values":[{"state":"SUCCESSFUL"}]}`)
					} else {
						fmt.Fprint(w, `{"values":[{"state":"FAILED"}],"next":"https://unsafe.example.invalid/page"}`)
					}
				} else {
					fmt.Fprint(w, `{"values":[]}`)
				}
			})
			got, err := statusBBHeadChecksContext(context.Background(), StatusRefreshForeground, "example/widget", checkHeadB, true)
			if got.Result != "FAILURE" {
				t.Fatalf("lost failure: %+v %v", got, err)
			}
			if mode == "partial" && (err == nil || got.Complete) {
				t.Fatalf("partial marked complete: %+v %v", got, err)
			}
			if mode == "newer" && (err != nil || !got.Complete) {
				t.Fatalf("newer writer lost: %+v %v", got, err)
			}
		})
	}
}

func TestStatusChecks424ParentDeadlineAndPageLimit(t *testing.T) {
	for _, mode := range []string{"deadline", "pages"} {
		t.Run(mode, func(t *testing.T) {
			var hits atomic.Int32
			server := setupHeadCheckServer(t, func(w http.ResponseWriter, r *http.Request) {
				n := hits.Add(1)
				if mode == "deadline" {
					<-r.Context().Done()
					return
				}
				fmt.Fprintf(w, `{"values":[],"next":%q}`, "http://"+r.Host+fmt.Sprintf("/page%d", n))
			})
			ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
			defer cancel()
			if mode == "pages" {
				ctx = context.Background()
			}
			started := time.Now()
			complete, err := fetchBBCheckPagesContext(ctx, StatusRefreshForeground, server.URL+"/start", "builder", "synthetic-token", func(json.RawMessage) error { return nil })
			if complete || err == nil {
				t.Fatalf("complete=%v err=%v", complete, err)
			}
			if mode == "deadline" && (!errors.Is(err, context.DeadlineExceeded) || time.Since(started) > 2*time.Second || hits.Load() != 1) {
				t.Fatalf("parent bound lost err=%v duration=%v hits=%d", err, time.Since(started), hits.Load())
			}
			if mode == "pages" && hits.Load() != 10 {
				t.Fatalf("page limit=%d", hits.Load())
			}
		})
	}
}

func TestStatusChecks424FailureBeforeLaterPageCancellation(t *testing.T) {
	entered := make(chan struct{}, 1)
	setupHeadCheckServer(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/statuses") {
			fmt.Fprint(w, `{"values":[{"state":"FAILED"}]}`)
			return
		}
		entered <- struct{}{}
		<-r.Context().Done()
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan headCheckEvidence, 1)
	errs := make(chan error, 1)
	go func() {
		e, err := statusBBHeadChecksContext(ctx, StatusRefreshForeground, "example/widget", checkHeadB, true)
		done <- e
		errs <- err
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("pipeline did not start")
	}
	cancel()
	select {
	case e := <-done:
		if e.Result != "FAILURE" || e.Complete {
			t.Fatalf("observed failure lost %+v", e)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancel stalled")
	}
	if err := <-errs; !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
	if _, ok := statusReadForgeEntryAny("checks", "bitbucket.org", "example/widget", checkHeadB); ok {
		t.Fatal("cancelled aggregate persisted")
	}
}

func TestStatusChecks424MissingCredentialsKeepsKnownFailure(t *testing.T) {
	setupHeadCheckServer(t, func(w http.ResponseWriter, r *http.Request) { t.Error("unexpected HTTP") })
	saved := checks424Seed(t, checkHeadB, "FAILURE")
	t.Setenv("BB_API_USER", "")
	t.Setenv("BB_API_PASS", "")
	e, err := statusBBHeadChecksContext(context.Background(), StatusRefreshForeground, "example/widget", checkHeadB, true)
	if err == nil || e.Result != "FAILURE" || e.Complete || e.ObservedAt != saved.ObservedAt {
		t.Fatalf("e=%+v err=%v", e, err)
	}
}
