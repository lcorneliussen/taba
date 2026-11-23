package wtc

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// NamedTunnelDiagnostic is how doctor sees one configured named tunnel.
// Problems and fixes name fields, services and variables; they never carry
// credential contents, provider identities or input paths.
type NamedTunnelDiagnostic struct {
	Tunnel   string               `json:"tunnel"`
	Scope    string               `json:"scope,omitempty"`
	Policy   string               `json:"policy,omitempty"`
	Provider string               `json:"provider,omitempty"`
	State    string               `json:"state"`
	Target   string               `json:"target,omitempty"`
	Problems []NamedTunnelProblem `json:"problems,omitempty"`
}

// NamedTunnelProblem is one reason a named tunnel would not resolve, with the
// change that fixes it when doctor can name one.
type NamedTunnelProblem struct {
	Problem string `json:"problem"`
	Fix     string `json:"fix,omitempty"`
}

// Named tunnel diagnostic states.
const (
	NamedTunnelStateOK       = "ok"
	NamedTunnelStateProblem  = "problem"
	NamedTunnelStateDisabled = "disabled"
)

func (d *NamedTunnelDiagnostic) problem(problem, fix string) {
	d.State = NamedTunnelStateProblem
	d.Problems = append(d.Problems, NamedTunnelProblem{problem, fix})
}

// NamedTunnelDiagnostics reports whether each configured named tunnel would
// resolve: its service and local target, its repository identity, and the
// provider inputs that can be checked on this machine.
//
// It is observational. It reads the tunnel policy files and the native task
// declarations as discovery does, without sourcing the collection env: a
// target declared through x-wtc.port_env or x-wtc.url_env is reported by
// variable name, and only the explicit tunnel action reads its value. It
// checks provider inputs with stat alone: it reads no credential file,
// connects to no socket, runs no provider executable, hook or probe, makes no
// provider, account or network request, and writes nothing.
func (c *Context) NamedTunnelDiagnostics() ([]NamedTunnelDiagnostic, error) {
	entries, err := c.effectiveTunnels(nil)
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return nil, nil
	}
	plan, _, planErr := c.runtimePlanResolving("dekit", false, false)
	if planErr == nil {
		// Recompute service matching against the declared services.
		if entries, err = c.effectiveTunnels(plan); err != nil {
			return nil, err
		}
	}
	out := make([]NamedTunnelDiagnostic, 0, len(entries))
	for _, e := range entries {
		out = append(out, c.namedTunnelDiagnostic(e, plan, planErr))
	}
	return out, nil
}

func (c *Context) namedTunnelDiagnostic(e tunnelEffectiveEntry, plan *runtimeManifest, planErr error) NamedTunnelDiagnostic {
	d := NamedTunnelDiagnostic{Tunnel: e.Key.String(), Scope: e.Scope, Policy: c.namedTunnelPolicyName(e), State: NamedTunnelStateOK}
	service := e.Key.Repo + "/" + e.Key.Service
	if e.Disabled {
		d.State = NamedTunnelStateDisabled
		return d
	}
	if e.Policy.Provider == "" {
		d.problem("a start selection names this tunnel, but no definition exists", "configure it with `taba svc tunnel "+service+" --name "+e.Key.Name+" --action configure --definition <file>`, or remove its [starts] selection from "+d.Policy)
		return d
	}
	d.Provider = e.Policy.Provider + "/" + e.Policy.Mode
	if planErr != nil {
		d.problem("cannot read service declarations: "+planErr.Error(), "fix the declarations, then run `taba doctor` again")
	} else {
		c.namedTunnelTarget(&d, e, plan)
	}
	c.namedTunnelInputs(&d, e)
	return d
}

func (c *Context) namedTunnelPolicyName(e tunnelEffectiveEntry) string {
	if rel, err := filepath.Rel(c.Collection, e.Source); err == nil && tunnelRelativePath(rel) {
		return rel
	}
	return "the " + e.Scope + " tunnel policy"
}

// namedTunnelTarget mirrors tunnelDefinitionFor's repository and target rules
// on an unresolved plan.
func (c *Context) namedTunnelTarget(d *NamedTunnelDiagnostic, e tunnelEffectiveEntry, plan *runtimeManifest) {
	service := e.Key.Repo + "/" + e.Key.Service
	identity, err := c.prRepositories(e.Key.Repo).resolve(e.Key.Repo)
	switch {
	case err != nil:
		d.problem(err.Error(), "")
		return
	case identity == nil || identity.dir() == "":
		d.problem("repository "+e.Key.Repo+" is not checked out in this collection", "bring it in with `taba add-repo "+e.Key.Repo+"`, or remove the tunnel from "+d.Policy)
		return
	case identity.name != e.Key.Repo:
		d.problem("the tunnel names repository "+e.Key.Repo+" by an alias", "rename the entry to the registered repository name "+identity.name+" in "+d.Policy)
		return
	}
	dir := identity.dir()
	if !e.Matched {
		d.problem("service "+service+" is not declared", "declare "+e.Key.Service+" in "+dir+"/.harness/dekit.tasks.yaml or with `taba svc add`, or remove the tunnel from "+d.Policy)
		return
	}
	literal, portEnv := "", ""
	for _, task := range plan.Tasks {
		if task.Repo != e.Key.Repo || task.Dir != dir || task.Group != dir+"/"+e.Key.Service || task.Kind != "service" {
			continue
		}
		if literal == "" {
			literal = task.URL
		}
		if portEnv == "" {
			portEnv = task.PortEnv
		}
	}
	urlEnv := c.namedTunnelURLEnv(dir, e.Key.Service)
	switch {
	case urlEnv != "":
		d.Target = "$" + urlEnv
		return
	case literal != "":
		d.Target = literal
	case portEnv != "":
		d.Target = "http://127.0.0.1:$" + portEnv
		return
	default:
		d.problem("service "+service+" declares no local URL or port", "declare x-wtc.port_env or x-wtc.url on "+service)
		return
	}
	local, err := url.Parse(literal)
	if err != nil || local.Hostname() != "localhost" && local.Hostname() != "127.0.0.1" && local.Hostname() != "::1" {
		d.Target = ""
		d.problem("the local target of "+service+" is not a loopback URL", "point x-wtc.url on "+service+" at localhost or 127.0.0.1")
		return
	}
	port, err := strconv.Atoi(local.Port())
	explicitPort := err == nil && port >= 1 && port <= 65535
	if e.Policy.Provider == "devtunnel" && (!explicitPort || local.Scheme != "http" || local.Path != "" && local.Path != "/" || local.Hostname() == "::1") {
		d.problem("devtunnel needs an IPv4 or localhost HTTP target with a port and no path", "set x-wtc.url on "+service+" to http://127.0.0.1:<port>, or declare x-wtc.port_env")
	}
	if e.Policy.Provider == "tailscale" && !explicitPort {
		d.problem("Tailscale Serve needs a target with an explicit port", "add the port to x-wtc.url on "+service+", or declare x-wtc.port_env")
	}
}

// namedTunnelURLEnv returns the url_env a service's tasks declare. The
// unresolved plan drops x-wtc after reading it, so read the same fragments.
func (c *Context) namedTunnelURLEnv(dir, service string) string {
	found := []string{}
	for _, path := range []string{runtimeSharedFragment(filepath.Join(c.Collection, dir)), c.runtimeLocalFragment(dir)} {
		if runtimeRegular(path) != nil {
			continue
		}
		fragment, err := runtimeReadFragment(path)
		if err != nil {
			continue
		}
		for name, task := range fragment.Tasks {
			if strings.Split(name, "/")[0] != service || task == nil {
				continue
			}
			if kind, _ := task["type"].(string); kind == "job" {
				continue
			}
			if tags, ok := task["tags"].([]any); ok && (containsAny(tags, "endpoint") || containsAny(tags, "accessory")) {
				continue
			}
			if metadata, ok := task["x-wtc"].(map[string]any); ok {
				if variable, _ := metadata["url_env"].(string); variable != "" {
					found = append(found, variable)
				}
			}
		}
	}
	sort.Strings(found)
	if len(found) == 0 {
		return ""
	}
	return found[0]
}

func containsAny(values []any, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// The inputs each documented mode reads before or during hosting; see
// tunnelProviderRuntime.authorize and runHost.
func namedTunnelRequiredInputs(e tunnelPolicyEntry) []string {
	switch e.Provider + "/" + e.Mode {
	case "cloudflare/named":
		return []string{"executable", "hosting_file", "management_file", "cleanup_file"}
	case "cloudflare/quick":
		return []string{"executable"}
	case "devtunnel/persistent":
		return []string{"node", "hosting_file", "management_file", "cleanup_file"}
	case "devtunnel/temporary":
		return []string{"node", "management_file", "cleanup_file"}
	case "ngrok/domain":
		if e.Options.Reserve {
			return []string{"hosting_file", "management_file", "cleanup_file"}
		}
		return []string{"hosting_file"}
	case "tailscale/serve":
		return []string{"socket", "node_id", "dns_name"}
	}
	return nil
}

func (c *Context) namedTunnelInputs(d *NamedTunnelDiagnostic, e tunnelEffectiveEntry) {
	r := e.Policy.Inputs
	values := map[string]string{"management_file": r.ManagementFile, "hosting_file": r.HostingFile, "cleanup_file": r.CleanupFile, "executable": r.Executable, "node": r.Node, "socket": r.Socket, "node_id": r.NodeID, "dns_name": r.DNSName}
	for _, field := range namedTunnelRequiredInputs(e.Policy) {
		if values[field] == "" {
			d.problem("inputs."+field+" is required for "+d.Provider, "add inputs."+field+" to the tunnel in "+d.Policy)
		}
	}
	for _, field := range []string{"executable", "node"} {
		if path := values[field]; path != "" {
			if info, err := os.Stat(path); err != nil || !info.Mode().IsRegular() || info.Mode()&0111 == 0 {
				d.problem("inputs."+field+" is not an executable file", "install the program or correct inputs."+field+" in "+d.Policy)
			}
		}
	}
	if path := values["socket"]; path != "" {
		if info, err := os.Stat(path); err != nil || info.Mode()&os.ModeSocket == 0 {
			d.problem("inputs.socket names no socket on this machine", "start the local daemon or correct inputs.socket in "+d.Policy)
		}
	}
	var scope *ResolvedScope
	for _, field := range []string{"hosting_file", "management_file", "cleanup_file"} {
		ref := values[field]
		if ref == "" {
			continue
		}
		if scope == nil {
			s, err := c.ValidateControlRoot()
			if err != nil {
				d.problem("cannot resolve the control root for credential inputs: "+err.Error(), "")
				return
			}
			scope = &s
		}
		path := ref
		if !filepath.IsAbs(path) {
			path = filepath.Join(scope.Root, path)
		}
		if scope.validateSourcePath(path) != nil {
			d.problem("inputs."+field+" is outside the control root", "keep provider credentials in the control root and reference them from inputs."+field)
			continue
		}
		// Size only: the contents are read when the tunnel runs, never here.
		if info, err := os.Stat(path); err != nil || !info.Mode().IsRegular() || info.Size() == 0 || info.Size() > 16384 {
			d.problem(fmt.Sprintf("inputs.%s names no usable credential file", field), "create it in the control root, or correct inputs."+field+" in "+d.Policy)
		}
	}
}
