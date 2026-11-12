//go:build openclaw_integration

package openclaw

// Helpers for the tests that drive a throwaway OpenClaw gateway in Docker.
//
//	go test -tags openclaw_integration ./internal/openclaw/ -v
//
// The gateway runs from testdata/gateway.Dockerfile: the OpenClaw image named
// by WTC_OPENCLAW_IMAGE (or the default below) plus a Git and a GitHub CLI
// recent enough to publish, and the wtc binary built from this checkout.
// OpenClaw refuses to allocate a worktree with less than about 4 GiB free, so
// the gateway's state lives on memory-backed filesystems, or, for a gateway
// that runs its agents' commands in sandbox containers, on the Docker host's
// disk (see gatewayOptions.sandbox).

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

const defaultImage = "ghcr.io/openclaw/openclaw:2026.10.2-browser"

const gatewayEnv = `cd /app
export HOME=/home/node OPENCLAW_HOME=/home/node OPENCLAW_STATE_DIR=/home/node/.openclaw
export OPENCLAW_CONFIG_PATH=/home/node/.openclaw/openclaw.json OPENCLAW_CONFIG_DIR=/home/node/.openclaw
`

const gatewayPrepare = gatewayEnv + `set -eu
git config --global user.email test@example.com
git config --global user.name Test
git config --global init.defaultBranch main
mkdir -p /tmp/origins /home/node/repos "$OPENCLAW_STATE_DIR/workspaces/dev"
`

// Without a model account the gateway's agents cannot run a turn. stubModel
// gives agent dev a model served by testdata/stub-model.mjs, and offers every
// tool to it directly instead of behind tool search.
const stubModel = `,
  "models": { "mode": "merge", "providers": { "stub": {
    "baseUrl": "http://127.0.0.1:18999/v1", "apiKey": "stub-local", "api": "openai-completions",
    "agentRuntime": { "id": "openclaw" },
    "models": [ { "id": "stub-model", "name": "Stub Model", "reasoning": false, "input": ["text"],
                  "cost": { "input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0 },
                  "contextWindow": 200000, "maxTokens": 8192 } ] } } },
  "tools": { "toolSearch": false }`

// gatewayStart writes the gateway's configuration and starts it. sandbox is
// the agents' sandbox configuration, or empty to run their commands on the
// gateway. tailnet serves the gateway on the container's tailnet and lets
// people sign in as their Tailscale identity.
func gatewayStart(stub bool, sandbox string, tailnet bool) string {
	model, defaults, start := "", []string{}, ""
	if stub {
		model, defaults = stubModel, append(defaults, `"model": { "primary": "stub/stub-model" }`)
		start = "mkdir -p /tmp/stub && nohup node /tmp/stub-model.mjs > /tmp/stub/stub.log 2>&1 &\n"
	}
	if sandbox != "" {
		defaults = append(defaults, `"sandbox": `+sandbox)
	}
	auth := `"auth": { "mode": "token", "token": "t" }`
	if tailnet {
		auth = `"auth": { "mode": "token", "token": "t", "allowTailscale": true }, "tailscale": { "mode": "serve" }`
	}
	agents := ""
	if len(defaults) > 0 {
		agents = `"defaults": { ` + strings.Join(defaults, ", ") + ` }, `
	}
	return `
cd /app
cat > "$OPENCLAW_CONFIG_PATH" <<JSON
{ "gateway": { "mode": "local", "bind": "loopback", "port": 18789,
               ` + auth + `,
               "tools": { "allow": ["sessions_spawn", "github_publish"] } },
  "agents": { ` + agents + `"entries": { "dev": { "workspace": "$OPENCLAW_STATE_DIR/workspaces/dev" } } }` + model + ` }
JSON
` + start + gatewayBoot
}

const gatewayBoot = `for _ in 1 2 3; do node dist/index.js doctor --fix --non-interactive > /tmp/doctor.log 2>&1 && break; done
nohup node dist/index.js gateway run --allow-unconfigured > /tmp/gateway.log 2>&1 &
for _ in $(seq 1 120); do
  node dist/index.js gateway call health --token t --json >/dev/null 2>&1 && exit 0
  sleep 1
done
tail -50 /tmp/gateway.log >&2
exit 1
`

var (
	imageOnce sync.Once
	imageTag  string
	imageErr  string
)

// testImage builds the gateway image once per test run. Docker's layer cache
// keeps the Git build from being repeated.
func testImage(t *testing.T) string {
	t.Helper()
	imageOnce.Do(func() {
		base := os.Getenv("WTC_OPENCLAW_IMAGE")
		if base == "" {
			base = defaultImage
		}
		fail := func(what string, err error, out []byte) { imageErr = what + ": " + err.Error() + "\n" + string(out) }
		arch, err := exec.Command("docker", "version", "--format", "{{.Server.Arch}}").Output()
		if err != nil {
			fail("docker version", err, nil)
			return
		}
		dir, err := os.MkdirTemp("", "wtc-openclaw-image")
		if err != nil {
			fail("temp dir", err, nil)
			return
		}
		defer os.RemoveAll(dir)
		dockerfile, err := os.ReadFile(filepath.Join("testdata", "gateway.Dockerfile"))
		if err == nil {
			err = os.WriteFile(filepath.Join(dir, "Dockerfile"), dockerfile, 0o644)
		}
		if err != nil {
			fail("Dockerfile", err, nil)
			return
		}
		build := exec.Command("go", "build", "-o", filepath.Join(dir, "wtc"), "../../cmd/taba")
		build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+strings.TrimSpace(string(arch)))
		if out, err := build.CombinedOutput(); err != nil {
			fail("go build wtc", err, out)
			return
		}
		tag := "wtc-openclaw-test:" + strings.NewReplacer("/", "-", ":", "-").Replace(base)
		if out, err := exec.Command("docker", "build", "-q", "--build-arg", "OPENCLAW_IMAGE="+base, "-t", tag, dir).CombinedOutput(); err != nil {
			fail("docker build", err, out)
			return
		}
		imageTag = tag
	})
	if imageErr != "" {
		t.Fatal(imageErr)
	}
	return imageTag
}

var (
	sandboxOnce sync.Once
	sandboxTag  string
	sandboxErr  string
)

// sandboxImage builds the image the gateway starts sandbox containers from:
// OpenClaw's default sandbox image plus the wtc binary of the gateway image.
func sandboxImage(t *testing.T) string {
	t.Helper()
	gatewayImage := testImage(t)
	sandboxOnce.Do(func() {
		dir, err := os.MkdirTemp("", "wtc-openclaw-sandbox")
		if err != nil {
			sandboxErr = "temp dir: " + err.Error()
			return
		}
		defer os.RemoveAll(dir)
		tag := strings.Replace(gatewayImage, "wtc-openclaw-test:", "wtc-openclaw-sandbox:", 1)
		dockerfile := filepath.Join("testdata", "sandbox.Dockerfile")
		if out, err := exec.Command("docker", "build", "-q", "--build-arg", "GATEWAY_IMAGE="+gatewayImage, "-f", dockerfile, "-t", tag, dir).CombinedOutput(); err != nil {
			sandboxErr = "docker build: " + err.Error() + "\n" + string(out)
			return
		}
		sandboxTag = tag
	})
	if sandboxErr != "" {
		t.Fatal(sandboxErr)
	}
	return sandboxTag
}

var (
	tailscaleOnce sync.Once
	tailscaleTag  string
	tailscaleErr  string
)

// tailscaleImage is the gateway image plus Tailscale.
func tailscaleImage(t *testing.T) string {
	t.Helper()
	gatewayImage := testImage(t)
	tailscaleOnce.Do(func() {
		dir, err := os.MkdirTemp("", "wtc-openclaw-tailscale")
		if err != nil {
			tailscaleErr = "temp dir: " + err.Error()
			return
		}
		defer os.RemoveAll(dir)
		tag := strings.Replace(gatewayImage, "wtc-openclaw-test:", "wtc-openclaw-tailscale:", 1)
		dockerfile := filepath.Join("testdata", "tailscale.Dockerfile")
		if out, err := exec.Command("docker", "build", "-q", "--build-arg", "BASE_IMAGE="+gatewayImage, "-f", dockerfile, "-t", tag, dir).CombinedOutput(); err != nil {
			tailscaleErr = "docker build: " + err.Error() + "\n" + string(out)
			return
		}
		tailscaleTag = tag
	})
	if tailscaleErr != "" {
		t.Fatal(tailscaleErr)
	}
	return tailscaleTag
}

// tailnetArgs let a container run its own tailscaled with a TUN device: its
// tailnet traffic stays inside the container's network namespace.
var tailnetArgs = []string{"--cap-add", "NET_ADMIN", "--device", "/dev/net/tun"}

// joinTailnet starts tailscaled in a container and brings it up on the
// tailnet the auth key in $WTC_TS_AUTHKEY (passed by name) belongs to. The
// node is ephemeral: it leaves the tailnet when the container goes. It
// returns the node's MagicDNS name.
func joinTailnet(t *testing.T, container, hostname string, env []string) string {
	t.Helper()
	cmd := exec.Command("docker", "exec", "-i", "-u", "0", "-e", "WTC_TS_AUTHKEY", container, "sh", "-c", `set -e
mkdir -p /var/lib/tailscale /var/run/tailscale
nohup tailscaled --state=mem: --socket=/var/run/tailscale/tailscaled.sock > /tmp/tailscaled.log 2>&1 &
for _ in $(seq 1 30); do tailscale status >/dev/null 2>&1 && break; [ -S /var/run/tailscale/tailscaled.sock ] && break; sleep 1; done
tailscale up --authkey="$WTC_TS_AUTHKEY" --hostname=`+hostname+` --operator=node --accept-dns=true --timeout=60s
tailscale status --json | node -e 'let s="";process.stdin.on("data",d=>s+=d).on("end",()=>console.log(JSON.parse(s).Self.DNSName.replace(/\.$/,"")))'`)
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("joining the tailnet as %s: %v\n%s", hostname, err, out)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	return lines[len(lines)-1]
}

// sandboxRoot is the directory on the Docker host that holds the state of
// sandboxed gateways, one directory per test.
const sandboxRoot = "/var/lib/wtc-openclaw-e2e"

// onDockerHost runs script as root in a throwaway container that sees the
// Docker host's /var/lib at /host-var-lib, and its Docker socket.
func onDockerHost(script string) (string, error) {
	out, err := exec.Command("docker", "run", "--rm", "--user", "0", "--entrypoint", "sh",
		"-v", "/var/lib:/host-var-lib", "-v", "/var/run/docker.sock:/var/run/docker.sock",
		imageTag, "-c", script).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

type gateway struct {
	t         *testing.T
	container string
	// secrets are passed into the container's environment by name and kept
	// out of command lines and failure output.
	secrets []string
	// hidden are further values that must not show up in failure output.
	hidden []string
	// tailnetName is the gateway's MagicDNS name on the test tailnet.
	tailnetName string
	// sandboxPrefix names the sandbox containers of a sandboxed gateway.
	sandboxPrefix string
	// network is the Docker network the gateway shares with its sandboxes.
	network string
}

type gatewayOptions struct {
	// repos is a script that prepares the repositories under /home/node/repos.
	repos string
	// secrets are NAME=value pairs the gateway process and every later
	// command see in their environment.
	secrets []string
	// files are written into the container before repos runs, readable by
	// the gateway's user only. The values in a *.login file are kept out of
	// failure output.
	files map[string][]byte
	// stubModel lets agent dev run turns against the scripted model.
	stubModel bool
	// plugin installs the collection plugin from ../../openclaw-plugin.
	plugin bool
	// sandbox runs every session's commands in a Docker sandbox container,
	// which the gateway starts on the Docker daemon the tests use, through
	// its socket. The daemon resolves bind mounts on its own host, and
	// OpenClaw translates the paths it mounts only for sources that are bind
	// mounts into the gateway, so /home/node is a directory on the Docker
	// host instead of memory; that also leaves room for OpenClaw's worktree
	// reserve.
	sandbox bool
	// sandboxNetwork puts the gateway and its sandbox containers on a Docker
	// network of their own, so a sandbox can reach the gateway. Without it a
	// sandbox has no network at all, OpenClaw's default.
	sandboxNetwork bool
	// tailnetKey joins the gateway to the tailnet this auth key belongs to
	// and serves it there, so people can sign in as their Tailscale identity.
	tailnetKey string
}

func startGateway(t *testing.T, opt gatewayOptions) *gateway {
	t.Helper()
	keep := os.Getenv("WTC_OPENCLAW_KEEP") != ""
	home := []string{"--tmpfs", "/home/node:size=9g,uid=1000,gid=1000,exec"}
	sandbox := ""
	g := &gateway{t: t, secrets: opt.secrets}
	if opt.sandbox {
		image := sandboxImage(t)
		id := make([]byte, 6)
		_, _ = rand.Read(id)
		g.sandboxPrefix = "wtc-sbx-" + hex.EncodeToString(id) + "-"
		network := ""
		if opt.sandboxNetwork {
			g.network = g.sandboxPrefix + "net"
			if out, err := exec.Command("docker", "network", "create", g.network).CombinedOutput(); err != nil {
				t.Fatalf("docker network create: %v\n%s", err, out)
			}
			// Registered first, so it runs after the containers are gone.
			t.Cleanup(func() {
				if !keep {
					_ = exec.Command("docker", "network", "rm", g.network).Run()
				}
			})
			network = `, "network": "` + g.network + `"`
		}
		dir := sandboxRoot + "/" + hex.EncodeToString(id)
		seen := "/host-var-lib/" + strings.TrimPrefix(dir, "/var/lib/")
		// The gateway's user needs the group that owns the socket.
		group, err := onDockerHost("mkdir -p " + seen + " && chown 1000:1000 " + seen + " && stat -c %g /var/run/docker.sock")
		if err != nil {
			t.Fatalf("preparing %s on the Docker host: %v\n%s", dir, err, group)
		}
		t.Cleanup(func() {
			if keep {
				t.Logf("kept %s on the Docker host and the sandbox containers named %s*", dir, g.sandboxPrefix)
				return
			}
			// The sandbox containers are the gateway's; it leaves them running.
			names, _ := exec.Command("docker", "ps", "-aq", "--filter", "name=^"+g.sandboxPrefix).Output()
			if ids := strings.Fields(string(names)); len(ids) > 0 {
				_ = exec.Command("docker", append([]string{"rm", "-f"}, ids...)...).Run()
			}
			if out, err := onDockerHost("rm -rf " + seen + " && { rmdir " + path.Dir(seen) + " 2>/dev/null || true; }"); err != nil {
				t.Logf("removing %s on the Docker host: %v\n%s", dir, err, out)
			}
		})
		home = []string{"-v", dir + ":/home/node", "-v", "/var/run/docker.sock:/var/run/docker.sock", "--group-add", group}
		if g.network != "" {
			home = append(home, "--network", g.network)
		}
		sandbox = `{ "mode": "all", "backend": "docker", "scope": "session",
                "docker": { "image": "` + image + `", "containerPrefix": "` + g.sandboxPrefix + `"` + network + ` } }`
	}
	image := testImage(t)
	if opt.tailnetKey != "" {
		image = tailscaleImage(t)
		home = append(home, tailnetArgs...)
		g.hidden = append(g.hidden, opt.tailnetKey)
	}
	args := append(append([]string{"run", "-d"}, home...), "--tmpfs", "/tmp:size=6g,exec", "--entrypoint", "sleep", image, "infinity")
	out, err := exec.Command("docker", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("docker run: %v\n%s", err, out)
	}
	g.container = strings.TrimSpace(string(out))
	t.Cleanup(func() {
		// WTC_OPENCLAW_KEEP leaves the gateway running for a look inside.
		if keep {
			t.Logf("kept container %s", g.container)
			return
		}
		_ = exec.Command("docker", "rm", "-f", g.container).Run()
	})
	for path, content := range opt.files {
		if strings.HasSuffix(path, ".login") {
			for _, line := range strings.Fields(string(content)) {
				_, value, _ := strings.Cut(line, "=")
				g.hidden = append(g.hidden, value)
			}
		}
		g.put(path, content)
		g.sh("chmod 600 " + path)
	}
	if opt.stubModel {
		stub, err := os.ReadFile(filepath.Join("testdata", "stub-model.mjs"))
		if err != nil {
			t.Fatal(err)
		}
		g.put("/tmp/stub-model.mjs", stub)
	}
	if opt.tailnetKey != "" {
		g.tailnetName = joinTailnet(t, g.container, "wtc-gw-"+g.container[:8], []string{"WTC_TS_AUTHKEY=" + opt.tailnetKey})
	}
	if out, err := g.bash(gatewayPrepare + opt.repos + gatewayStart(opt.stubModel, sandbox, opt.tailnetKey != "")); err != nil {
		t.Fatalf("gateway setup: %v\n%s", err, g.redact(out))
	}
	if opt.plugin {
		source := filepath.Join("..", "..", "openclaw-plugin")
		err := filepath.WalkDir(source, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(source, path)
			g.put("/home/node/wtc-plugin/"+filepath.ToSlash(rel), content)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		// A plugin that brings its own Control UI code is behind a switch.
		g.sh(gatewayEnv + `node dist/index.js config set gateway.controlUi.experimental.customPlugins true >/dev/null 2>&1
node dist/index.js plugins install -l /home/node/wtc-plugin --force 2>&1 | tail -3`)
	}
	return g
}

func (g *gateway) exec(args ...string) *exec.Cmd {
	full := []string{"exec", "-i"}
	for _, s := range g.secrets {
		name, _, _ := strings.Cut(s, "=")
		full = append(full, "-e", name)
	}
	cmd := exec.Command("docker", append(append(full, g.container), args...)...)
	cmd.Env = append(os.Environ(), g.secrets...)
	return cmd
}

func (g *gateway) redact(s string) string {
	for _, secret := range g.secrets {
		if _, v, _ := strings.Cut(secret, "="); v != "" {
			s = strings.ReplaceAll(s, v, "<redacted>")
		}
	}
	for _, v := range g.hidden {
		if len(v) >= 8 {
			s = strings.ReplaceAll(s, v, "<redacted>")
		}
	}
	return s
}

// put writes a file in the container. docker cp cannot: the paths that
// matter are memory-backed mounts.
func (g *gateway) put(path string, content []byte) {
	g.t.Helper()
	cmd := g.exec("sh", "-c", `mkdir -p "$(dirname "$1")" && cat > "$1"`, "put", path)
	cmd.Stdin = bytes.NewReader(content)
	if out, err := cmd.CombinedOutput(); err != nil {
		g.t.Fatalf("write %s: %v\n%s", path, err, out)
	}
}

// step is one answer of the scripted model; see testdata/stub-model.mjs.
type step map[string]any

// turn sends message to a session and waits for the agent's turn, in which
// the model answers with steps. cwd is the session's working directory: the
// steps are only served to turns running there, because other sessions take
// turns of their own meanwhile — a parent is woken when a child finishes.
// It returns what agent.wait reports.
func (g *gateway) turn(sessionKey, cwd, message string, steps ...step) reply {
	g.t.Helper()
	for _, s := range steps {
		if _, routed := s["match"]; !routed {
			s["cwd"] = cwd
		}
	}
	script, err := json.Marshal(map[string]any{"steps": steps})
	if err != nil {
		g.t.Fatal(err)
	}
	g.put("/tmp/stub/steps.json", script)
	sent := g.call("sessions.send", map[string]any{"key": sessionKey, "message": message})
	if sent.str("runId") == "" {
		g.t.Fatalf("sessions.send: %v", sent)
	}
	done := g.call("agent.wait", map[string]any{"runId": sent.str("runId"), "timeoutMs": 120000})
	if done.str("status") != "ok" {
		g.t.Fatalf("agent turn did not finish: %v\n%s", done, g.modelLog())
	}
	return done
}

const agentWorkspace = "/home/node/.openclaw/workspaces/dev"

// modelLog is what the scripted model was asked and answered, one line per
// request: the step it served and the latest tool result it was shown.
func (g *gateway) modelLog() string {
	out, _ := g.bash(`node -e '
const fs = require("fs");
for (const line of fs.readFileSync("/tmp/stub/requests.jsonl", "utf8").split("\n").filter(Boolean)) {
  const r = JSON.parse(line); if (!r.served) continue;
  const tools = (r.body.messages || []).filter((m) => m.role === "tool");
  const last = tools.length ? JSON.stringify(tools[tools.length - 1].content).slice(0, 700) : "-";
  console.log("#" + r.seq, "tools=" + (r.body.tools || []).length, "served=" + JSON.stringify(r.served.step).slice(0, 300), "\n   last tool result:", last);
}'`)
	return g.redact(out)
}

func (g *gateway) bash(script string) (string, error) {
	cmd := g.exec("bash", "-s")
	cmd.Stdin = strings.NewReader(script)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// sh runs a script that has to succeed and returns its trimmed output.
func (g *gateway) sh(script string) string {
	g.t.Helper()
	out, err := g.bash("set -eu\n" + script)
	if err != nil {
		g.t.Fatalf("%s: %v\n%s", script, err, g.redact(out))
	}
	return strings.TrimSpace(out)
}

type reply map[string]any

func (r reply) str(path ...string) string {
	var v any = map[string]any(r)
	for _, k := range path {
		m, _ := v.(map[string]any)
		v = m[k]
	}
	s, _ := v.(string)
	return s
}

func (r reply) failed() bool { ok, set := r["ok"].(bool); return set && !ok }

// call is one gateway method call. A refusal comes back as a reply with
// ok=false, not as a test failure.
func (g *gateway) call(method string, params any) reply {
	g.t.Helper()
	p, err := json.Marshal(params)
	if err != nil {
		g.t.Fatal(err)
	}
	cmd := g.exec("bash", "-c", gatewayEnv+`node dist/index.js gateway call `+method+` --token t --json --timeout 150000 --params "$(cat)" 2>&1`)
	cmd.Stdin = bytes.NewReader(p)
	out, _ := cmd.Output()
	return g.decode(method, out)
}

// invoke is one tool call made as sessionKey, through POST /tools/invoke.
func (g *gateway) invoke(tool, sessionKey string, args any) reply {
	g.t.Helper()
	body, err := json.Marshal(map[string]any{"tool": tool, "sessionKey": sessionKey, "args": args})
	if err != nil {
		g.t.Fatal(err)
	}
	cmd := g.exec("curl", "-s", "-X", "POST",
		"http://127.0.0.1:18789/tools/invoke", "-H", "Authorization: Bearer t",
		"-H", "Content-Type: application/json", "--data-binary", "@-")
	cmd.Stdin = bytes.NewReader(body)
	out, _ := cmd.Output()
	return g.decode(tool, out)
}

func (g *gateway) decode(what string, out []byte) reply {
	g.t.Helper()
	start := bytes.IndexByte(out, '{')
	var r reply
	if start < 0 || json.Unmarshal(out[start:], &r) != nil {
		g.t.Fatalf("%s: no JSON reply:\n%s", what, g.redact(string(out)))
	}
	return r
}

// createFromIntent does what sessions_spawn does with an add-repo intent,
// minus the agent turn: the same project and worktree arguments, given to
// sessions.create under the parent.
func (g *gateway) createFromIntent(parent string, in Intent) reply {
	g.t.Helper()
	raw, err := json.Marshal(in.Args)
	if err != nil {
		g.t.Fatal(err)
	}
	params := map[string]any{}
	if err := json.Unmarshal(raw, &params); err != nil {
		g.t.Fatal(err)
	}
	delete(params, "task")
	delete(params, "visible")
	params["agentId"] = "dev"
	params["parentSessionKey"] = parent
	return g.call("sessions.create", params)
}

// worktreePath is where the gateway put repo's managed worktree named name.
func (g *gateway) worktreePath(repo, name string) string {
	g.t.Helper()
	return g.sh("git -C /home/node/repos/" + repo + " worktree list --porcelain | sed -n 's/^worktree //p' | grep /" + name + "$")
}
