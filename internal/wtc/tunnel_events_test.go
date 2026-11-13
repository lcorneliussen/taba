package wtc

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestTunnelEventsCanonicalPairAndCorruptionRefusal(t *testing.T) {
	key := testTunnelKey()
	other := key
	other.Name = "second"
	active := []tunnelActiveAddress{{other, "https://web.example.test:8443/b"}, {key, "https://web.example.test:443/a"}}
	snapshot, err := tunnelSnapshotForEvent(active)
	if err != nil {
		t.Fatal(err)
	}
	pair, _ := buildTunnelHostSnapshot(active)
	if snapshot.Revision != pair.Revision || len(snapshot.Hosts) != 1 || snapshot.Endpoints[0].Key != key {
		t.Fatal("event/publication pair differs")
	}
	before, _ := tunnelSnapshotForEvent(nil)
	current := snapshot.Endpoints[0].Address
	e := tunnelEvent{Schema: 1, OperationID: "synthetic-operation", Type: "started", Key: key, Definition: tunnelDigest([]byte("synthetic-definition")), Current: &current, Before: before, After: snapshot}
	e.ID = tunnelDigest([]byte(e.OperationID + "/" + key.String() + "/started"))
	if err = e.validate(); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(e)
	if strings.Contains(string(data), "/synthetic/") || strings.Contains(string(data), "token") {
		t.Fatal("event leaked execution inputs")
	}
	for _, change := range []func(*tunnelEvent){
		func(e *tunnelEvent) { e.ID = tunnelDigest([]byte("different")) },
		func(e *tunnelEvent) { e.OperationID = "other" },
		func(e *tunnelEvent) { e.Type = "post" },
		func(e *tunnelEvent) { e.After.Hosts = append(e.After.Hosts, "unrelated.example.test") },
		func(e *tunnelEvent) { e.After.Endpoints[0].Address.Hostname = "forged.example.test" },
		func(e *tunnelEvent) { e.Before.Revision = e.After.Revision },
	} {
		var bad tunnelEvent
		if err = json.Unmarshal(data, &bad); err != nil {
			t.Fatal(err)
		}
		change(&bad)
		if bad.validate() == nil {
			t.Fatal("corrupt event accepted")
		}
	}
	reverse, _ := tunnelSnapshotForEvent([]tunnelActiveAddress{active[1], active[0]})
	if !reflect.DeepEqual(snapshot, reverse) {
		t.Fatal("input order changed event snapshot")
	}
}
