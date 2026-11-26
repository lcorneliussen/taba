package wtc

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestStatusResourcePipelineRedirectPaysPerHop(t *testing.T) {
	resourceCancellationFixture(t)
	t.Setenv("WTC_FORGE_MIN_INTERVAL", "3600")
	t.Setenv("TABA_FORGE_MIN_INTERVAL", "3600")
	var calls atomic.Int32
	var origin string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/target" {
			w.Header().Set("Location", origin+"/target")
			w.WriteHeader(http.StatusFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"values":[]}`))
	}))
	defer server.Close()
	origin = server.URL
	old := statusBBAPI
	statusBBAPI = server.URL
	defer func() { statusBBAPI = old }()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, _, _, err := statusWithForgeAdmissionContext(ctx, "bitbucket.org", nil, func() ([]byte, error) {
		return statusBBPipelinesContext(ctx, "example", "widget", "main", "fixture", "synthetic")
	})
	t.Logf("actualHTTP=%d err=%v", calls.Load(), err)
	if calls.Load() != 1 {
		t.Fatalf("one-token read dispatched %d HTTP hops", calls.Load())
	}
}

func TestStatusResourcePipelineRedirectContracts(t *testing.T) {
	for _, mode := range []string{"allowed", "cross-origin", "userinfo", "loop", "404", "429", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			resourceCancellationFixture(t)
			if mode == "loop" {
				t.Setenv("WTC_FORGE_MIN_INTERVAL", "0")
				t.Setenv("TABA_FORGE_MIN_INTERVAL", "0")
			} else {
				redirectBudget(t, 3)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var calls atomic.Int32
			var leaked atomic.Int32
			other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked.Add(1) }))
			defer other.Close()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := calls.Add(1)
				u, p, ok := r.BasicAuth()
				if !ok || u != "fixture" || p != "synthetic" {
					t.Error("auth lost")
				}
				if mode == "cancel" {
					cancel()
					<-r.Context().Done()
					return
				}
				if n == 1 || mode == "loop" {
					target := "/target"
					if mode == "cross-origin" {
						target = other.URL
					}
					if mode == "userinfo" {
						target = "http://user:synthetic@invalid.example/"
					}
					w.Header().Set("Location", target)
					w.WriteHeader(302)
					return
				}
				if mode == "404" {
					w.WriteHeader(404)
					return
				}
				if mode == "429" {
					w.WriteHeader(429)
					return
				}
				w.Write([]byte(`{"values":[]}`))
			}))
			defer server.Close()
			old := statusBBAPI
			statusBBAPI = server.URL
			defer func() { statusBBAPI = old }()
			raw, live, _, err := statusWithForgeAdmissionContext(ctx, "bitbucket.org", nil, func() ([]byte, error) {
				return statusBBPipelinesContext(ctx, "example", "widget", "main", "fixture", "synthetic")
			})
			switch mode {
			case "allowed", "404":
				if err != nil || !live || string(raw) != `{"values":[]}` || calls.Load() != 2 {
					t.Fatalf("result %s %v %v hits%d", raw, live, err, calls.Load())
				}
				if statusTakeForgeToken(context.Background(), "bitbucket.org") != nil || statusTakeForgeToken(context.Background(), "bitbucket.org") == nil {
					t.Fatal("per-hop debit incorrect")
				}
			case "cross-origin", "userinfo":
				if err == nil || calls.Load() != 1 || leaked.Load() != 0 {
					t.Fatal(err, calls.Load(), leaked.Load())
				}
			case "loop":
				if err == nil || calls.Load() != 10 {
					t.Fatal(err, calls.Load())
				}
			case "429":
				if live || !statusForgeCooling("bitbucket.org") || calls.Load() != 2 {
					t.Fatal(live, err, calls.Load())
				}
			case "cancel":
				if !errors.Is(err, context.Canceled) || calls.Load() != 1 {
					t.Fatal(err, calls.Load())
				}
			}
		})
	}
}
