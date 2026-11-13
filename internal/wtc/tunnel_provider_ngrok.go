package wtc

import (
	"context"
	"errors"
	"net/url"
	"strings"

	"golang.ngrok.com/ngrok"
	"golang.ngrok.com/ngrok/config"
)

type ngrokDomainRegistration struct {
	ID     string `json:"id"`
	Domain string `json:"domain"`
}

// An existing domain is borrowed without calling this method. Only explicitly
// requested new reservations produce an owned registration receipt.
func reserveNgrokDomain(ctx context.Context, client tunnelHTTP, domain, operation string) (ngrokDomainRegistration, error) {
	if !tunnelHostname(domain) || !tunnelComponent.MatchString(operation) {
		return ngrokDomainRegistration{}, errors.New("invalid ngrok domain reservation")
	}
	var result ngrokDomainRegistration
	_, err := client.request(ctx, "POST", "https://api.ngrok.com/reserved_domains", map[string]string{"ngrok-version": "2"}, map[string]string{"domain": domain, "metadata": operation}, &result)
	if err != nil {
		return result, err
	}
	if !tunnelComponent.MatchString(result.ID) || result.Domain != domain {
		return ngrokDomainRegistration{}, errors.New("ngrok reservation identity mismatch; reconcile pending operation")
	}
	return result, nil
}

func deleteNgrokDomain(ctx context.Context, client tunnelHTTP, id string) error {
	if !tunnelComponent.MatchString(id) {
		return errors.New("invalid exact ngrok reservation target")
	}
	code, err := client.request(ctx, "DELETE", "https://api.ngrok.com/reserved_domains/"+id, map[string]string{"ngrok-version": "2"}, nil, nil)
	if code == 404 {
		return nil
	}
	return err
}

// One session per named entry avoids closing another entry's endpoints. The
// provided dialer and authtoken are explicit; no CLI/profile/env login is used.
func connectNgrokHost(ctx context.Context, token string, dialer ngrok.Dialer) (ngrok.Session, error) {
	if token == "" || strings.ContainsAny(token, "\r\n\x00") || dialer == nil {
		return nil, errors.New("explicit scoped ngrok hosting credential and dialer required")
	}
	session, err := ngrok.Connect(ctx, ngrok.WithAuthtoken(token), ngrok.WithDialer(dialer))
	if err != nil {
		return nil, errors.New("ngrok host connection failed; reconcile pending operation")
	}
	return session, nil
}

type ngrokHostSession interface {
	ListenAndForward(context.Context, *url.URL, config.Tunnel) (ngrok.Forwarder, error)
	Close() error
}

type ngrokHost struct {
	URL       string
	forwarder ngrok.Forwarder
	session   ngrokHostSession
	closing   <-chan error
}

func startNgrokHost(ctx context.Context, session ngrokHostSession, domain, target string) (*ngrokHost, error) {
	if session == nil {
		return nil, errors.New("explicit ngrok session required")
	}
	host := &ngrokHost{session: session}
	if !tunnelHostname(domain) {
		return host, errors.New("invalid ngrok host domain; retain session for cleanup")
	}
	address, err := parseTunnelAddress(target)
	if err != nil || address.Hostname != "127.0.0.1" && address.Hostname != "::1" && address.Hostname != "localhost" {
		return host, errors.New("ngrok host target must be an explicit loopback HTTP(S) URL; retain session for cleanup")
	}
	u, _ := url.Parse(address.URL)
	forwarder, err := session.ListenAndForward(ctx, u, config.HTTPEndpoint(config.WithDomain(domain)))
	if err != nil {
		// A failed startup may still leave a session. Use the same bounded
		// cleanup path as stop, retaining any pending attempt for the owner.
		if closeErr := host.stop(ctx); closeErr != nil {
			return host, errors.New("ngrok startup and session cleanup unconfirmed; retain ownership")
		}
		return nil, errors.New("ngrok endpoint startup failed")
	}
	host.forwarder = forwarder
	if forwarder == nil {
		return host, errors.New("ngrok endpoint missing; retain session for cleanup")
	}
	remote, err := parseTunnelAddress(forwarder.URL())
	if err != nil || remote.Hostname != domain || remote.Origin != "https://"+domain {
		return host, errors.New("ngrok endpoint address mismatch; retain handle for cleanup")
	}
	host.URL = remote.URL
	return host, nil
}

func (h *ngrokHost) stop(ctx context.Context) error {
	if h.forwarder != nil {
		if err := h.forwarder.CloseWithContext(ctx); err != nil {
			return errors.New("ngrok endpoint shutdown unconfirmed; retain ownership")
		}
		h.forwarder = nil
	}
	if h.session != nil {
		// Session.Close has no context parameter. Keep one in-flight attempt
		// so a timeout/retry neither blocks the owner nor blindly repeats it.
		if h.closing == nil {
			result := make(chan error, 1)
			session := h.session
			h.closing = result
			go func() { result <- session.Close() }()
		}
		select {
		case err := <-h.closing:
			h.closing = nil
			if err != nil {
				return errors.New("ngrok session shutdown unconfirmed; retain ownership")
			}
		case <-ctx.Done():
			return errors.New("ngrok session shutdown pending; retain ownership")
		}
		h.session = nil
	}
	return nil
}
