package wtc

import (
	"context"
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
	_, _, err := statusWithForgeBudgetKeyContext(ctx, "bitbucket.org", "", nil, func() ([]byte, error) {
		return statusBBPipelinesContext(ctx, "example", "widget", "main", "fixture", "synthetic")
	})
	t.Logf("actualHTTP=%d err=%v", calls.Load(), err)
	if calls.Load() != 1 {
		t.Fatalf("one-token read dispatched %d HTTP hops", calls.Load())
	}
}
