// The gateway's git service for sandboxes, without a gateway.
import { test } from "node:test";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { mkdtempSync, rmSync } from "node:fs";
import os from "node:os";
import path from "node:path";
import { parseCgiHead, projectionWorktreeId, startSandboxGit, uploadPackConfig } from "../sandbox-git.js";

test("a sandbox's workspace names the managed worktree it projects", () => {
  assert.equal(projectionWorktreeId("/home/node/.openclaw/worktree-projections/ee0ae31d-cf9a/workspace"), "ee0ae31d-cf9a");
  assert.equal(projectionWorktreeId("/home/node/.openclaw/workspaces/dev"), null);
  assert.equal(projectionWorktreeId("/x/worktree-projections/../workspace"), null);
  assert.equal(projectionWorktreeId(undefined), null);
});

test("upload-pack shows origin, tags and the collection's branches, and nothing else", () => {
  const dir = mkdtempSync(path.join(os.tmpdir(), "wtc-sandbox-git-"));
  try {
    const git = (...args) => execFileSync("git", ["-C", dir, ...args], { encoding: "utf8", env: { ...process.env, GIT_CONFIG_GLOBAL: "/dev/null" } });
    git("init", "-q", "-b", "main");
    git("-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "--allow-empty", "-m", "init");
    for (const ref of ["refs/heads/openclaw/ours", "refs/heads/openclaw/ours--a", "refs/heads/openclaw/theirs", "refs/remotes/origin/main", "refs/tags/v1", "refs/openclaw/snapshots/x"]) {
      git("update-ref", ref, "HEAD");
    }
    const env = { ...process.env, GIT_CONFIG_GLOBAL: "/dev/null", ...uploadPackConfig(new Set(["openclaw/ours"])) };
    // What http-backend runs. (A local ls-remote would drop the config.)
    const shown = execFileSync("git", ["upload-pack", "--advertise-refs", "--stateless-rpc", dir], { encoding: "utf8", env })
      .split("\n").map((l) => l.slice(4).split("\0")[0].split(" ")[1]).filter(Boolean).sort();
    assert.deepEqual(shown, ["refs/heads/openclaw/ours", "refs/remotes/origin/main", "refs/tags/v1"]);
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

test("a CGI response's status, headers and body", () => {
  const head = parseCgiHead(Buffer.from("Status: 403 Forbidden\r\nContent-Type: text/plain\r\n\r\nno"));
  assert.deepEqual([head.status, head.headers["Content-Type"]], [403, "text/plain"]);
  assert.equal(parseCgiHead(Buffer.from("Content-Type: x\r\n")), null);
  const plain = parseCgiHead(Buffer.from("Content-Type: x\n\nbody"));
  assert.deepEqual([plain.status, plain.bodyStart], [200, 17]);
});

test("a connection from no sandbox container is refused", async () => {
  const asked = [];
  const server = startSandboxGit({
    host: "127.0.0.1",
    port: 0,
    run: async (argv) => {
      asked.push(argv.join(" "));
      return { code: 0, stdout: JSON.stringify({ containers: [] }), stderr: "" };
    },
    openclaw: ["openclaw"],
    listSessionEntries: () => [],
    collectionOf: () => ({ children: [] }),
    worktreeOf: () => null,
  });
  try {
    await new Promise((resolve) => server.once("listening", resolve));
    const { port } = server.address();
    const r = await fetch(`http://127.0.0.1:${port}/api/info/refs?service=git-upload-pack`);
    assert.equal(r.status, 403);
    assert.match((await r.json()).error, /no sandbox container/);
    assert.ok(asked.every((a) => a === "openclaw sandbox list --json"), asked.join("; "));
  } finally {
    server.close();
  }
});

test("a sandbox container is served its session, cannot push, and delivers its branch", async () => {
  const asked = [];
  let inspected = 0;
  const server = startSandboxGit({
    host: "127.0.0.1",
    port: 0,
    run: async (argv, options) => {
      asked.push({ argv: argv.join(" "), cwd: options?.cwd });
      if (argv[1] === "sandbox") {
        return { code: 0, stdout: JSON.stringify({ containers: [{ containerName: "sbx-1", backendId: "docker", running: true, sessionKey: "agent:dev:required-session:x", workspaceDir: "/state/worktree-projections/w1/workspace" }] }), stderr: "" };
      }
      if (argv[0] === "docker") {
        inspected += 1;
        return { code: 0, stdout: "/sbx-1 127.0.0.1\n", stderr: "" };
      }
      if (argv[2] === "sandbox-session") return { code: 0, stdout: JSON.stringify({ branch: "openclaw/x", base: "main", head: "h" }), stderr: "" };
      if (argv[2] === "sandbox-deliver") return { code: 0, stdout: JSON.stringify({ branch: "openclaw/x", head: "c", commits: 1, merges: 0 }), stderr: "" };
      return { code: 1, stdout: "", stderr: "unexpected" };
    },
    openclaw: ["openclaw"],
    listSessionEntries: async ({ agentId }) => (agentId === "dev" ? [
      { sessionKey: "s1", entry: { worktree: { id: "w1", branch: "openclaw/x" }, projectId: "api", workspaceDir: "/wt/api" } },
      { sessionKey: "s2", entry: { worktree: { id: "w2", branch: "openclaw/y" }, projectId: "api", workspaceDir: "/wt/other" } },
    ] : []),
    collectionOf: async () => ({ children: [] }),
    worktreeOf: (entry) => entry?.workspaceDir ?? null,
  });
  try {
    await new Promise((resolve) => server.once("listening", resolve));
    const base = `http://127.0.0.1:${server.address().port}`;
    const session = await (await fetch(`${base}/wtc/session`)).json();
    assert.deepEqual([session.repo, session.branch, session.repos], ["api", "openclaw/x", ["api"]]);
    assert.equal(asked.find((a) => a.argv.includes("sandbox-session")).cwd, "/wt/api");

    const push = await fetch(`${base}/api/info/refs?service=git-receive-pack`);
    assert.equal(push.status, 403);
    assert.match((await push.json()).error, /read-only/);
    assert.equal((await fetch(`${base}/web/info/refs?service=git-upload-pack`)).status, 404);

    const before = inspected;
    const delivered = await fetch(`${base}/wtc/deliver?commit=${"a".repeat(40)}`, { method: "POST", body: Buffer.from("bundle") });
    assert.equal(delivered.status, 200);
    const deliver = asked.find((a) => a.argv.includes("sandbox-deliver"));
    assert.match(deliver.argv, new RegExp(`--commit ${"a".repeat(40)} --bundle \\S+branch\\.bundle$`));
    assert.equal(deliver.cwd, "/wt/api");
    assert.ok(inspected > before, "a delivery did not ask Docker again who the address is");
  } finally {
    server.close();
  }
});

// Discovery must not turn possession of a gateway URL into session authority.
test("project discovery refuses an unknown or ambiguous worktree owner", async (t) => {
  const owner = { sessionKey: "s1", entry: { worktree: { id: "w1" }, projectId: "api", workspaceDir: "/fixture/api" } };
  for (const [name, entries] of [["unknown", []], ["ambiguous", [owner, { ...owner, sessionKey: "s2" }]]]) {
    await t.test(name, async () => {
      const commands = [];
      const server = startSandboxGit({
        host: "127.0.0.1", port: 0,
        run: async (argv) => {
          commands.push(argv);
          if (argv[0] === "openclaw") return { code: 0, stdout: JSON.stringify({ containers: [{ containerName: "sbx-1", backendId: "docker", running: true, sessionKey: "agent:dev:session:x", workspaceDir: "/fixture/worktree-projections/w1/workspace" }] }), stderr: "" };
          if (argv[0] === "docker") return { code: 0, stdout: "/sbx-1 127.0.0.1\n", stderr: "" };
          throw new Error("unauthorized session reached gateway metadata");
        },
        openclaw: ["openclaw"],
        listSessionEntries: () => entries,
        collectionOf: () => { throw new Error("no unique session owner"); },
        worktreeOf: (entry) => entry?.workspaceDir,
      });
      try {
        await new Promise((resolve) => server.once("listening", resolve));
        const response = await fetch(`http://127.0.0.1:${server.address().port}/wtc/projects`);
        assert.equal(response.status, 403);
        assert.deepEqual(await response.json(), { error: "no single session owns this sandbox's worktree" });
        assert.deepEqual(commands.map((argv) => argv[0]), ["openclaw", "docker"]);
      } finally {
        server.close();
      }
    });
  }
});

function projectService(t, options = {}) {
  const commands = [];
  let agent = "dev";
  const server = startSandboxGit({
    host: "127.0.0.1", port: 0,
    openclaw: ["openclaw"],
    run: async (argv, opts) => {
      commands.push({ argv, opts });
      if (argv[0] === "openclaw") return { code: 0, stdout: JSON.stringify({ containers: [{ containerName: "sbx", backendId: "docker", running: true, sessionKey: `agent:${agent}:session:x`, workspaceDir: "/fixture/worktree-projections/w1/workspace" }] }), stderr: "" };
      if (argv[0] === "docker") return { code: 0, stdout: "/sbx 127.0.0.1\n", stderr: "" };
      if (argv[2] === "sandbox-projects") return options.metadata ?? { code: 0, stdout: JSON.stringify({ projects: [{ name: "platform", harness: "https://github.com/example/harness.git", ref: "main", repos: [{ name: "web", remote: "https://github.com/example/web.git" }] }] }), stderr: "" };
      throw new Error("unexpected command");
    },
    listSessionEntries: () => [{ sessionKey: "s1", entry: { worktree: { id: "w1" }, projectId: "api", workspaceDir: "/fixture/api" } }],
    collectionOf: () => ({ children: [] }),
    worktreeOf: (entry) => entry?.workspaceDir,
    ...options,
  });
  t.after(() => server.close());
  return {
    server, commands,
    setAgent: (value) => { agent = value; },
    url: async () => {
      await new Promise((resolve) => server.once("listening", resolve));
      return `http://127.0.0.1:${server.address().port}`;
    },
  };
}

test("project discovery is closed by default and invalid policies fail closed", async (t) => {
  for (const projectsByAgent of [undefined, {}, { dev: [] }, { dev: ["*"] }, { dev: ["../platform"] }, { dev: "platform" }, { dev: ["none"] }, { other: ["platform"] }, Object.create({ dev: ["platform"] })]) {
    await t.test(JSON.stringify(projectsByAgent), async (t) => {
      const fixture = projectService(t, { projectsByAgent });
      const response = await fetch(`${await fixture.url()}/wtc/projects?agentId=other&project=platform`, { headers: { "x-agent-id": "other" } });
      assert.equal(response.status, 403);
      assert.deepEqual(await response.json(), { error: "project discovery is not enabled for this agent" });
      assert.equal(fixture.commands.filter(({ argv }) => argv[0] === "wtc").length, 0);
    });
  }
});

test("discovery uses only authenticated agent policy, stays read-only, and refreshes identity", async (t) => {
  const fixture = projectService(t, { projectsByAgent: { dev: ["platform"], admin: ["hidden"] } });
  const base = await fixture.url();
  const response = await fetch(`${base}/wtc/projects?agentId=admin&project=hidden&path=/fixture/secret`, { headers: { "x-agent-id": "admin" } });
  assert.equal(response.status, 200);
  assert.equal(response.headers.get("cache-control"), "no-store");
  assert.deepEqual((await response.json()).projects.map((p) => p.name), ["platform"]);
  const load = fixture.commands.find(({ argv }) => argv[0] === "wtc");
  assert.deepEqual(load.argv, ["wtc", "openclaw", "sandbox-projects", "--project", "platform"]);
  assert.equal(load.opts.cwd, "/fixture/api");
  assert.deepEqual(load.opts.env, { WTC_GATEWAY_GIT: "" });
  assert.equal((await fetch(`${base}/web/info/refs?service=git-upload-pack`)).status, 404, "metadata granted Git access outside the collection");
  assert.equal((await fetch(`${base}/wtc/projects`, { method: "POST" })).status, 404);
  fixture.setAgent("unlisted");
  assert.equal((await fetch(`${base}/wtc/projects`)).status, 403, "discovery reused the previous container identity");
  assert.equal(fixture.commands.filter(({ argv }) => argv[0] === "wtc").length, 1);
});

test("project command failures and malformed or unallowed results disclose no diagnostics", async (t) => {
  for (const metadata of [
    { code: 1, stdout: "", stderr: "credential=secret /fixture/private-config" },
    { code: 0, stdout: "not JSON /fixture/private-config", stderr: "" },
    { code: 0, stdout: '{"projects":[{"name":"hidden"}]}', stderr: "" },
    { code: 0, stdout: '{"projects":null}', stderr: "" },
  ]) {
    await t.test(metadata.stdout, async (t) => {
      const fixture = projectService(t, { projectsByAgent: { dev: ["platform"] }, metadata });
      const response = await fetch(`${await fixture.url()}/wtc/projects`);
      assert.equal(response.status, 503);
      assert.deepEqual(await response.json(), { error: "gateway project metadata unavailable" });
    });
  }
});

test("discovery refuses a container without an authenticated agent identity", async (t) => {
  const fixture = projectService(t, { projectsByAgent: { "": ["platform"] } });
  fixture.setAgent("");
  const response = await fetch(`${await fixture.url()}/wtc/projects?agentId=dev`);
  assert.equal(response.status, 403);
  assert.equal(fixture.commands.filter(({ argv }) => argv[0] === "wtc").length, 0);
});

test("discovery hides identity-provider diagnostics", async (t) => {
  const fixture = projectService(t, { projectsByAgent: { dev: ["platform"] }, listSessionEntries: async () => { throw new Error("credential=secret /fixture/private-config"); } });
  const response = await fetch(`${await fixture.url()}/wtc/projects`);
  assert.equal(response.status, 503);
  assert.deepEqual(await response.json(), { error: "gateway project metadata unavailable" });
});

test("failed or ambiguous container lookup cannot authorize discovery", async (t) => {
  for (const mode of ["list failure", "inspect failure", "duplicate address"]) {
    await t.test(mode, async (t) => {
      const containers = ["one", "two"].map((containerName) => ({ containerName, backendId: "docker", running: true, sessionKey: "agent:dev:x", workspaceDir: "/fixture/worktree-projections/w1/workspace" }));
      const fixture = projectService(t, { projectsByAgent: { dev: ["platform"] }, run: async (argv) => {
        if (argv[0] === "openclaw") return { code: mode === "list failure" ? 1 : 0, stdout: JSON.stringify({ containers }), stderr: "" };
        if (argv[0] === "docker") return { code: mode === "inspect failure" ? 1 : 0, stdout: "/one 127.0.0.1\n/two 127.0.0.1\n", stderr: "" };
        throw new Error("identity failure reached metadata command");
      } });
      const response = await fetch(`${await fixture.url()}/wtc/projects`);
      assert.equal(response.status, 403);
      if (mode === "duplicate address") {
        assert.deepEqual(await response.json(), { error: "this address belongs to more than one sandbox container" });
      } else {
        assert.deepEqual(await response.json(), { error: "this address is no sandbox container of the gateway's" });
      }
    });
  }
});
