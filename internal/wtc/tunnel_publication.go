package wtc

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// The pointer is the only mutable publication. A consumer reads it once, then
// reads both immutable files from that generation. Reloads happen after this
// switch; two unrelated flat-file renames cannot provide this guarantee.
type tunnelPublication struct {
	Schema      int    `json:"schema"`
	Owner       string `json:"owner"`
	Revision    string `json:"revision"`
	HostsFile   string `json:"hosts_file"`
	OriginsFile string `json:"origins_file"`
}

func (c *Context) tunnelPublicationRoot() string {
	return filepath.Join(c.runtimeRoot(), "tunnel-publications")
}

func readTunnelBoundedFile(path string, limit int64) ([]byte, error) {
	if err := runtimeRegular(path); err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, fmt.Errorf("invalid bounded tunnel file")
	}
	return data, nil
}

func validateTunnelHostSnapshot(snapshot tunnelHostSnapshot) error {
	if !validTunnelDigest(snapshot.Revision) || len(snapshot.Hosts) > tunnelStateLimit || len(snapshot.Origins) > tunnelStateLimit || !json.Valid(snapshot.Origins) || !tunnelUniqueJSONKeys(json.NewDecoder(bytes.NewReader(snapshot.Origins))) {
		return fmt.Errorf("invalid tunnel publication")
	}
	var wire struct {
		Schema    int `json:"schema"`
		Endpoints []struct {
			Key tunnelKey `json:"key"`
			tunnelAddress
		} `json:"endpoints"`
	}
	d := json.NewDecoder(bytes.NewReader(snapshot.Origins))
	d.DisallowUnknownFields()
	if d.Decode(&wire) != nil || wire.Schema != 1 {
		return fmt.Errorf("invalid tunnel publication origins")
	}
	active := []tunnelActiveAddress{}
	for _, endpoint := range wire.Endpoints {
		address, err := parseTunnelAddress(endpoint.URL)
		if err != nil || address != endpoint.tunnelAddress {
			return fmt.Errorf("invalid tunnel publication endpoint")
		}
		active = append(active, tunnelActiveAddress{endpoint.Key, endpoint.URL})
	}
	expected, err := buildTunnelHostSnapshot(active)
	if err != nil || expected.Revision != snapshot.Revision || !bytes.Equal(expected.Hosts, snapshot.Hosts) || !bytes.Equal(expected.Origins, snapshot.Origins) {
		return fmt.Errorf("tunnel publication pair differs from revision")
	}
	return nil
}

func (c *Context) readTunnelPublication() (tunnelPublication, tunnelHostSnapshot, error) {
	var p tunnelPublication
	var snapshot tunnelHostSnapshot
	if err := c.runtimePathsSafe(); err != nil {
		return p, snapshot, err
	}
	root := c.tunnelPublicationRoot()
	pointer := filepath.Join(root, "current.json")
	if err := runtimeSafeParents(c.runtimeRoot(), pointer); err != nil {
		return p, snapshot, err
	}
	if err := runtimeRegular(pointer); err != nil {
		return p, snapshot, err
	}
	data, err := readTunnelBoundedFile(pointer, 4096)
	if err != nil || !tunnelUniqueJSONKeys(json.NewDecoder(bytes.NewReader(data))) {
		return p, snapshot, fmt.Errorf("invalid tunnel publication pointer")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if d.Decode(&p) != nil || p.Schema != 1 || p.Owner != c.tunnelOwner() || !validTunnelDigest(p.Revision) {
		return p, snapshot, fmt.Errorf("foreign/invalid tunnel publication pointer")
	}
	generation := filepath.Join(root, p.Revision)
	if p.HostsFile != filepath.Join(generation, "active-hosts") || p.OriginsFile != filepath.Join(generation, "active-origins.json") {
		return p, snapshot, fmt.Errorf("tunnel publication paths escape generation")
	}
	for _, file := range []string{p.HostsFile, p.OriginsFile} {
		if err = runtimeSafeParents(c.runtimeRoot(), file); err != nil {
			return p, snapshot, err
		}
		if err = runtimeRegular(file); err != nil {
			return p, snapshot, err
		}
		info, err := os.Stat(file)
		if err != nil || info.Size() > tunnelStateLimit {
			return p, snapshot, fmt.Errorf("oversized tunnel publication")
		}
	}
	snapshot.Hosts, err = readTunnelBoundedFile(p.HostsFile, tunnelStateLimit)
	if err != nil {
		return p, snapshot, err
	}
	snapshot.Origins, err = readTunnelBoundedFile(p.OriginsFile, tunnelStateLimit)
	if err != nil {
		return p, snapshot, err
	}
	snapshot.Revision = p.Revision
	return p, snapshot, validateTunnelHostSnapshot(snapshot)
}

func (c *Context) publishTunnelSnapshot(snapshot tunnelHostSnapshot) error {
	if err := validateTunnelHostSnapshot(snapshot); err != nil {
		return err
	}
	return c.withRuntimeLock(func() error {
		if _, err := c.ValidateControlRoot(); err != nil {
			return err
		}
		state, err := c.readTunnelJournal()
		if err != nil {
			return err
		}
		current, err := buildTunnelHostSnapshot(tunnelStateActive(state))
		if err != nil {
			return err
		}
		if current.Revision != snapshot.Revision {
			return fmt.Errorf("active tunnel snapshot changed; reconcile publication")
		}
		root := c.tunnelPublicationRoot()
		generation := filepath.Join(root, snapshot.Revision)
		if err = runtimeSafeParents(c.runtimeRoot(), filepath.Join(generation, "active-hosts")); err != nil {
			return err
		}
		if _, old, readErr := c.readTunnelPublication(); readErr == nil {
			if old.Revision == snapshot.Revision {
				return nil
			}
		} else if !os.IsNotExist(readErr) {
			return readErr
		}
		if err = os.MkdirAll(root, 0700); err != nil {
			return err
		}
		if _, err = os.Lstat(generation); err == nil {
			entries, err := os.ReadDir(generation)
			if err != nil || len(entries) != 2 {
				return fmt.Errorf("existing tunnel generation requires reconciliation")
			}
			for name, want := range map[string][]byte{"active-hosts": snapshot.Hosts, "active-origins.json": snapshot.Origins} {
				path := filepath.Join(generation, name)
				got, err := readTunnelBoundedFile(path, tunnelStateLimit)
				if err != nil || !bytes.Equal(got, want) {
					return fmt.Errorf("immutable tunnel generation was changed")
				}
			}
		} else if !os.IsNotExist(err) {
			return err
		} else {
			temp, err := os.MkdirTemp(root, ".generation-")
			if err != nil {
				return err
			}
			defer func() {
				os.Remove(filepath.Join(temp, "active-hosts"))
				os.Remove(filepath.Join(temp, "active-origins.json"))
				os.Remove(temp)
			}()
			if err = runtimeWrite(filepath.Join(temp, "active-hosts"), snapshot.Hosts); err != nil {
				return err
			}
			if err = runtimeWrite(filepath.Join(temp, "active-origins.json"), snapshot.Origins); err != nil {
				return err
			}
			if err = os.Rename(temp, generation); err != nil {
				return err
			}
		}
		p := tunnelPublication{1, c.tunnelOwner(), snapshot.Revision, filepath.Join(generation, "active-hosts"), filepath.Join(generation, "active-origins.json")}
		data, _ := json.Marshal(p)
		return runtimeWrite(filepath.Join(root, "current.json"), append(data, '\n'))
	})
}
