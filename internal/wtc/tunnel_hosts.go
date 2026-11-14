package wtc

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

type tunnelAddress struct {
	URL      string `json:"url"`
	Origin   string `json:"origin"`
	Hostname string `json:"hostname"`
}

func parseTunnelAddress(raw string) (tunnelAddress, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Opaque != "" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(raw, "#") {
		return tunnelAddress{}, fmt.Errorf("tunnel URL must be HTTP(S) without credentials, query or fragment")
	}
	host := strings.ToLower(u.Hostname())
	if net.ParseIP(host) == nil && !tunnelHostname(host) {
		return tunnelAddress{}, fmt.Errorf("invalid tunnel URL hostname")
	}
	port := u.Port()
	if strings.HasSuffix(u.Host, ":") {
		return tunnelAddress{}, fmt.Errorf("invalid tunnel URL port")
	}
	if port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return tunnelAddress{}, fmt.Errorf("invalid tunnel URL port")
		}
		port = strconv.Itoa(n)
		if u.Scheme == "https" && port == "443" || u.Scheme == "http" && port == "80" {
			port = ""
		}
	}
	authority := host
	if strings.Contains(host, ":") {
		authority = "[" + host + "]"
	}
	if port != "" {
		authority = net.JoinHostPort(host, port)
	}
	u.Host = authority
	return tunnelAddress{u.String(), u.Scheme + "://" + authority, host}, nil
}

type tunnelActiveAddress struct {
	Key tunnelKey `json:"key"`
	URL string    `json:"url"`
}

type tunnelHostSnapshot struct {
	// Revision hashes both payloads. A publisher must commit this pair under
	// one generation before atomically switching its current-generation pointer.
	Revision string
	Hosts    []byte
	Origins  []byte
}

// Build from active observations only; registered-but-stopped URLs belong in
// separate known-registration data. Removing one entry never removes another
// entry's URL or its use of a shared hostname. This function performs no I/O.
func buildTunnelHostSnapshot(active []tunnelActiveAddress) (tunnelHostSnapshot, error) {
	type endpoint struct {
		Key tunnelKey `json:"key"`
		tunnelAddress
	}
	endpoints := make([]endpoint, 0, len(active))
	keys := map[tunnelKey]bool{}
	hosts := map[string]bool{}
	for _, item := range active {
		if err := item.Key.validate(); err != nil {
			return tunnelHostSnapshot{}, err
		}
		if keys[item.Key] {
			return tunnelHostSnapshot{}, fmt.Errorf("duplicate active tunnel entry")
		}
		address, err := parseTunnelAddress(item.URL)
		if err != nil {
			return tunnelHostSnapshot{}, err
		}
		keys[item.Key], hosts[address.Hostname] = true, true
		endpoints = append(endpoints, endpoint{item.Key, address})
	}
	sort.Slice(endpoints, func(i, j int) bool { return endpoints[i].Key.String() < endpoints[j].Key.String() })
	names := make([]string, 0, len(hosts))
	for host := range hosts {
		names = append(names, host)
	}
	sort.Strings(names)
	var lines string
	if len(names) > 0 {
		lines = strings.Join(names, "\n") + "\n"
	}
	origins, err := json.Marshal(struct {
		Schema    int        `json:"schema"`
		Endpoints []endpoint `json:"endpoints"`
	}{1, endpoints})
	if err != nil {
		return tunnelHostSnapshot{}, err
	}
	origins = append(origins, '\n')
	// Separate JSON framing avoids ambiguous concatenation of the two payloads.
	pair, _ := json.Marshal([2]string{lines, string(origins)})
	return tunnelHostSnapshot{tunnelDigest(pair), []byte(lines), origins}, nil
}
