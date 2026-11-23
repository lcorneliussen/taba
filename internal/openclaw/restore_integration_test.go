//go:build openclaw_integration

package openclaw

// Restoring an evicted checkout from the Collection view (#494), against a
// real OpenClaw Gateway and its own client and authentication contract.
//
// The gateway runs in trusted-proxy mode with no shared secret on the host:
// people are identified by a (synthetic, loopback) identity-aware proxy and
// get gateway admin only through gateway.auth.identityScopes. That is the
// setup in which a restore started through the host's own CLI fails with
// "unauthorized": the CLI has no credential of the person who asked. Clients
// connect with the SDK's GatewayClient, exactly as an operator UI does.
//
// Everything is synthetic and contained: the container is detached from every
// Docker network after setup (only loopback remains), identities, repositories,
// snapshots and pairings are disposable, and no real account is involved.

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
)

// restoreProxy is the synthetic identity-aware reverse proxy: one loopback
// port per user. It overwrites, never forwards, client identity headers.
const restoreProxy = `import http from "node:http"; import net from "node:net";
const [upstream, ...pairs] = process.argv.slice(2);
for (const pair of pairs) {
  const [port, user] = pair.split("=");
  const server = http.createServer((req, res) => { res.writeHead(400); res.end(); });
  server.on("upgrade", (req, socket, head) => {
    const up = net.connect(Number(upstream), "127.0.0.1", () => {
      const h = Object.entries(req.headers).filter(([k]) => !/^x-forwarded-|^x-openclaw-/i.test(k));
      h.push(["x-forwarded-user", user], ["x-forwarded-for", "10.9.9.9"], ["x-forwarded-proto", "https"], ["x-forwarded-host", "gateway.example.test"]);
      up.write("GET " + req.url + " HTTP/1.1\r\n" + h.map(([k, v]) => k + ": " + v).join("\r\n") + "\r\n\r\n");
      if (head?.length) up.write(head);
      up.pipe(socket); socket.pipe(up);
    });
    up.on("error", () => socket.destroy()); socket.on("error", () => up.destroy());
  });
  server.listen(Number(port), "127.0.0.1");
}
`

// restoreDriver is one request from an operator client built with the SDK's
// GatewayClient. It has no credential of its own: its identity comes from the
// proxy port it connects to, its device grant from pairing.
const restoreDriver = `import { GatewayClient } from "/app/dist/plugin-sdk/gateway-runtime.js";
const [port, method, params] = process.argv.slice(2);
const out = (o) => { process.stdout.write(JSON.stringify(o) + "\n"); process.exit(0); };
const timer = setTimeout(() => out({ phase: "connect", ok: false, message: "timeout" }), 60000);
const client = new GatewayClient({
  url: "ws://127.0.0.1:" + port, role: "operator", scopes: ["operator.read", "operator.write"],
  clientName: "openclaw-tui", mode: "ui", requestTimeoutMs: 300000,
  onHelloOk: async (hello) => {
    clearTimeout(timer);
    const scopes = hello?.auth?.scopes ?? null;
    try { out({ phase: "request", ok: true, scopes, res: await client.request(method, JSON.parse(params || "{}"), { expectFinal: true, timeoutMs: 300000 }) }); }
    catch (e) { out({ phase: "request", ok: false, scopes, code: e?.gatewayCode ?? e?.code, message: String(e?.message ?? e), details: e?.details }); }
  },
  onConnectError: (e) => out({ phase: "connect", ok: false, code: e?.gatewayCode ?? e?.code, message: String(e?.message ?? e), details: e?.details }),
  onClose: (code, reason) => out({ phase: "close", ok: false, message: String(reason) }),
});
client.start();
`

const (
	adminPort    = "18801" // admin@example.test: operator.admin through identityScopes only
	readerPort   = "18802" // reader@example.test: operator.read and operator.write
	strangerPort = "18803" // stranger@example.test: not an allowed proxy user
	closedPort   = "18899" // nothing listens here
	localSecret  = "local-synthetic"
)

// as is one request as the user behind port, through the SDK client.
func (g *gateway) as(port, method string, params any) reply {
	g.t.Helper()
	p, err := json.Marshal(params)
	if err != nil {
		g.t.Fatal(err)
	}
	cmd := g.exec("bash", "-c", gatewayEnv+`OPENCLAW_STATE_DIR=/tmp/clients/`+port+` HOME=/tmp/clients/`+port+` node /tmp/restore-driver.mjs `+port+` `+method+` "$(cat)" 2>&1 | tail -1`)
	cmd.Stdin = strings.NewReader(string(p))
	out, _ := cmd.Output()
	return g.decode(method, out)
}

// restartGateway stops the gateway and starts it with the current config.
// ready is a script that succeeds once the new gateway answers.
func (g *gateway) restartGateway(ready string) {
	g.t.Helper()
	// [d]ist keeps pkill from matching the shell running it.
	g.sh(gatewayEnv + `pkill -f "[d]ist/index.js gateway run" || true
for _ in $(seq 1 30); do pgrep -f "[d]ist/index.js gateway run" >/dev/null || break; sleep 1; done
nohup node dist/index.js gateway run --allow-unconfigured > /tmp/gateway.log 2>&1 &
for _ in $(seq 1 120); do ` + ready + ` >/dev/null 2>&1 && exit 0; sleep 1; done
grep -a -i "error\|failed" /tmp/gateway.log | tail -5 >&2; exit 1`)
}

// editConfig applies a JavaScript statement to the gateway's config object c.
func (g *gateway) editConfig(statement string) {
	g.t.Helper()
	g.sh(gatewayEnv + `node -e 'const fs=require("fs"),p=process.env.OPENCLAW_CONFIG_PATH,c=JSON.parse(fs.readFileSync(p));` + statement + `;fs.writeFileSync(p,JSON.stringify(c,null,2))'`)
}

func (g *gateway) worktreeRecord(id string) reply {
	g.t.Helper()
	list := g.as(adminPort, "worktrees.list", map[string]any{})
	if !list.ok() {
		g.t.Fatalf("worktrees.list: %v", list)
	}
	records, _ := list.res()["worktrees"].([]any)
	for _, r := range records {
		if rec := reply(r.(map[string]any)); rec.str("id") == id {
			return rec
		}
	}
	return nil
}

func (r reply) ok() bool      { ok, _ := r["ok"].(bool); return ok }
func (r reply) res() reply    { m, _ := r["res"].(map[string]any); return reply(m) }
func (r reply) evicted() bool { _, removed := r["removedAt"].(float64); return removed }
func (r reply) text() string  { b, _ := json.Marshal(r); return string(b) }
func (r reply) code() string  { return r.str("code") + " " + r.str("details", "code") }

func TestCheckoutRestoreUsesTheRequestersGatewayIdentity(t *testing.T) {
	g := startGateway(t, gatewayOptions{repos: syntheticRepos, plugin: true})

	// Contain the gateway: no network but loopback for the rest of the test.
	nets, err := exec.Command("docker", "inspect", "-f", "{{range $k, $v := .NetworkSettings.Networks}}{{$k}} {{end}}", g.container).Output()
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range strings.Fields(string(nets)) {
		if out, err := exec.Command("docker", "network", "disconnect", "-f", n, g.container).CombinedOutput(); err != nil {
			t.Fatalf("docker network disconnect %s: %v\n%s", n, err, out)
		}
	}
	contained := func() {
		t.Helper()
		if ifaces := g.sh("ls /sys/class/net"); ifaces != "lo" {
			t.Fatalf("the gateway must reach only loopback, has: %q", ifaces)
		}
	}
	contained()

	// OpenClaw 2026.10.2 installs this plugin only with explicit consent to the
	// capabilities it declares (gateway method dispatch under the request).
	g.sh(gatewayEnv + `node dist/index.js plugins install -l /home/node/wtc-plugin --force --accept-capabilities >/dev/null 2>&1`)
	g.restartGateway(`node dist/index.js gateway call health --token t --json`)

	// A synthetic collection: api with saved edits, web untouched.
	const worktree = "494-restore"
	for _, repo := range []string{"api", "web"} {
		g.call("projects.register", map[string]any{"path": "/home/node/repos/" + repo, "name": repo})
	}
	parent := g.call("sessions.create", map[string]any{"agentId": "dev", "label": worktree}).str("key")
	keys, ids, paths := map[string]string{}, map[string]string{}, map[string]string{}
	for _, repo := range []string{"api", "web"} {
		// What wtc add-repo's spawn intent creates. (The shared createFromIntent
		// helper also forwards a field this host's sessions.create rejects.)
		created := g.call("sessions.create", map[string]any{"agentId": "dev", "parentSessionKey": parent, "label": worktree + " " + repo,
			"projectId": repo, "worktree": true, "worktreeName": worktree})
		keys[repo] = created.str("key")
		ids[repo] = created.str("entry", "worktree", "id")
		if keys[repo] == "" || ids[repo] == "" {
			t.Fatalf("%s session with a managed worktree: %v", repo, created)
		}
		// The native record says where the checkout is.
		list := g.call("worktrees.list", map[string]any{})
		records, _ := list["worktrees"].([]any)
		for _, r := range records {
			if rec := reply(r.(map[string]any)); rec.str("id") == ids[repo] {
				paths[repo] = rec.str("path")
			}
		}
		if paths[repo] == "" {
			t.Fatalf("%s: no native worktree record %s: %v", repo, ids[repo], list)
		}
	}
	g.sh("cd " + paths["api"] + ` && echo one > one.txt && git add one.txt && git commit -qm "One" && echo edited >> README.md && echo stray > stray.txt`)
	for _, repo := range []string{"api", "web"} {
		if r := g.call("worktrees.remove", map[string]any{"id": ids[repo]}); r["removed"] != true {
			t.Fatalf("evict %s: %v", repo, r)
		}
	}

	// Trusted-proxy authentication with synthetic identities. A local password
	// exists only while client devices are paired; it is removed before any
	// restore, so the gateway host holds no credential a CLI could borrow.
	g.put("/tmp/restore-proxy.mjs", []byte(restoreProxy))
	g.put("/tmp/restore-driver.mjs", []byte(restoreDriver))
	g.editConfig(`c.gateway.trustedProxies=["127.0.0.1"];c.gateway.auth={mode:"trusted-proxy",password:"` + localSecret + `",` +
		`trustedProxy:{userHeader:"x-forwarded-user",requiredHeaders:["x-forwarded-proto"],allowUsers:["admin@example.test","reader@example.test"],allowLoopback:true},` +
		`identityScopes:{"admin@example.test":["operator.admin","operator.read","operator.write"],"reader@example.test":["operator.read","operator.write"]}}`)
	g.restartGateway(`node dist/index.js gateway call health --password ` + localSecret + ` --json`)
	g.sh(`nohup node /tmp/restore-proxy.mjs 18789 ` + adminPort + `=admin@example.test ` + readerPort + `=reader@example.test ` + strangerPort + `=stranger@example.test > /tmp/restore-proxy.log 2>&1 &
sleep 1`)
	for _, port := range []string{adminPort, readerPort} {
		if r := g.as(port, "health", map[string]any{}); r.ok() || !strings.Contains(r.code(), "PAIRING") && !strings.Contains(r.code(), "NOT_PAIRED") {
			t.Fatalf("a new synthetic device asks to be paired: %v", r)
		}
	}
	g.sh(gatewayEnv + `for id in $(node dist/index.js devices list --password ` + localSecret + ` --json 2>/dev/null | node -e 'const t=require("fs").readFileSync(0,"utf8");const j=JSON.parse(t.slice(t.indexOf("{")));for(const r of j.pending||[])console.log(r.requestId)'); do
  node dist/index.js devices approve "$id" --password ` + localSecret + ` >/dev/null; done`)
	g.editConfig(`delete c.gateway.auth.password`)
	g.restartGateway(`OPENCLAW_STATE_DIR=/tmp/clients/` + adminPort + ` HOME=/tmp/clients/` + adminPort + ` node /tmp/restore-driver.mjs ` + adminPort + ` health '{}' | grep -q '"ok":true'`)
	if strings.Contains(g.sh(gatewayEnv+`cat "$OPENCLAW_CONFIG_PATH"`), localSecret) {
		t.Fatal("the gateway host must hold no shared credential during restore")
	}
	contained()

	scopes := func(port string) string {
		r := g.as(port, "health", map[string]any{})
		b, _ := json.Marshal(r["scopes"])
		return string(b)
	}
	if s := scopes(adminPort); !strings.Contains(s, "operator.admin") {
		t.Fatalf("admin identity scopes: %s", s)
	}
	if s := scopes(readerPort); strings.Contains(s, "operator.admin") || !strings.Contains(s, "operator.write") {
		t.Fatalf("reader identity scopes: %s", s)
	}

	view := func() map[string]reply {
		t.Helper()
		v := g.as(adminPort, "plugins.sessionAction", map[string]any{"pluginId": "wtc", "actionId": "collection", "sessionKey": parent, "agentId": "dev"})
		result, _ := v.res()["result"].(map[string]any)
		if result == nil {
			t.Fatalf("collection view: %v", v)
		}
		return repoRows(reply(result))
	}
	rows := view()
	for _, repo := range []string{"api", "web"} {
		if rows[repo].str("error") != "checkout unavailable" || rows[repo].str("recovery", "state") != "restorable" {
			t.Fatalf("%s is evicted with a recorded snapshot and this gateway can restore it: %v", repo, rows[repo])
		}
	}
	restore := func(port, repo, key string) reply {
		return g.as(port, "wtc.checkout.restore", map[string]any{"sessionKey": parent, "agentId": "dev", "ownerSessionKey": keys[repo], "key": key})
	}
	stillEvicted := func(repo string) {
		t.Helper()
		if rec := g.worktreeRecord(ids[repo]); rec == nil || !rec.evicted() {
			t.Fatalf("%s must still be evicted: %v", repo, rec)
		}
		if out, _ := g.bash("test -e " + paths[repo] + " && echo present"); strings.Contains(out, "present") {
			t.Fatalf("%s destination must be untouched", repo)
		}
	}
	apiKey, webKey := rows["api"].str("recovery", "key"), rows["web"].str("recovery", "key")

	t.Run("a session without gateway admin is refused by the host and nothing changes", func(t *testing.T) {
		r := restore(readerPort, "api", apiKey)
		if r.ok() || !strings.Contains(r.code(), "MISSING_SCOPE") || !strings.Contains(r.str("message"), "operator.admin") {
			t.Fatalf("reader: %v", r.text())
		}
		stillEvicted("api")
	})

	t.Run("a connection whose authentication fails never reaches restore", func(t *testing.T) {
		r := restore(strangerPort, "api", apiKey)
		if r.str("phase") != "connect" || !strings.Contains(r.str("message"), "unauthorized") {
			t.Fatalf("unauthenticated connection: %v", r.text())
		}
		stillEvicted("api")
	})

	t.Run("a refused gateway connection reaches nothing", func(t *testing.T) {
		r := restore(closedPort, "api", apiKey)
		if r.str("phase") != "connect" || !strings.Contains(r.text(), "ECONNREFUSED") {
			t.Fatalf("refused connection: %v", r.text())
		}
		stillEvicted("api")
	})

	t.Run("a changed target is refused before native restore", func(t *testing.T) {
		r := restore(adminPort, "api", "stale-recovery-key").res()
		if r.str("status") != "refused" || r.str("kind") != "changed" || r.str("mutation") != "none" {
			t.Fatalf("stale key: %v", r.text())
		}
		stillEvicted("api")
	})

	t.Run("a tab loaded before this change is told to reload and runs no host CLI", func(t *testing.T) {
		r := g.as(adminPort, "plugins.sessionAction", map[string]any{"pluginId": "wtc", "actionId": "restore-checkout", "sessionKey": parent, "agentId": "dev", "payload": map[string]any{"ownerSessionKey": keys["api"], "key": apiKey}})
		if !strings.Contains(r.text(), "Reload the Collection view") {
			t.Fatalf("stale action: %v", r.text())
		}
		stillEvicted("api")
	})

	// Restore all, one repository after another: web's snapshot expired, api's did not.
	snapshot := g.worktreeRecord(ids["web"]).str("snapshotRef")
	g.sh("git -C /home/node/repos/web update-ref -d " + snapshot)

	t.Run("an expired snapshot is reported as unavailable and nothing changes", func(t *testing.T) {
		r := restore(adminPort, "web", webKey).res()
		if r.str("status") != "failed" || r.str("kind") != "snapshot" || r.str("mutation") != "none" {
			t.Fatalf("expired snapshot: %v", r.text())
		}
		stillEvicted("web")
	})

	t.Run("an authorized restore keeps the session binding and saved edits", func(t *testing.T) {
		r := restore(adminPort, "api", apiKey).res()
		if r.str("status") != "restored" || r.str("mutation") != "applied" {
			t.Fatalf("admin restore: %v\n%s", r.text(), g.sh("grep -a -i 'error\\|restore' /tmp/gateway.log | tail -5"))
		}
		rec := g.worktreeRecord(ids["api"])
		if rec.evicted() || rec.str("ownerId") != keys["api"] || rec.str("path") != paths["api"] || rec.str("branch") != Branch(worktree) {
			t.Fatalf("restored record: %v", rec.text())
		}
		if got := g.sh("cd " + paths["api"] + ` && git branch --show-current && git log -1 --format=%s && git status --short`); got != Branch(worktree)+"\nOne\n M README.md\n?? stray.txt" {
			t.Fatalf("saved edits after restore:\n%s", got)
		}
		row := view()["api"]
		if row.str("error") != "" || row.str("sessionKey") != keys["api"] || row["dirty"] != float64(2) || row["untracked"] != float64(1) || row["ahead"] != float64(1) {
			t.Fatalf("the original session shows its restored checkout: %v", row)
		}
		if again := restore(adminPort, "api", apiKey).res(); again.str("status") != "refused" {
			t.Fatalf("a restored checkout is never restored over: %v", again.text())
		}
	})

	t.Run("a partial batch keeps each repository's own outcome", func(t *testing.T) {
		rows := view()
		if rows["api"].str("error") != "" || rows["web"].str("error") != "checkout unavailable" {
			t.Fatalf("after the batch: api %v / web %v", rows["api"], rows["web"])
		}
		stillEvicted("web")
	})

	contained()
}
