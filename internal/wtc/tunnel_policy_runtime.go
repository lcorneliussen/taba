package wtc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// References only. Values are read at the effect boundary through the actual
// receiving scope; they are never copied into policy, journal or hook payload.
type tunnelInputRefs struct {
	ManagementFile string `toml:"management_file" json:"management_file,omitempty"`
	HostingFile    string `toml:"hosting_file" json:"hosting_file,omitempty"`
	CleanupFile    string `toml:"cleanup_file" json:"cleanup_file,omitempty"`
	Executable     string `toml:"executable" json:"executable,omitempty"`
	Node           string `toml:"node" json:"node,omitempty"`
	Socket         string `toml:"socket" json:"socket,omitempty"`
	NodeID         string `toml:"node_id" json:"node_id,omitempty"`
	DNSName        string `toml:"dns_name" json:"dns_name,omitempty"`
}

type tunnelEventCommand struct {
	Command []string `toml:"command" json:"command"`
}

func tunnelRelativePath(path string) bool {
	return path != "" && path != "." && !filepath.IsAbs(path) && filepath.Clean(path) == path && path != ".." && !strings.HasPrefix(path, ".."+string(filepath.Separator)) && !strings.ContainsAny(path, "\x00\r\n\\")
}

func (r tunnelInputRefs) validate() error {
	for _, ref := range []string{r.ManagementFile, r.HostingFile, r.CleanupFile} {
		if ref != "" && (!filepath.IsAbs(ref) && !tunnelRelativePath(ref) || strings.ContainsAny(ref, "\r\n\x00") || len(ref) > 4096) {
			return fmt.Errorf("credential inputs must be explicit receiving-scope file references")
		}
	}
	for _, path := range []string{r.Executable, r.Node, r.Socket} {
		if path != "" && (!filepath.IsAbs(path) || strings.ContainsAny(path, "\x00\r\n")) {
			return fmt.Errorf("provider executable/socket inputs must be absolute")
		}
	}
	if r.NodeID != "" && !tunnelComponent.MatchString(r.NodeID) || r.DNSName != "" && !tunnelHostname(r.DNSName) {
		return fmt.Errorf("invalid explicit daemon identity")
	}
	return nil
}

func (c *Context) tunnelPolicyPaths() (string, string, error) {
	receiver, err := c.receivingContext()
	if err != nil {
		return "", "", err
	}
	collection := filepath.Join(receiver.Collection, ".config", "tunnels.toml")
	project := ""
	if receiver.Project != nil {
		project = filepath.Join(receiver.Project.Root, ".config", "tunnels.toml")
	}
	return project, collection, nil
}

func readTunnelPolicyFile(path string) (tunnelPolicy, error) {
	p := tunnelPolicy{Source: path, Entries: map[tunnelKey]tunnelPolicyEntry{}}
	if path == "" {
		return p, nil
	}
	if err := runtimeSafeParents(filepath.Dir(filepath.Dir(path)), path); err != nil {
		return p, err
	}
	if err := runtimeRegular(path); os.IsNotExist(err) {
		return p, nil
	} else if err != nil {
		return p, err
	}
	f, err := os.Open(path)
	if err != nil {
		return p, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 1<<20+1))
	if err != nil || len(data) > 1<<20 {
		return p, fmt.Errorf("cannot read bounded tunnel policy")
	}
	return parseTunnelPolicy(path, data)
}

func (c *Context) effectiveTunnels(plan *runtimeManifest) ([]tunnelEffectiveEntry, error) {
	projectPath, collectionPath, err := c.tunnelPolicyPaths()
	if err != nil {
		return nil, err
	}
	project, err := readTunnelPolicyFile(projectPath)
	if err != nil {
		return nil, err
	}
	collection, err := readTunnelPolicyFile(collectionPath)
	if err != nil {
		return nil, err
	}
	services := map[string]bool{}
	if plan != nil {
		for _, task := range plan.Tasks {
			if task.Kind == "service" {
				if service, ok := strings.CutPrefix(task.Group, task.Dir+"/"); ok && !strings.Contains(service, "/") {
					services[task.Repo+"/"+service] = true
				}
			}
		}
	}
	return effectiveTunnelPolicy(project, collection, services)
}

// SetTunnelPolicy is the one CLI/library writer. Gateway actions use only the
// collection branch; project mutation is an explicit native operation, never a
// consequence of discovering or owning one collection. Harness snapshots stay
// read-only while an actual receiving project's local policy remains writable.
func (c *Context) SetTunnelPolicy(repo, service, name string, entry tunnelPolicyEntry, project bool) error {
	key := tunnelKey{repo, service, name}
	if err := key.validate(); err != nil {
		return err
	}
	if err := entry.validate(); err != nil {
		return err
	}
	return c.mutateTunnelPolicy(project, func(policy *tunnelPolicy) error { policy.Entries[key] = entry; return nil })
}

func (c *Context) setTunnelStart(key tunnelKey, on bool) error {
	if err := key.validate(); err != nil {
		return err
	}
	return c.mutateTunnelPolicy(false, func(policy *tunnelPolicy) error {
		if policy.Starts == nil {
			policy.Starts = map[tunnelKey]bool{}
		}
		policy.Starts[key] = on
		return nil
	})
}

// All definition and session-selection writes share this one path and the
// same receiving authority, atomic replacement and appropriate policy lock.
func (c *Context) mutateTunnelPolicy(project bool, change func(*tunnelPolicy) error) error {
	if _, err := c.ValidateControlRoot(); err != nil {
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
		if state.Retiring {
			return fmt.Errorf("collection tunnel retirement is pending")
		}
		projectPath, collectionPath, err := c.tunnelPolicyPaths()
		if err != nil {
			return err
		}
		path := collectionPath
		if project {
			receiver, err := c.receivingContext()
			if err != nil {
				return err
			}
			if receiver.Project == nil {
				return fmt.Errorf("explicit writable receiving project required for project defaults")
			}
			path = projectPath
			// The harness source may be an immutable bare snapshot; this
			// target is the real receiving project's local policy. Serialize
			// its read/modify/write across native owners of different members.
			if err = runtimeSafeParents(receiver.Project.Root, path); err != nil {
				return err
			}
			if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			unlock, lockErr := lockDevFile(ctx, filepath.Join(filepath.Dir(path), ".tunnels-policy.lock"))
			if lockErr != nil {
				return fmt.Errorf("project tunnel policy lock: %w", lockErr)
			}
			defer unlock()
			current, _, err := c.tunnelPolicyPaths()
			if err != nil || current != path {
				return fmt.Errorf("receiving project policy changed while waiting")
			}
			if _, err = c.ValidateControlRoot(); err != nil {
				return err
			}
		}
		policy, err := readTunnelPolicyFile(path)
		if err != nil {
			return err
		}
		if err = change(&policy); err != nil {
			return err
		}
		doc := struct {
			Schema  int                                   `toml:"schema"`
			Tunnels map[string]map[string]map[string]any  `toml:"tunnels"`
			Starts  map[string]map[string]map[string]bool `toml:"starts,omitempty"`
		}{1, map[string]map[string]map[string]any{}, map[string]map[string]map[string]bool{}}
		for key, value := range policy.Entries {
			if doc.Tunnels[key.Repo] == nil {
				doc.Tunnels[key.Repo] = map[string]map[string]any{}
			}
			if doc.Tunnels[key.Repo][key.Service] == nil {
				doc.Tunnels[key.Repo][key.Service] = map[string]any{}
			}
			if value.disabled() {
				doc.Tunnels[key.Repo][key.Service][key.Name] = map[string]any{"enabled": false}
			} else {
				doc.Tunnels[key.Repo][key.Service][key.Name] = value
			}
		}
		for key, on := range policy.Starts {
			if doc.Starts[key.Repo] == nil {
				doc.Starts[key.Repo] = map[string]map[string]bool{}
			}
			if doc.Starts[key.Repo][key.Service] == nil {
				doc.Starts[key.Repo][key.Service] = map[string]bool{}
			}
			doc.Starts[key.Repo][key.Service][key.Name] = on
		}
		var buffer bytes.Buffer
		if err = toml.NewEncoder(&buffer).Encode(doc); err != nil {
			return err
		}
		data := buffer.Bytes()
		if _, err = parseTunnelPolicy(path, data); err != nil {
			return err
		}
		if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return err
		}
		if err = runtimeSafeParents(filepath.Dir(filepath.Dir(path)), path); err != nil {
			return err
		}
		if err = runtimeWrite(path, data); err != nil {
			return err
		}
		if !project {
			return c.recordTunnelPolicyLocked(path, data)
		}
		return nil
	})
}

func (c *Context) tunnelSecret(ref string) (string, error) {
	if ref == "" {
		return "", fmt.Errorf("explicit provider credential file required")
	}
	scope, err := c.ValidateControlRoot()
	if err != nil {
		return "", err
	}
	path := ref
	if !filepath.IsAbs(path) {
		path = filepath.Join(scope.Root, path)
	}
	if err = scope.validateSourcePath(path); err != nil {
		return "", err
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 16384 {
		return "", fmt.Errorf("explicit scoped credential file unavailable")
	}
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("explicit scoped credential file unavailable")
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 16385))
	if err != nil || len(data) > 16384 {
		return "", fmt.Errorf("invalid bounded provider credential input")
	}
	value := strings.TrimSpace(string(data))
	if value == "" || strings.ContainsAny(value, "\r\n\x00") {
		return "", fmt.Errorf("invalid provider credential input")
	}
	return value, nil
}

func (c *Context) tunnelDefinitionFor(e tunnelEffectiveEntry, plan *runtimeManifest) (tunnelDefinition, error) {
	d := tunnelDefinition{Owner: c.tunnelOwner(), Entry: e, ProviderScope: e.Policy.Provider + ":" + e.Policy.Options.AccountID + ":" + e.Policy.Options.ZoneID + ":" + e.Policy.Inputs.NodeID}
	scope, err := c.ValidateControlRoot()
	if err != nil {
		return d, err
	}
	scopeBytes, _ := json.Marshal([2]string{scope.OwnerRoot, scope.Root})
	d.ControlScope = tunnelDigest(scopeBytes)
	identities := c.prRepositories(e.Key.Repo)
	identity, err := identities.resolve(e.Key.Repo)
	if err != nil {
		return d, err
	}
	if identity == nil || identity.dir() == "" || identity.name != e.Key.Repo {
		return d, fmt.Errorf("registered tunnel repository is not uniquely checked out")
	}
	d.Directory = identity.dir()
	identityBytes, _ := json.Marshal([3]string{identity.name, prForgeIdentity(identity.remote), d.Directory})
	d.RepositoryIdentity = tunnelDigest(identityBytes)
	for _, task := range plan.Tasks {
		if task.Repo == e.Key.Repo && task.Dir == d.Directory && task.Group == task.Dir+"/"+e.Key.Service && task.Kind == "service" && task.URL != "" {
			d.Target = task.URL
			break
		}
	}
	if d.Target == "" {
		return d, fmt.Errorf("named tunnel service needs an explicit local URL/port")
	}
	local, err := url.Parse(d.Target)
	if err != nil || local.Hostname() != "localhost" && local.Hostname() != "127.0.0.1" && local.Hostname() != "::1" {
		return d, fmt.Errorf("named tunnels require an explicit loopback service target")
	}
	if e.Policy.Provider == "devtunnel" {
		port, err := strconv.Atoi(local.Port())
		if err != nil || port < 1 || port > 65535 || local.Scheme != "http" || local.Path != "" && local.Path != "/" || local.Hostname() == "::1" {
			return d, fmt.Errorf("devtunnel host requires an explicit IPv4/localhost HTTP port without a backend path")
		}
	}
	d.HooksDigest, err = c.tunnelCallbackDigest(d.Directory, e.Policy.Events)
	if err != nil {
		return d, err
	}
	if len(e.Policy.Reload) > 0 {
		_, d.ReloadDigest, err = c.tunnelReloadPlan(plan, e.Policy.Reload)
		if err != nil {
			return d, err
		}
	}
	_, err = d.digest()
	return d, err
}
