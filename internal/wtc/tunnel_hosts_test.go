package wtc

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestTunnelHostSnapshotRetainsOtherEntriesAndFullOrigins(t *testing.T) {
	a := tunnelActiveAddress{tunnelKey{"widget", "web", "first"}, "https://WEB.example.test:443/callback/first"}
	b := tunnelActiveAddress{tunnelKey{"widget", "web", "second"}, "https://web.example.test:8443/callback/second"}
	c := tunnelActiveAddress{tunnelKey{"widget", "web", "third"}, "http://other.example.test:8080/hook"}
	all, err := buildTunnelHostSnapshot([]tunnelActiveAddress{a, b, c})
	if err != nil || string(all.Hosts) != "other.example.test\nweb.example.test\n" {
		t.Fatalf("deduplicated sorted hosts: %q %v", all.Hosts, err)
	}
	reordered, _ := buildTunnelHostSnapshot([]tunnelActiveAddress{c, b, a})
	if !reflect.DeepEqual(all, reordered) {
		t.Fatal("input ordering changed snapshot/reload revision")
	}
	stopped, err := buildTunnelHostSnapshot([]tunnelActiveAddress{b, c})
	if err != nil || !bytes.Equal(stopped.Hosts, all.Hosts) || stopped.Revision == all.Revision || bytes.Contains(stopped.Origins, []byte("/callback/first")) || !bytes.Contains(stopped.Origins, []byte("https://web.example.test:8443/callback/second")) {
		t.Fatalf("stopping first entry overwrote another entry: %+v %v", stopped, err)
	}
	var origins struct {
		Schema    int `json:"schema"`
		Endpoints []struct {
			Key tunnelKey `json:"key"`
			tunnelAddress
		} `json:"endpoints"`
	}
	if err := json.Unmarshal(all.Origins, &origins); err != nil || origins.Schema != 1 || len(origins.Endpoints) != 3 {
		t.Fatalf("origins: %+v %v", origins, err)
	}
	first := origins.Endpoints[0]
	if first.Origin != "https://web.example.test" || first.URL != "https://web.example.test/callback/first" || first.Hostname != "web.example.test" {
		t.Fatalf("URL paths, ports and hostname must remain distinct: %+v", first)
	}
	empty, err := buildTunnelHostSnapshot(nil)
	if err != nil || len(empty.Hosts) != 0 || !strings.Contains(string(empty.Origins), `"endpoints":[]`) {
		t.Fatalf("empty active set: %+v %v", empty, err)
	}
}

func TestTunnelAddressRejectsSensitiveOrMalformedURLs(t *testing.T) {
	for _, raw := range []string{"https://user:synthetic-secret@example.test/", "https://example.test/?token=synthetic-secret", "https://example.test/#fragment", "https://example.test/?", "https://example.test/#", "file:///tmp/host", "https://example.test:0", "https://example.test:65536", "https://example.test:", "https://bad_host.example.test", "https://example.test\nother.example.test", "https://*.example.test"} {
		if _, err := parseTunnelAddress(raw); err == nil || strings.Contains(err.Error(), "synthetic-secret") {
			t.Fatalf("must reject without echoing secret: %q %v", raw, err)
		}
	}
	for raw, origin := range map[string]string{"http://[::1]:3000/path": "http://[::1]:3000", "https://EXAMPLE.test:443/path": "https://example.test", "http://127.0.0.1:80/path": "http://127.0.0.1"} {
		a, err := parseTunnelAddress(raw)
		if err != nil || a.Origin != origin {
			t.Fatalf("canonical address %s: %+v %v", raw, a, err)
		}
	}
	item := tunnelActiveAddress{testTunnelKey(), "https://web.example.test"}
	if _, err := buildTunnelHostSnapshot([]tunnelActiveAddress{item, item}); err == nil {
		t.Fatal("duplicate entry identity accepted")
	}
}
