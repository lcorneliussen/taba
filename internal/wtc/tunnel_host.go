package wtc

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type tunnelHostReady struct {
	Schema      int                     `json:"schema"`
	Owner       string                  `json:"owner"`
	OperationID string                  `json:"operation_id"`
	Definition  string                  `json:"definition"`
	State       string                  `json:"state"`
	Address     tunnelAddress           `json:"address"`
	Parent      tunnelProcessIdentity   `json:"parent"`
	ChildPhase  string                  `json:"child_phase"`
	Children    []tunnelProcessIdentity `json:"children,omitempty"`
}

func (c *Context) tunnelHostDir(key tunnelKey) string {
	return filepath.Join(c.runtimeRoot(), "tunnel-hosts", tunnelDigest([]byte(key.String())))
}

func (p *tunnelProviderRuntime) hostTask(call tunnelEffectCall) (*runtimeManifest, runtimeTask, error) {
	m, err := p.c.runtimeManifest()
	if err != nil || m == nil || m.Schema != 2 {
		return m, runtimeTask{}, fmt.Errorf("managed host runtime/fence unavailable")
	}
	path := tunnelTaskPath(call.Definition.Directory, call.Entry.Key)
	for _, task := range m.Tasks {
		if task.Path == path && task.ManagedTunnel && task.Definition == call.Entry.Definition && task.Repo == call.Entry.Key.Repo {
			return m, task, nil
		}
	}
	return m, runtimeTask{}, fmt.Errorf("retained managed host task differs from runtime")
}

func (p *tunnelProviderRuntime) hostReceipt(call tunnelEffectCall, task runtimeTask) tunnelResourceRef {
	r := providerReceipt(call, "host-task", call.Owner, task.Path, task.Spec, "session")
	r.Instance = call.OperationID
	return r
}

func (p *tunnelProviderRuntime) readHostReady(call tunnelEffectCall) (tunnelHostReady, tunnelResourceRef, error) {
	var ready tunnelHostReady
	m, task, err := p.hostTask(call)
	if err != nil {
		return ready, tunnelResourceRef{}, err
	}
	path := filepath.Join(p.c.tunnelHostDir(call.Entry.Key), "ready.json")
	if err = runtimeSafeParents(p.c.runtimeRoot(), path); err != nil {
		return ready, tunnelResourceRef{}, err
	}
	data, err := readTunnelBoundedFile(path, 4096)
	if err != nil {
		return ready, tunnelResourceRef{}, err
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if !tunnelUniqueJSONKeys(json.NewDecoder(bytes.NewReader(data))) || d.Decode(&ready) != nil || ready.Schema != 1 || ready.Owner != call.Owner || ready.OperationID != call.OperationID || ready.Definition != call.Entry.Definition || ready.State != "active" {
		return ready, tunnelResourceRef{}, fmt.Errorf("managed host readiness is not for this operation")
	}
	if err = ready.validateProcesses(); err != nil || !ready.live() || call.Entry.Provider != "ngrok" && ready.ChildPhase != "owned" || call.Entry.Provider == "ngrok" && ready.ChildPhase != "none" {
		return ready, tunnelResourceRef{}, fmt.Errorf("owned host process liveness unconfirmed")
	}
	address, err := parseTunnelAddress(ready.Address.URL)
	if err != nil || address != ready.Address {
		return ready, tunnelResourceRef{}, fmt.Errorf("invalid managed host address")
	}
	for _, item := range p.c.runtimeStatus(m) {
		if item.Path == task.Path && (item.State == "ready" || item.State == "running") {
			return ready, p.hostReceipt(call, task), nil
		}
	}
	return ready, tunnelResourceRef{}, fmt.Errorf("owned host startup is not confirmed")
}

func (p *tunnelProviderRuntime) startHostTask(ctx context.Context, call tunnelEffectCall) (tunnelEffectResult, error) {
	result := tunnelEffectResult{}
	m, task, err := p.hostTask(call)
	if err != nil {
		return result, err
	}
	for _, item := range p.c.runtimeStatus(m) {
		if item.Path == task.Path && (item.State == "ready" || item.State == "running" || item.State == "starting" || item.State == "unknown") {
			return result, fmt.Errorf("pre-existing host task must be reconciled, never adopted")
		}
	}
	receipt := p.hostReceipt(call, task)
	if err = p.intent(ctx, call, "start-host", receipt.Scope, receipt.ID, receipt); err != nil {
		return result, err
	}
	if err = p.checkpoint(ctx, call, []tunnelResourceRef{receipt}, nil, true); err != nil {
		return result, err
	}
	dir := p.c.tunnelHostDir(call.Entry.Key)
	if err = runtimeSafeParents(p.c.runtimeRoot(), filepath.Join(dir, "ready.json")); err != nil {
		return result, err
	}
	if err = os.MkdirAll(dir, 0700); err != nil {
		return result, err
	}
	if _, err = runtimeCommand(m, false, "up", task.Path); err != nil {
		return result, fmt.Errorf("owned host launch unconfirmed; reconcile operation")
	}
	deadline := time.Now().Add(60 * time.Second)
	for {
		ready, _, readErr := p.readHostReady(call)
		if readErr == nil {
			result.Address = &ready.Address
			return result, nil
		}
		if err = ctx.Err(); err != nil {
			return result, err
		}
		if time.Now().After(deadline) {
			return result, fmt.Errorf("owned host readiness pending; reconcile operation")
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func (p *tunnelProviderRuntime) confirmHostStopped(call tunnelEffectCall) error {
	m, task, err := p.hostTask(call)
	if err != nil {
		return err
	}
	for _, item := range p.c.runtimeStatus(m) {
		if item.Path == task.Path && item.State != "idle" && item.State != "exited" && item.State != "done" && item.State != "absent" {
			return fmt.Errorf("owned host task shutdown not confirmed")
		}
	}
	for _, receipt := range call.Entry.Resources {
		if receipt.Kind != "host-task" {
			continue
		}
		ready, err := p.ownedHostObservation(call, receipt)
		if err != nil {
			return err
		}
		if err = ready.disposed(); err != nil {
			return err
		}
	}

	return nil
}

func (p *tunnelProviderRuntime) stopHostTask(ctx context.Context, call tunnelEffectCall) error {
	for _, receipt := range call.Entry.Resources {
		if receipt.Kind != "host-task" {
			continue
		}
		m, task, err := p.hostTask(call)
		if err != nil {
			return err
		}
		if receipt.Ownership != "owned" || receipt.Owner != call.Owner || receipt.Scope != call.Owner || receipt.ID != task.Path || receipt.Fingerprint != task.Spec {
			return fmt.Errorf("owned host task definition changed; shutdown refused")
		}
		if err = p.intent(ctx, call, "stop-host", receipt.Scope, receipt.ID, receipt); err != nil {
			return err
		}
		runner, runnerErr := runtimeRunnerState(m)
		if runnerErr != nil {
			return fmt.Errorf("owned runner state unknown; retain session")
		}
		switch runner {
		case "running":
			if _, err = runtimeCommand(m, false, "veto", task.Path); err != nil {
				return err
			}
			if err = p.c.runtimeWaitStopped(m, map[string]bool{task.Path: true}); err != nil {
				return err
			}
		case "absent":
			// Runner absence alone never proves provider-child disposal below.
		default:
			return fmt.Errorf("owned runner shutdown is pending")
		}
		ready, readErr := p.ownedHostObservation(call, receipt)
		if readErr != nil {
			return readErr
		}
		if err = ready.stopProcesses(ctx); err != nil {
			return err
		}
		if err = p.confirmHostStopped(call); err != nil {
			return err
		}
		if err = p.checkpoint(ctx, call, nil, []tunnelResourceRef{receipt}, true); err != nil {
			return err
		}
	}
	return nil
}

// ServeTunnelHost is a foreground task of the existing runner, never a detached
// ad-hoc daemon. Only a fenced, persisted pending start for this named entry
// admits it. Supplied credentials are read at use and never written to disk.
func (c *Context) ServeTunnelHost(ctx context.Context, repo, service, name string) error {
	ctx, cancel := signal.NotifyContext(ctx, syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	key := tunnelKey{repo, service, name}
	if err := key.validate(); err != nil {
		return err
	}
	state, err := c.readTunnelJournal()
	if err != nil {
		return err
	}
	e := lifecycleEntry(&state, key)
	if e == nil || e.Operation == nil || e.Operation.Lifecycle == nil || len(e.Operation.Steps) == 0 {
		return fmt.Errorf("pending named tunnel start required")
	}
	op := e.Operation
	last := op.Steps[len(op.Steps)-1]
	if op.Action != "start" || last.Name != "start" || last.Status != "pending" || op.Lifecycle.ProviderIntent == nil || op.Lifecycle.ProviderIntent.Kind != "start-host" {
		return fmt.Errorf("host task is not admitted by the current start")
	}
	p := c.tunnelProviders()
	call := tunnelEffectCall{Owner: c.tunnelOwner(), OperationID: op.ID, Stage: "start", Definition: op.Lifecycle.Definition, Entry: *e}
	l := tunnelLifecycle{Owner: call.Owner, Store: c.tunnelRuntimeStore(), Effects: p.effects()}
	if err = l.authorize(ctx, call); err != nil {
		return err
	}
	_, task, err := p.hostTask(call)
	if err != nil {
		return err
	}
	owned := false
	for _, r := range e.Resources {
		if r == p.hostReceipt(call, task) {
			owned = true
		}
	}
	if !owned {
		return fmt.Errorf("host task lacks its exact persisted ownership intent")
	}
	dir := c.tunnelHostDir(key)
	if err = runtimeSafeParents(c.runtimeRoot(), filepath.Join(dir, "ready.json")); err != nil {
		return err
	}
	if err = os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	parent, err := currentTunnelProcess()
	if err != nil {
		return err
	}
	observation := tunnelHostReady{Schema: 1, Owner: call.Owner, OperationID: op.ID, Definition: e.Definition, Parent: parent, ChildPhase: "none"}
	if e.Provider != "ngrok" {
		observation.ChildPhase = "not-launched"
	}
	write := func(status string, address tunnelAddress) error {
		observation.State, observation.Address = status, address
		data, _ := json.Marshal(observation)
		return runtimeWrite(filepath.Join(dir, "ready.json"), data)
	}
	if err = write("starting", tunnelAddress{}); err != nil {
		return err
	}
	observeChild := func(phase string, child tunnelProcessIdentity) error {
		observation.ChildPhase = phase
		if phase == "owned" {
			observation.Children = []tunnelProcessIdentity{child}
		}
		return write(observation.State, observation.Address)
	}

	ready := func(raw string) error {
		address, err := parseTunnelAddress(raw)
		if err != nil {
			return err
		}
		if err = write("active", address); err != nil {
			return err
		}
		fmt.Println("TUNNEL_READY")
		return nil
	}
	address, err := p.runHost(ctx, call, dir, ready, observeChild)
	if err != nil {
		_ = write("failed", address)
		return fmt.Errorf("provider host cleanup/start unconfirmed; reconcile named operation")
	}
	return write("stopped", address)
}

func writeTunnelAsset(path string, data []byte) error {
	if existing, err := readTunnelBoundedFile(path, int64(len(data))); err == nil {
		if !bytes.Equal(existing, data) {
			return fmt.Errorf("managed host asset changed; preserve before continuing")
		}
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	return runtimeWrite(path, data)
}

func (p *tunnelProviderRuntime) runHost(ctx context.Context, call tunnelEffectCall, dir string, ready func(string) error, observers ...func(string, tunnelProcessIdentity) error) (tunnelAddress, error) {
	d := call.Definition
	refs := d.Entry.Policy.Inputs
	address := tunnelAddress{}
	if call.Entry.Provider == "ngrok" {
		token, err := p.c.tunnelSecret(refs.HostingFile)
		if err != nil {
			return address, err
		}
		session, err := connectNgrokHost(ctx, token, &net.Dialer{Timeout: 10 * time.Second})
		if err != nil {
			return address, err
		}
		host, err := startNgrokHost(ctx, session, d.Entry.Policy.Options.Domain, d.Target)
		if err != nil {
			return address, err
		}
		address, _ = parseTunnelAddress(host.URL)
		if err = ready(address.URL); err == nil {
			<-ctx.Done()
		}
		stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if stopErr := host.stop(stopCtx); stopErr != nil {
			return address, stopErr
		}
		return address, err
	}
	env := minimalTunnelEnvironment(dir)
	var cmd *exec.Cmd
	var err error
	expected := ""
	dev := call.Entry.Provider == "devtunnel"
	if dev {
		if err = checkDevTunnelAssets(); err != nil {
			return address, err
		}
		helper := filepath.Join(dir, "devtunnel-host.cjs")
		if err = writeTunnelAsset(helper, devTunnelHelper); err != nil {
			return address, err
		}
		var registration *tunnelResourceRef
		for i := range call.Entry.Resources {
			r := &call.Entry.Resources[i]
			if r.Provider == "devtunnel" && r.Kind == "tunnel" && r.Ownership == "owned" {
				registration = r
				break
			}
		}
		if registration == nil {
			return address, fmt.Errorf("owned devtunnel registration missing")
		}
		token, raw, err := p.devHostCapability(ctx, call, *registration)
		if err != nil {
			return address, err
		}
		expected = raw
		backend, _ := url.Parse(d.Target)
		port, _ := strconv.Atoi(backend.Port())
		cmd, err = devTunnelHostCommand(ctx, refs.Node, helper, env, devTunnelHostInput{Schema: 1, TunnelID: registration.ID, ClusterID: registration.Scope, HostID: call.Owner, PortNumber: port, HostToken: token})
	} else if call.Entry.Provider == "cloudflare" {
		config := filepath.Join(dir, "cloudflared.json")
		if err = writeTunnelAsset(config, []byte("{}\n")); err != nil {
			return address, err
		}
		if d.Entry.Policy.Mode == "quick" {
			if !filepath.IsAbs(refs.Executable) {
				return address, fmt.Errorf("explicit cloudflared executable required")
			}
			cmd = exec.CommandContext(ctx, refs.Executable, "tunnel", "--no-autoupdate", "--config", config, "--url", d.Target)
			cmd.Env = env
		} else {
			var receipt *tunnelResourceRef
			for i := range call.Entry.Resources {
				r := &call.Entry.Resources[i]
				if r.Provider == "cloudflare" && r.Kind == "tunnel" && r.Ownership == "owned" {
					receipt = r
					break
				}
			}
			if receipt == nil {
				return address, fmt.Errorf("owned Cloudflare registration missing")
			}
			token, err := p.c.tunnelSecret(refs.HostingFile)
			if err != nil {
				return address, err
			}
			if !cloudflareTokenBound(token, receipt.Scope, receipt.ID) {
				client, err := p.explicitClient(d, refs.HostingFile)
				if err != nil {
					return address, err
				}
				observation, err := p.cloudflareObservation(ctx, client, *receipt)
				if err != nil {
					return address, err
				}
				token, err = getCloudflareHostToken(ctx, client, call.Owner, *receipt, observation)
				if err != nil {
					return address, err
				}
			}
			cmd, err = cloudflareHostCommand(ctx, refs.Executable, config, receipt.Scope, receipt.ID, token, env)
			expected = "https://" + d.Entry.Policy.Options.Hostname
		}
	} else {
		return address, fmt.Errorf("unsupported owned host provider")
	}
	if err != nil {
		return address, err
	}
	// Let official clients dispose their owned endpoint on SIGTERM. A forced
	// kill or missing stopped acknowledgement remains pending, never success.
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		return cmd.Process.Signal(syscall.SIGTERM)
	}
	cmd.WaitDelay = 10 * time.Second
	output, err := cmd.StdoutPipe()
	if err != nil {
		return address, err
	}
	cmd.Stderr = cmd.Stdout
	observe := func(phase string, child tunnelProcessIdentity) error {
		if len(observers) == 0 {
			return nil
		}
		return observers[0](phase, child)
	}
	if err = observe("launching", tunnelProcessIdentity{}); err != nil {
		return address, err
	}
	if err = cmd.Start(); err != nil {
		return address, fmt.Errorf("explicit provider host failed to start")
	}
	child, childState := readTunnelProcess(cmd.Process.Pid)
	if childState != "alive" {
		_ = cmd.Cancel()
		_ = cmd.Wait()
		return address, fmt.Errorf("owned child identity unavailable; retain launch intent")
	}
	if err = observe("owned", child); err != nil {
		_ = cmd.Cancel()
		_ = cmd.Wait()
		return address, err
	}
	started, stopped := false, false
	scanner := bufio.NewScanner(output)
	scanner.Buffer(make([]byte, 4096), 65536)
	quickURL := regexp.MustCompile(`https://[a-z0-9-]+\.trycloudflare\.com`)
	for scanner.Scan() {
		line := scanner.Bytes()
		if dev {
			var event struct {
				Schema int    `json:"schema"`
				Event  string `json:"event"`
			}
			if json.Unmarshal(line, &event) != nil || event.Schema != 1 {
				continue
			}
			if event.Event == "started" && !started {
				address, _ = parseTunnelAddress(expected)
				if err = ready(address.URL); err != nil {
					cmd.Cancel()
					break
				}
				started = true
			}
			if event.Event == "stopped" {
				stopped = true
			}
		} else if !started {
			if expected != "" && bytes.Contains(line, []byte("Registered tunnel connection")) {
				address, _ = parseTunnelAddress(expected)
				if err = ready(address.URL); err != nil {
					cmd.Cancel()
					break
				}
				started = true
			} else if raw := quickURL.Find(line); raw != nil {
				address, _ = parseTunnelAddress(string(raw))
				if err = ready(address.URL); err != nil {
					cmd.Cancel()
					break
				}
				started = true
			}
		}
	}
	waitErr := cmd.Wait()
	if err != nil || scanner.Err() != nil || !started {
		return address, fmt.Errorf("provider host observation incomplete")
	}
	if dev && !stopped || cmd.ProcessState == nil || cmd.ProcessState.ExitCode() != 0 {
		return address, fmt.Errorf("provider endpoint disposal not confirmed")
	}
	if waitErr != nil && ctx.Err() == nil {
		return address, fmt.Errorf("provider host exited unexpectedly")
	}
	return address, nil
}

func (p *tunnelProviderRuntime) devHostCapability(ctx context.Context, call tunnelEffectCall, receipt tunnelResourceRef) (string, string, error) {
	ref := call.Definition.Entry.Policy.Inputs.HostingFile
	if call.Entry.Mode == "temporary" {
		ref = call.Definition.Entry.Policy.Inputs.ManagementFile
	}
	token, err := p.c.tunnelSecret(ref)
	if err != nil {
		return "", "", err
	}
	if call.Entry.Mode != "temporary" && !strings.HasPrefix(token, "Bearer ") {
		if call.Entry.Known == nil {
			return "", "", fmt.Errorf("registered devtunnel URL missing")
		}
		return token, call.Entry.Known.URL, nil
	}
	if !strings.HasPrefix(token, "Bearer ") {
		token = "Bearer " + token
	}
	var wire struct {
		TunnelID     string            `json:"tunnelId"`
		ClusterID    string            `json:"clusterId"`
		AccessTokens map[string]string `json:"accessTokens"`
		Ports        []struct {
			Port int      `json:"portNumber"`
			URLs []string `json:"portForwardingUris"`
		} `json:"ports"`
	}
	client, err := p.explicitClient(call.Definition, ref)
	if err != nil {
		return "", "", err
	}
	_, err = client.request(ctx, http.MethodGet, "https://"+receipt.Scope+".rel.tunnels.api.visualstudio.com/tunnels/"+receipt.ID+"?api-version=2023-09-27-preview&includePorts=true&tokenScopes=host", nil, nil, &wire)
	if err != nil || wire.TunnelID != receipt.ID || wire.ClusterID != receipt.Scope || wire.AccessTokens["host"] == "" {
		return "", "", fmt.Errorf("exact devtunnel hosting capability unavailable")
	}
	backend, _ := url.Parse(call.Definition.Target)
	port, _ := strconv.Atoi(backend.Port())
	for _, entry := range wire.Ports {
		if entry.Port != port {
			continue
		}
		for _, raw := range entry.URLs {
			a, err := parseTunnelAddress(raw)
			if err == nil && strings.HasPrefix(a.Origin, "https://") && strings.HasSuffix(a.Hostname, ".devtunnels.ms") {
				return wire.AccessTokens["host"], a.URL, nil
			}
		}
	}
	return "", "", fmt.Errorf("exact devtunnel port URL unavailable")
}
