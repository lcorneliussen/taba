import { test } from "node:test";
import assert from "node:assert/strict";
import { readFile, mkdtemp, writeFile, rm, rename } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, resolve, dirname } from "node:path";
import { fileURLToPath } from "node:url";
import { spawnSync } from "node:child_process";
import { runHost, createSdkHost, validateInput } from "./host.mjs";
import bufferUtil from "bufferutil/fallback.js";
import validUTF8 from "utf-8-validate/fallback.js";

const root = dirname(fileURLToPath(import.meta.url));
const token = () => ["header", Buffer.from(JSON.stringify({ clusterId: "use2", tunnelId: "synthetic-tunnel", scp: "host", exp: Math.floor(Date.now() / 1000) + 3600 })).toString("base64url"), "signature"].join(".");
const input = () => ({ schema: 1, clusterId: "use2", tunnelId: "synthetic-tunnel", portNumber: 3000, hostId: "synthetic-owner", hostToken: token() });

test("upstream pure-JS websocket fallbacks retain framing and UTF-8 validation", () => {
  const payload = Buffer.from("synthetic websocket payload"), mask = Buffer.from([1, 2, 3, 4]);
  const framed = Buffer.alloc(payload.length);
  bufferUtil.mask(payload, mask, framed, 0, payload.length);
  assert.notDeepEqual(framed, payload);
  bufferUtil.unmask(framed, mask);
  assert.deepEqual(framed, payload);
  assert.equal(validUTF8(Buffer.from("valid 🐈")), true);
  assert.equal(validUTF8(Buffer.from([0xc0, 0x80])), false);
  assert.equal(validUTF8(Buffer.from([0xed, 0xa0, 0x80])), false);
});

test("invalid or incomplete inputs never construct a host", async () => {
  for (const change of [x => { x.clusterId = null; }, x => { x.clusterId = "../other"; }, x => { x.tunnelId = "../other"; }, x => { x.hostId = ""; }, x => { x.hostToken = ""; }, x => { x.portNumber = 0; }, x => { x.login = true; }]) {
    const x = input(); change(x);
    let called = false;
    await assert.rejects(runHost(x, { signal: new AbortController().signal, emit() {}, factory() { called = true; } }));
    assert.equal(called, false);
  }
  assert.equal(validateInput(input()).portNumber, 3000);
});

test("stop waits for owned disposal and never claims failed cleanup succeeded", async () => {
  for (const fail of [false, true]) {
    const controller = new AbortController(), events = [], calls = [];
    const promise = runHost(input(), {
      signal: controller.signal,
      emit(event) { events.push(event.event); if (event.event === "started") controller.abort(); },
      async factory() { return { async connect() { calls.push("connect"); }, disconnected: new Promise(() => {}), async dispose() { calls.push("dispose"); if (fail) throw Error("cleanup failed"); } }; },
    });
    if (fail) await assert.rejects(promise); else await promise;
    assert.deepEqual(calls, ["connect", "dispose"]);
    assert.deepEqual(events, fail ? ["started"] : ["started", "stopped"]);
  }
});

test("cancellation while connecting disposes without announcing readiness", async () => {
  const controller = new AbortController(); let disposed = 0;
  const promise = runHost(input(), { signal: controller.signal, emit() { assert.fail("not ready"); }, async factory() { return { connect() { queueMicrotask(() => controller.abort()); return new Promise(() => {}); }, async dispose() { disposed++; } }; } });
  await assert.rejects(promise);
  assert.equal(disposed, 1);
});

test("pinned SDK uses supplied host capability and injected HTTP/relay transports", async () => {
  const x = input(), requests = [], relays = [];
  const host = await createSdkHost(x, {
    async httpAdapter(config) {
      const u = new URL(config.url);
      assert.equal(u.hostname, "use2.rel.tunnels.api.visualstudio.com");
      assert.equal(config.headers.get ? config.headers.get("Authorization") : config.headers.Authorization, "tunnel " + x.hostToken);
      assert.match(u.pathname, /^\/tunnels\/synthetic-tunnel\/endpoints\//);
      assert.ok(["put", "delete"].includes(config.method));
      requests.push({ method: config.method, path: u.pathname });
      const endpoint = config.method === "put" ? JSON.parse(config.data) : {};
      if (config.method === "put") {
        assert.equal(endpoint.hostId, x.hostId);
        assert.equal(endpoint.hostPublicKeys.length, 1);
        assert.ok(Buffer.from(endpoint.hostPublicKeys[0], "base64").length > 64);
      }
      return { config, status: config.method === "delete" ? 204 : 200, statusText: "OK", headers: {}, data: { ...endpoint, hostRelayUri: "wss://use2-data.rel.tunnels.api.visualstudio.com/fake" } };
    },
    streamFactory: { async createRelayStream(uri, protocols, credential) { relays.push({ uri, protocols }); assert.equal(credential, x.hostToken); throw Error("synthetic relay rejection"); } },
  });
  await assert.rejects(host.connect(), /synthetic relay rejection/);
  await host.dispose();
  assert.equal(relays.length, 1);
  assert.ok(requests.some(r => r.method === "put"));
  assert.ok(requests.some(r => r.method === "delete"));
});

test("pinned SDK RSA uses supported Node crypto without the legacy package", async () => {
  const { SshAlgorithms } = await import("@microsoft/dev-tunnels-ssh");
  const algorithm = SshAlgorithms.publicKey.rsaWithSha256;
  const key = await algorithm.generateKeyPair(2048);
  try {
    const data = Buffer.from("synthetic RSA signing fixture");
    const signature = await algorithm.createSigner(key).sign(data);
    assert.equal(await algorithm.createVerifier(key).verify(data, signature), true);
    assert.equal(await algorithm.createVerifier(key).verify(Buffer.from("changed"), signature), false);
  } finally { key.dispose(); }
});

test("lock uses public integrity-pinned packages, with accepted direct versions", async () => {
  const lock = JSON.parse(await readFile(join(root, "package-lock.json"), "utf8"));
  for (const [name, pkg] of Object.entries(lock.packages)) {
    if (!name) continue;
    assert.ok(pkg.resolved.startsWith("https://registry.npmjs.org/"));
    assert.match(pkg.integrity, /^sha512-/);
  }
  for (const name of ["connections", "contracts", "management"]) assert.equal(lock.packages[`node_modules/@microsoft/dev-tunnels-${name}`].version, "1.3.62");
  for (const name of ["ssh", "ssh-tcp"]) assert.equal(lock.packages[`node_modules/@microsoft/dev-tunnels-${name}`].version, "3.12.42");
});

test("embedded bundle works without node_modules and redacts invalid input", async () => {
  const dir = await mkdtemp(join(tmpdir(), "taba-host-bundle-"));
  try {
    const asset = resolve(root, "../../internal/wtc/assets/devtunnel-host.cjs");
    const dest = join(dir, "host.cjs"); await writeFile(dest, await readFile(asset));
    const env = { HOME: dir, PATH: dirname(process.execPath), NODE_NO_WARNINGS: "1" };
    const check = spawnSync(process.execPath, [dest, "--check"], { cwd: dir, env, encoding: "utf8", timeout: 5000 });
    assert.equal(check.status, 0, check.stderr);
    assert.equal(JSON.parse(check.stdout).sdk, "1.3.62");
    const sdkSmoke = spawnSync(process.execPath, ["-e", `
      const assert = require('node:assert/strict');
      const Module = require('node:module');
      const builtins = new Set(Module.builtinModules.map(name => name.replace(/^node:/, '')));
      const load = Module._load;
      Module._load = function(name, ...args) {
        assert.ok(builtins.has(name.replace(/^node:/, '')) || name === process.argv[1], 'external module lookup forbidden: ' + name);
        return load.call(this, name, ...args);
      };
      for (const name of ['node:http', 'node:https']) require(name).request = () => { throw Error('real HTTP forbidden'); };
      require('node:net').connect = require('node:net').createConnection = () => { throw Error('real socket forbidden'); };
      globalThis.fetch = () => { throw Error('real fetch forbidden'); };
      const crypto = require('node:crypto');
      const generate = crypto.generateKeyPair;
      let nativeHostKeys = 0;
      crypto.generateKeyPair = function(type, options, callback) {
        assert.equal(type, 'ec');
        assert.equal(options.namedCurve, 'P-384');
        nativeHostKeys++;
        return generate.call(this, type, options, callback);
      };
      const { createSdkHost } = require(process.argv[1]);
      const fixture = JSON.parse(require('node:fs').readFileSync(0, 'utf8'));
      let setup = 0, relay = 0, cleanup = 0;
      (async () => {
        const host = await createSdkHost(fixture, {
          async httpAdapter(config) {
            const u = new URL(config.url);
            assert.equal(u.hostname, 'use2.rel.tunnels.api.visualstudio.com');
            assert.match(u.pathname, /^\\/tunnels\\/synthetic-tunnel\\/endpoints\\//);
            assert.equal(config.headers.get('Authorization'), 'tunnel ' + fixture.hostToken);
            assert.ok(['put', 'delete'].includes(config.method));
            const endpoint = config.method === 'put' ? JSON.parse(config.data) : {};
            if (config.method === 'put') {
              setup++;
              assert.equal(endpoint.hostId, fixture.hostId);
              assert.equal(endpoint.hostPublicKeys.length, 1);
              assert.ok(Buffer.from(endpoint.hostPublicKeys[0], 'base64').length > 64);
            } else cleanup++;
            return { config, status: config.method === 'delete' ? 204 : 200, statusText: 'OK', headers: {}, data: { ...endpoint, hostRelayUri: 'wss://use2-data.rel.tunnels.api.visualstudio.com/fake' } };
          },
          streamFactory: { async createRelayStream(uri, protocols, credential) {
            relay++; assert.equal(credential, fixture.hostToken); throw Error('synthetic relay rejection');
          } },
        });
        await assert.rejects(host.connect(), /synthetic relay rejection/);
        await host.dispose();
        assert.equal(nativeHostKeys, 1);
        assert.equal(setup, 1); assert.equal(relay, 1); assert.equal(cleanup, 1);
        process.stdout.write('bundled SDK host initialization verified\\n');
      })().catch(error => { console.error(error); process.exitCode = 1; });
    `, dest], { cwd: dir, env, input: JSON.stringify(input()), encoding: "utf8", timeout: 10000 });
    assert.equal(sdkSmoke.status, 0, sdkSmoke.stderr);
    assert.match(sdkSmoke.stdout, /bundled SDK host initialization verified/);
    const invalid = spawnSync(process.execPath, [dest], { cwd: dir, env, input: '{"hostToken":"synthetic-sensitive-value"}', encoding: "utf8", timeout: 5000 });
    assert.equal(invalid.status, 1);
    assert.equal(invalid.stdout, "");
    assert.ok(!invalid.stderr.includes("synthetic-sensitive-value"));
    const content = await readFile(dest, "utf8");
    assert.ok(!content.includes("sourceMappingURL="));
    assert.ok(!content.includes(root));
    const notices = await readFile(resolve(root, "../../internal/wtc/assets/devtunnel-host.NOTICES.txt"), "utf8");
    assert.match(notices, /agent-base@6\.0\.2/);
    assert.match(notices, /Copyright \(c\) 2013 Nathan Rajlich/);
    assert.match(notices, /Permission is hereby granted, free of charge/);
  } finally { await rm(dir, { recursive: true, force: true }); }
});

test("missing exact-version license text still prevents bundling", async () => {
  const readme = join(root, "node_modules/agent-base/README.md");
  const hidden = readme + ".license-guard-test";
  await rename(readme, hidden);
  try {
    const result = spawnSync(process.execPath, ["build.mjs", "--check"], { cwd: root, encoding: "utf8", timeout: 30000 });
    assert.notEqual(result.status, 0);
    assert.match(result.stderr, /missing third-party license: agent-base/);
  } finally { await rename(hidden, readme); }
});
