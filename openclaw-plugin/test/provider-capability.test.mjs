import { test } from "node:test";
import assert from "node:assert/strict";
import { chmodSync, mkdirSync, mkdtempSync, rmSync, symlinkSync, writeFileSync } from "node:fs";
import os from "node:os";
import path from "node:path";
import plugin from "../index.js";

// Exercise registered handlers without executing clients, services or providers.
test("gateway provider lookup rejects unusable candidates and rechecks setup", async () => {
  const root = mkdtempSync(path.join(os.tmpdir(), "taba-capability-"));
  const savedPath = process.env.PATH;
  const bin = path.join(root, "bin"), local = path.join(root, "collection-bin");
  mkdirSync(bin); mkdirSync(local);
  const candidate = path.join(bin, "cloudflared");
  const actions = new Map(), commands = [];
  const entry = { parentSessionKey: "task", projectId: "widget", workspaceDir: path.join(root, "worktree"), worktree: { branch: "openclaw/task" } };
  plugin.register({
    pluginConfig: { pullRequestEvents: "off", pullRequestPollMinutes: 0 },
    runtime: {
      state: { resolveStateDir: () => root },
      agent: { session: {
        listSessionEntriesAsync: async () => [{ sessionKey: "task", entry: {} }, { sessionKey: "repo", entry }],
        getSessionEntryAsync: async () => entry,
      } },
      system: { runCommandWithTimeout: async (argv) => {
        commands.push(argv);
        if (argv[0] === "wtc" && argv[1] === "--version") return { code: 0, stdout: "wtc version dev" };
        if (argv[0] === "wtc" && argv[1] === "status") return { code: 0, stdout: "{}" };
        return { code: 1, stdout: "" };
      } },
    },
    session: { controls: { registerSessionAction: a => actions.set(a.id, a), registerControlUiDescriptor() {} } },
    registerGatewayMethod() {}, registerHttpRoute() {}, on() {}, logger: {},
  });
  const read = async () => (await actions.get("collection").handler({ agentId: "main", sessionKey: "task" })).result;
  const setup = () => actions.get("svc").handler({ agentId: "main", sessionKey: "task", payload: { op: "tunnel", group: "widget/main", provider: "cloudflare", add: true } });
  const expect = async (state) => {
    const result = await read();
    assert.equal(result.tunnelProviders.includes("cloudflare"), state === "available");
    assert.deepEqual(result.tunnelProviderAvailability.cloudflare, { state, scope: "gateway-path" });
    const click = await setup();
    assert.equal(click.ok, true);
    assert.equal(click.result.setup, true);
    assert.deepEqual(click.result.availability, { state, scope: "gateway-path" });
    assert.match(click.result.message, /collection.*runtime/i);
    assert.doesNotMatch(click.result.message, /install first|must install|client is not installed/i);
  };
  try {
    process.env.PATH = bin;
    // Fail-before: mere filesystem existence must not advertise a provider.
    writeFileSync(candidate, "synthetic fixture\n", { mode: 0o600 });
    assert.equal((await read()).tunnelProviders.includes("cloudflare"), false);
    await expect("unusable");
    chmodSync(candidate, 0o700); await expect("available");
    rmSync(candidate); await expect("missing");
    mkdirSync(candidate); await expect("unusable"); rmSync(candidate, { recursive: true });
    symlinkSync(path.join(root, "absent"), candidate); await expect("unusable"); rmSync(candidate);
    const target = path.join(root, "client"); writeFileSync(target, "fixture\n", { mode: 0o700 });
    symlinkSync(target, candidate); await expect("available"); chmodSync(target, 0o600); await expect("unusable"); rmSync(candidate);
    symlinkSync(candidate, candidate); await expect("unknown");
    writeFileSync(path.join(local, "cloudflared"), "fixture\n", { mode: 0o700 });
    process.env.PATH = bin + path.delimiter + local; await expect("available");
    process.env.PATH = bin; rmSync(candidate);
    // A later eligible candidate wins over an unusable or failed earlier lookup.
    writeFileSync(path.join(local, "cloudflared"), "fixture\n", { mode: 0o700 });
    writeFileSync(candidate, "fixture\n", { mode: 0o600 });
    process.env.PATH = bin + path.delimiter + local; await expect("available");
    rmSync(candidate);
    process.env.PATH = bin; await expect("missing"); // collection-local presence is not gateway evidence
    delete process.env.PATH; await expect("unknown");
    process.env.PATH = ""; await expect("unknown");
    process.env.PATH = bin;
    await read(); writeFileSync(candidate, "fixture\n", { mode: 0o700 });
    assert.equal((await setup()).result.availability.state, "available", "click must recheck stale state");
    assert.ok(commands.every(a => !["cloudflared", "ngrok", "tailscale", "devtunnel"].includes(a[0]) && a[1] !== "svc"), "lookup and setup never launch clients or services");
  } finally {
    if (savedPath === undefined) delete process.env.PATH; else process.env.PATH = savedPath;
    rmSync(root, { recursive: true, force: true });
  }
});
