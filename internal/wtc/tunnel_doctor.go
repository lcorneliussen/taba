package wtc

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
)

// NamedTunnelDiagnostic is how doctor sees one configured named tunnel.
// Problems name the provider and mode, input fields, services, variables and
// the policy file. They never carry credential contents, account or node
// identities, or the values and paths of provider inputs; an unreadable task
// declaration is reported with the planner's own error, which can name the
// declaration file.
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
		if e.Policy.Provider == "" && !e.Disabled && (e.Selected == nil || !*e.Selected) {
			// A start-off selection without a definition, as stop or service
			// removal can leave behind, starts nothing.
			continue
		}
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

// namedTunnelTarget applies tunnelDefinitionFor's repository and target rules
// to an unresolved plan, assuming every declared variable holds a usable
// value.
func (c *Context) namedTunnelTarget(d *NamedTunnelDiagnostic, e tunnelEffectiveEntry, plan *runtimeManifest) {
	service := e.Key.Repo + "/" + e.Key.Service
	identity, err := c.prRepositories(e.Key.Repo).resolve(e.Key.Repo)
	switch {
	case err != nil:
		d.problem(err.Error(), "")
		return
	case identity != nil && identity.name != e.Key.Repo:
		d.problem("the tunnel names repository "+e.Key.Repo+" by an alias", "rename the entry to the registered repository name "+identity.name+" in "+d.Policy)
		return
	case identity == nil || identity.dir() == "":
		d.problem("repository "+e.Key.Repo+" is not checked out in this collection", "bring it in with `taba add-repo "+e.Key.Repo+"`, or remove the tunnel from "+d.Policy)
		return
	}
	dir := identity.dir()
	if !e.Matched {
		d.problem("service "+service+" is not declared", "declare "+e.Key.Service+" in "+dir+"/.harness/dekit.tasks.yaml or with `taba svc add`, or remove the tunnel from "+d.Policy)
		return
	}
	// The action takes the first service task, in path order, with a target;
	// per task url_env wins over x-wtc.url, then port_env, then a plain
	// ready.http probe URL. This plan has not read the env, so a port_env or
	// url_env target is shown by name. When a port_env variable is unset or
	// not a port, the action skips that task instead; doctor cannot tell.
	declared := c.namedTunnelDeclaredURLs(dir)
	literal := ""
	for _, task := range plan.Tasks {
		if task.Repo != e.Key.Repo || task.Dir != dir || task.Group != dir+"/"+e.Key.Service || task.Kind != "service" {
			continue
		}
		urls := declared[task.Path]
		switch {
		case urls.env != "":
			d.Target = "$" + urls.env
			return
		case urls.literal != "":
			literal = urls.literal
		case task.PortEnv != "":
			d.Target = "http://127.0.0.1:$" + task.PortEnv
			return
		case task.URL != "":
			literal = task.URL
		default:
			continue
		}
		break
	}
	if literal == "" {
		d.problem("service "+service+" declares no local URL or port", "declare x-wtc.port_env or x-wtc.url on "+service)
		return
	}
	d.Target = literal
	local, err := url.Parse(literal)
	if err != nil || local.Hostname() != "localhost" && local.Hostname() != "127.0.0.1" && local.Hostname() != "::1" {
		d.Target = ""
		d.problem("the local target of "+service+" is not a loopback URL", "point x-wtc.url on "+service+" at localhost or 127.0.0.1")
		return
	}
	port, err := strconv.Atoi(local.Port())
	explicitPort := err == nil && port >= 1 && port <= 65535
	if e.Policy.Provider == "devtunnel" && (!explicitPort || local.Scheme != "http" || local.Path != "" && local.Path != "/" || local.Hostname() == "::1") {
		d.problem("devtunnel needs an IPv4 or localhost HTTP target with a port and no path other than /", "set x-wtc.url on "+service+" to http://127.0.0.1:<port>, or declare x-wtc.port_env")
	}
	if e.Policy.Provider == "tailscale" && !tailscaleExplicitPort(literal) {
		d.problem("Tailscale Serve needs a target with an explicit port", "add the port to x-wtc.url on "+service+", or declare x-wtc.port_env")
	}
}

// tailscaleExplicitPort applies the Tailscale serve target's port rule: like
// the action, a scheme's default port does not count.
func tailscaleExplicitPort(target string) bool {
	address, err := parseTunnelAddress(target)
	if err != nil {
		return false
	}
	u, err := url.Parse(address.URL)
	return err == nil && u.Port() != ""
}

type namedTunnelURLs struct{ env, literal string }

// namedTunnelDeclaredURLs returns the x-wtc url_env and url of each task in
// a repository, by task path. The plan drops x-wtc after reading it and
// cannot tell a literal url from a ready.http probe, so read the same
// fragments again.
func (c *Context) namedTunnelDeclaredURLs(dir string) map[string]namedTunnelURLs {
	out := map[string]namedTunnelURLs{}
	// The same loader as rendering, so configured declaration files count.
	sources, err := c.runtimeRepoSources(filepath.Join(c.Collection, dir), dir, false)
	if err != nil {
		return out
	}
	for name, task := range sources.tasks {
		if metadata, ok := task.spec["x-wtc"].(map[string]any); ok {
			env, _ := metadata["url_env"].(string)
			literal, _ := metadata["url"].(string)
			out[dir+"/"+name] = namedTunnelURLs{env, literal}
		}
	}
	return out
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
