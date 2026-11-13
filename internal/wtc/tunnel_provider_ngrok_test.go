package wtc

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.ngrok.com/ngrok"
	"golang.ngrok.com/ngrok/config"
)

func TestTunnelNgrokReservationUsesSeparateManagementTransport(t *testing.T) {
	calls := 0
	client := tunnelHTTP{Auth: "Bearer synthetic-management-capability", RoundTrip: tunnelProviderTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Host != "api.ngrok.com" || r.Header.Get("ngrok-version") != "2" || r.Header.Get("Authorization") != "Bearer synthetic-management-capability" {
			t.Fatal("wrong API capability/version")
		}
		if r.Method == "POST" {
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["domain"] != "preview.example.test" || body["metadata"] != "synthetic-operation" {
				t.Fatal("wrong reservation body")
			}
			return tunnelProviderResponse(201, `{"id":"rd_synthetic","domain":"preview.example.test"}`), nil
		}
		if r.Method != "DELETE" || r.URL.Path != "/reserved_domains/rd_synthetic" {
			t.Fatal("cleanup widened its target")
		}
		return tunnelProviderResponse(204, ""), nil
	})}
	r, err := reserveNgrokDomain(context.Background(), client, "preview.example.test", "synthetic-operation")
	if err != nil || r.ID != "rd_synthetic" {
		t.Fatal("reservation failed")
	}
	if err := deleteNgrokDomain(context.Background(), client, r.ID); err != nil || calls != 2 {
		t.Fatal("exact cleanup failed")
	}
	if err := deleteNgrokDomain(context.Background(), client, "../foreign"); err == nil || calls != 2 {
		t.Fatal("unsafe cleanup target reached transport")
	}
}

type ngrokFakeForwarder struct {
	ngrok.Forwarder
	address string
	closes  int
	fail    bool
}

func (f *ngrokFakeForwarder) URL() string { return f.address }
func (f *ngrokFakeForwarder) CloseWithContext(context.Context) error {
	f.closes++
	if f.fail {
		return errors.New("synthetic-sensitive-value")
	}
	return nil
}

type ngrokFakeSession struct {
	forwarder *ngrokFakeForwarder
	closes    int
	failClose bool
	failStart bool
}

func (s *ngrokFakeSession) Close() error {
	s.closes++
	if s.failClose {
		return errors.New("synthetic-sensitive-value")
	}
	return nil
}
func (s *ngrokFakeSession) ListenAndForward(_ context.Context, target *url.URL, cfg config.Tunnel) (ngrok.Forwarder, error) {
	if target.Host != "127.0.0.1:3000" {
		return nil, errors.New("unexpected target")
	}
	options, _ := json.Marshal(cfg.(interface{ Opts() any }).Opts())
	var fields map[string]any
	_ = json.Unmarshal(options, &fields)
	if fields["Domain"] != "preview.example.test" {
		return nil, errors.New("requested domain not passed to SDK")
	}
	if s.failStart {
		return nil, errors.New("synthetic-sensitive-value")
	}
	return s.forwarder, nil
}

func TestTunnelNgrokHostOwnsOnlyItsSessionAndRetainsFailedCleanup(t *testing.T) {
	f := &ngrokFakeForwarder{address: "https://preview.example.test"}
	s := &ngrokFakeSession{forwarder: f}
	host, err := startNgrokHost(context.Background(), s, "preview.example.test", "http://127.0.0.1:3000")
	if err != nil || host.URL != f.address {
		t.Fatalf("startup failed: %v", err)
	}
	f.fail = true
	if err := host.stop(context.Background()); err == nil || strings.Contains(err.Error(), "synthetic-sensitive-value") || s.closes != 0 || host.forwarder == nil {
		t.Fatal("failed endpoint cleanup lost handle or closed session")
	}
	f.fail = false
	s.failClose = true
	if err := host.stop(context.Background()); err == nil || host.session == nil {
		t.Fatal("failed session cleanup lost handle")
	}
	s.failClose = false
	if err := host.stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	closes := s.closes
	if err := host.stop(context.Background()); err != nil || s.closes != closes {
		t.Fatal("duplicate stop repeated successful cleanup")
	}
	other := &ngrokFakeSession{forwarder: &ngrokFakeForwarder{address: "https://other.example.test"}}
	partial, err := startNgrokHost(context.Background(), other, "preview.example.test", "http://127.0.0.1:3000")
	if err == nil || partial == nil {
		t.Fatal("address mismatch lost cleanup handle")
	}
	if err := partial.stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	failed := &ngrokFakeSession{failStart: true, failClose: true}
	partial, err = startNgrokHost(context.Background(), failed, "preview.example.test", "http://127.0.0.1:3000")
	if err == nil || partial == nil || partial.session == nil || strings.Contains(err.Error(), "synthetic-sensitive-value") {
		t.Fatal("startup failure forgot session cleanup obligation")
	}
}

type ngrokDeniedDialer struct{ calls atomic.Int32 }

func (d *ngrokDeniedDialer) Dial(string, string) (net.Conn, error) {
	d.calls.Add(1)
	return nil, errors.New("synthetic dial denied")
}
func (d *ngrokDeniedDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return d.Dial(network, address)
}

func TestTunnelNgrokPinnedSDKCannotFallThroughInjectedDialer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	dialer := &ngrokDeniedDialer{}
	if _, err := connectNgrokHost(ctx, "synthetic-host-capability", dialer); err == nil || dialer.calls.Load() == 0 {
		t.Fatal("expected explicit fake SDK transport refusal")
	}
	if _, err := connectNgrokHost(context.Background(), "synthetic-host-capability", nil); err == nil {
		t.Fatal("implicit dialer permitted")
	}
}

type ngrokPendingSession struct {
	ngrokHostSession
	entered chan struct{}
	finish  chan struct{}
	calls   atomic.Int32
}

func (s *ngrokPendingSession) Close() error {
	s.calls.Add(1)
	close(s.entered)
	<-s.finish
	return nil
}

func TestTunnelNgrokPendingCloseRetainsOneAttempt(t *testing.T) {
	s := &ngrokPendingSession{entered: make(chan struct{}), finish: make(chan struct{})}
	host := &ngrokHost{session: s}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := host.stop(ctx); err == nil || host.session == nil || host.closing == nil {
		t.Fatal("pending shutdown lost its session/attempt")
	}
	<-s.entered
	if err := host.stop(ctx); err == nil || s.calls.Load() != 1 {
		t.Fatal("retry duplicated an in-flight close")
	}
	close(s.finish)
	if err := host.stop(context.Background()); err != nil || host.session != nil || s.calls.Load() != 1 {
		t.Fatal("completed close was not reconciled exactly to its attempt")
	}
}
