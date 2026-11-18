package wtc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func resourceChecksServer(t *testing.T, handler http.HandlerFunc) {
	t.Helper()
	resourceCancellationFixture(t)
	t.Setenv("BB_API_USER", "fixture")
	t.Setenv("BB_API_PASS", "synthetic")
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	old := statusBBAPI
	statusBBAPI = server.URL
	t.Cleanup(func() { statusBBAPI = old })
}

func TestStatusResourceBackgroundChecksCancelHTTP(t *testing.T) {
	for _, abbreviated := range []bool{false, true} {
		t.Run(fmt.Sprint(abbreviated), func(t *testing.T) {
			entered, release := make(chan struct{}, 1), make(chan struct{})
			var hits atomic.Int32
			resourceChecksServer(t, func(w http.ResponseWriter, r *http.Request) {
				n := hits.Add(1)
				if n == 1 {
					entered <- struct{}{}
					select {
					case <-release:
					case <-r.Context().Done():
						return
					}
				}
				if strings.HasSuffix(r.URL.Path, "/commit/"+checkHeadB[:12]) {
					fmt.Fprintf(w, `{"hash":%q}`, checkHeadB)
				} else {
					fmt.Fprint(w, `{"values":[]}`)
				}
			})
			head := checkHeadB
			if abbreviated {
				head = head[:12]
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				_, err := statusBBHeadChecksContext(ctx, StatusRefreshBackground, "example/widget", head, false)
				done <- err
			}()
			select {
			case <-entered:
			case <-time.After(time.Second):
				close(release)
				<-done
				t.Fatal("HTTP not entered")
			}
			cancel()
			blocked := false
			var err error
			select {
			case err = <-done:
			case <-time.After(200 * time.Millisecond):
				blocked = true
			}
			close(release)
			if blocked {
				err = <-done
			}
			if blocked || !errors.Is(err, context.Canceled) || hits.Load() != 1 {
				t.Errorf("cancel outcome blocked=%v err=%v hits=%d", blocked, err, hits.Load())
			}
			for _, entry := range [][2]string{{"checks", checkHeadB}, {"checkhead", head}} {
				if _, ok := statusReadForgeEntryAny(entry[0], "bitbucket.org", "example/widget", entry[1]); ok {
					t.Errorf("postcancel publication: %v", entry)
				}
			}
		})
	}
}

// Done is observed only after the contended context lock enters its wait.
type resourceChecksQueuedContext struct {
	context.Context
	waiting chan struct{}
	once    sync.Once
}

func (c *resourceChecksQueuedContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.waiting) })
	return c.Context.Done()
}

func TestStatusResourceBackgroundChecksCancelQueuedLock(t *testing.T) {
	for _, abbreviated := range []bool{false, true} {
		for _, budget := range []bool{false, true} {
			t.Run(fmt.Sprintf("%t/budget-%t", abbreviated, budget), func(t *testing.T) {
				var hits atomic.Int32
				resourceChecksServer(t, func(w http.ResponseWriter, r *http.Request) {
					hits.Add(1)
					if strings.HasSuffix(r.URL.Path, "/commit/"+checkHeadB[:12]) {
						fmt.Fprintf(w, `{"hash":%q}`, checkHeadB)
					} else {
						fmt.Fprint(w, `{"values":[]}`)
					}
				})
				head, kind := checkHeadB, "checks"
				if abbreviated {
					head, kind = head[:12], "checkhead"
				}
				path := statusForgeEntryPath(kind, "bitbucket.org", "example/widget", head)
				if budget {
					path = statusForgeStatePath("budget", "bitbucket.org")
				}
				unlock, err := statusLockCache(path)
				if err != nil {
					t.Fatal(err)
				}
				parent, cancel := context.WithCancel(context.Background())
				defer cancel()
				ctx := &resourceChecksQueuedContext{Context: parent, waiting: make(chan struct{})}
				done := make(chan error, 1)
				go func() {
					_, err := statusBBHeadChecksContext(ctx, StatusRefreshBackground, "example/widget", head, false)
					done <- err
				}()
				queued := false
				select {
				case <-ctx.waiting:
					queued = true
				case <-time.After(200 * time.Millisecond):
				}
				cancel()
				blocked := false
				select {
				case err = <-done:
				case <-time.After(200 * time.Millisecond):
					blocked = true
				}
				unlock()
				if blocked {
					err = <-done
				}
				if !queued || blocked || !errors.Is(err, context.Canceled) || hits.Load() != 0 {
					t.Fatalf("queued cancel blocked=%v err=%v hits=%d", blocked, err, hits.Load())
				}
				if _, ok := statusReadForgeEntryAny(kind, "bitbucket.org", "example/widget", head); ok {
					t.Fatal("queued cancellation persisted")
				}
			})
		}
	}
}

func TestStatusResourceBackgroundChecksPageCancelKeepsFailure(t *testing.T) {
	for _, source := range []string{"received", "cached"} {
		for _, fresh := range []bool{false, true} {
			t.Run(fmt.Sprint(source, fresh), func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				var hits atomic.Int32
				resourceChecksServer(t, func(w http.ResponseWriter, r *http.Request) {
					n := hits.Add(1)
					if n == 1 {
						state := "FAILED"
						if source == "cached" {
							state = "SUCCESSFUL"
						}
						fmt.Fprintf(w, `{"values":[{"state":%q}],"next":%q}`, state, statusBBAPI+"/second")
						return
					}
					cancel()
					fmt.Fprint(w, `{"values":[]}`)
				})
				path := statusForgeEntryPath("checks", "bitbucket.org", "example/widget", checkHeadB)
				var before []byte
				if source == "cached" {
					checks424Seed(t, checkHeadB, "FAILURE")
					past := time.Now().Add(-time.Hour)
					if err := os.Chtimes(path, past, past); err != nil {
						t.Fatal(err)
					}
					before, _ = os.ReadFile(path)
				}
				got, err := statusBBHeadChecksContext(ctx, StatusRefreshBackground, "example/widget", checkHeadB, fresh)
				if !errors.Is(err, context.Canceled) || got.Head != checkHeadB || got.Result != "FAILURE" || got.Complete || hits.Load() != 2 {
					t.Fatalf("failure lost: %+v err=%v hits=%d", got, err, hits.Load())
				}
				after, _ := os.ReadFile(path)
				if string(before) != string(after) {
					t.Fatal("canceled partial pages published")
				}
			})
		}
	}
}

func TestStatusResourceBackgroundChecksExactHeadAndCost(t *testing.T) {
	for _, abbreviated := range []bool{false, true} {
		t.Run(fmt.Sprint(abbreviated), func(t *testing.T) {
			var hits atomic.Int32
			resourceChecksServer(t, func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				if strings.HasSuffix(r.URL.Path, "/commit/"+checkHeadB[:12]) {
					fmt.Fprintf(w, `{"hash":%q}`, checkHeadB)
				} else if strings.Contains(r.URL.Path, "/statuses") {
					fmt.Fprint(w, `{"values":[{"state":"SUCCESSFUL"}]}`)
				} else {
					fmt.Fprintf(w, `{"values":[{"state":{"name":"COMPLETED","result":{"name":"FAILED"}},"target":{"commit":{"hash":%q},"selector":{"type":"branches","pattern":"main"}}}]}`, checkHeadA)
				}
			})
			head := checkHeadB
			want := int32(2)
			if abbreviated {
				head = head[:12]
				want++
			}
			got, err := statusBBHeadChecksContext(context.Background(), StatusRefreshBackground, "example/widget", head, false)
			if err != nil || got.Head != checkHeadB || got.Result != "SUCCESS" || !got.Complete || hits.Load() != want {
				t.Fatalf("head/cost: %+v err=%v hits=%d", got, err, hits.Load())
			}
			data, err := os.ReadFile(statusForgeStatePath("budget", "bitbucket.org"))
			if err != nil {
				t.Fatal(err)
			}
			var tokens int
			fmt.Sscanf(string(data), "%d", &tokens)
			if tokens != statusBitbucketBurst-int(want) {
				t.Fatalf("aggregate/extra charge: %s", data)
			}
			raw, _ := statusReadForgeEntryAny("checks", "bitbucket.org", "example/widget", checkHeadB)
			var saved headCheckEvidence
			if json.Unmarshal(raw, &saved) != nil || saved.Head != checkHeadB {
				t.Fatal("wrong cache binding")
			}
		})
	}
}

func TestStatusResourceBackgroundChecksMissingAuthKeepsFailure(t *testing.T) {
	resourceCancellationFixture(t)
	for _, name := range []string{"BB_API_USER", "BB_API_PASS", "BB_USERNAME", "BB_API_TOKEN", "ATLASSIAN_ACCOUNT_EMAIL", "ATLASSIAN_API_TOKEN"} {
		t.Setenv(name, "")
	}
	before := checks424Seed(t, checkHeadB, "FAILURE")
	got, err := statusBBHeadChecksContext(context.Background(), StatusRefreshBackground, "example/widget", checkHeadB, false)
	if err == nil || got.Result != "FAILURE" || got.Complete || got.ObservedAt != before.ObservedAt {
		t.Fatalf("auth failure erased known check: %+v %v", got, err)
	}
	got, _ = statusBBHeadChecksContext(context.Background(), StatusRefreshBackground, "example/widget", checkHeadA, false)
	if got.Result == "FAILURE" {
		t.Fatal("auth fallback crossed heads")
	}
}

func TestStatusResourceBackgroundChecksCommitSurvivesCancellation(t *testing.T) {
	resourceChecksServer(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/statuses") {
			fmt.Fprint(w, `{"values":[{"state":"FAILED"}]}`)
		} else {
			fmt.Fprint(w, `{"values":[]}`)
		}
	})
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	path := statusForgeEntryPath("checks", "bitbucket.org", "example/widget", checkHeadB)
	ctx := status424CancelAfterFile{Context: parent, path: path, cancel: cancel}
	got, err := statusBBHeadChecksContext(ctx, StatusRefreshBackground, "example/widget", checkHeadB, false)
	if !errors.Is(err, context.Canceled) || got.Result != "FAILURE" || got.Complete {
		t.Fatalf("committed request outcome: %+v %v", got, err)
	}
	raw, ok := statusReadForgeEntryAny("checks", "bitbucket.org", "example/widget", checkHeadB)
	var saved headCheckEvidence
	if !ok || json.Unmarshal(raw, &saved) != nil || saved.Result != "FAILURE" || !saved.Complete {
		t.Fatal("valid committed evidence rolled back")
	}
}

func TestStatusResourceBackgroundChecksNewerPartialKeepsKnownFailure(t *testing.T) {
	for _, complete := range []bool{false, true} {
		t.Run(fmt.Sprint(complete), func(t *testing.T) {
			var hits atomic.Int32
			resourceChecksServer(t, func(w http.ResponseWriter, r *http.Request) {
				if hits.Add(1) == 2 {
					newer := unknownHeadChecks(checkHeadB)
					raw, _ := json.Marshal(newer)
					// A compatibility writer publishes a genuinely later incomplete read.
					// Its bytes/order remain authoritative; it cannot disprove a known failure.
					statusWriteForgeEntrySince("checks", "bitbucket.org", "example/widget", checkHeadB, raw, time.Now())
					if !complete {
						w.WriteHeader(http.StatusServiceUnavailable)
					}
					fmt.Fprint(w, `{"values":[]}`)
					return
				}
				fmt.Fprint(w, `{"values":[{"state":"SUCCESSFUL"}]}`)
			})
			before := checks424Seed(t, checkHeadB, "FAILURE")
			path := statusForgeEntryPath("checks", "bitbucket.org", "example/widget", checkHeadB)
			past := time.Now().Add(-time.Hour)
			if err := os.Chtimes(path, past, past); err != nil {
				t.Fatal(err)
			}
			got, err := statusBBHeadChecksContext(context.Background(), StatusRefreshBackground, "example/widget", checkHeadB, false)
			if err == nil || got.Result != "FAILURE" || got.Complete || got.ObservedAt != before.ObservedAt {
				t.Fatalf("incomplete winner erased failure: %+v %v", got, err)
			}
			raw, _ := os.ReadFile(path)
			var winner headCheckEvidence
			if json.Unmarshal(raw, &winner) != nil || winner.Result != "UNKNOWN" {
				t.Fatal("older completion overwrote actual newer bytes")
			}
		})
	}
}

func TestStatusResourceForegroundNewerPartialKeepsKnownFailure(t *testing.T) {
	for _, complete := range []bool{false, true} {
		t.Run(fmt.Sprint(complete), func(t *testing.T) {
			var hits atomic.Int32
			resourceChecksServer(t, func(w http.ResponseWriter, r *http.Request) {
				if hits.Add(1) == 2 {
					newer := unknownHeadChecks(checkHeadB)
					raw, _ := json.Marshal(newer)
					statusWriteForgeEntrySince("checks", "bitbucket.org", "example/widget", checkHeadB, raw, time.Now())
					if !complete {
						w.WriteHeader(http.StatusServiceUnavailable)
					}
					fmt.Fprint(w, `{"values":[]}`)
					return
				}
				fmt.Fprint(w, `{"values":[{"state":"SUCCESSFUL"}]}`)
			})
			before := checks424Seed(t, checkHeadB, "FAILURE")
			path := statusForgeEntryPath("checks", "bitbucket.org", "example/widget", checkHeadB)
			past := time.Now().Add(-time.Hour)
			if err := os.Chtimes(path, past, past); err != nil {
				t.Fatal(err)
			}
			got, err := statusBBHeadChecksContext(context.Background(), StatusRefreshForeground, "example/widget", checkHeadB, false)
			if err == nil || got.Result != "FAILURE" || got.Complete || got.ObservedAt != before.ObservedAt {
				t.Fatalf("incomplete winner erased failure: %+v %v", got, err)
			}
			raw, _ := os.ReadFile(path)
			var winner headCheckEvidence
			if json.Unmarshal(raw, &winner) != nil || winner.Result != "UNKNOWN" {
				t.Fatal("older completion overwrote actual newer bytes")
			}
		})
	}
}
