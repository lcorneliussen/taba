// The gateway's git service for sandboxed sessions. A sandbox's checkout is
// one commit deep, has no origin, and keeps its Git metadata to itself: only
// its files reach the managed worktree. This serves the gateway's clones to
// the sandbox, read-only, so it can read history and merge its base with
// Git's own conflict state, and takes back the session's branch, which wtc
// checks before the gateway's branch moves to it.
//
// Nothing the sandbox says decides who it is. A connection's address is a
// sandbox container's on a Docker network it shares with the gateway; OpenClaw
// runs that container on one managed worktree's projection; the session that
// owns the worktree decides what the sandbox may read: the repositories of
// its collection, with the collection's branches and what origin has.
import { spawn } from "node:child_process";
import { createWriteStream, mkdtempSync, rmSync } from "node:fs";
import { createServer } from "node:http";
import os from "node:os";
import path from "node:path";

// The id of the managed worktree a sandbox container projects, from the
// workspace OpenClaw gives it.
export function projectionWorktreeId(workspaceDir) {
  const m = String(workspaceDir ?? "").match(/\/worktree-projections\/([A-Za-z0-9-]+)\/workspace\/?$/);
  return m ? m[1] : null;
}

// What git upload-pack shows a sandbox of a clone: origin's branches and
// tags as the gateway fetched them, and the collection's own branches.
// Everything else, other collections' branches included, stays hidden, and
// so unfetchable: upload-pack takes no request for a commit it did not
// advertise.
export function uploadPackConfig(branches) {
  const pairs = [
    ["http.receivepack", "false"],
    ["http.getanyfile", "false"],
    ["uploadpack.allowAnySHA1InWant", "false"],
    ["uploadpack.allowReachableSHA1InWant", "false"],
    ["uploadpack.allowTipSHA1InWant", "false"],
    ["uploadpack.hideRefs", "HEAD"],
    ["uploadpack.hideRefs", "refs/"],
    ["uploadpack.hideRefs", "!refs/remotes/origin/"],
    ["uploadpack.hideRefs", "!refs/tags/"],
    ...[...branches].map((b) => ["uploadpack.hideRefs", `!refs/heads/${b}`]),
  ];
  const env = { GIT_CONFIG_COUNT: String(pairs.length) };
  pairs.forEach(([key, value], i) => {
    env[`GIT_CONFIG_KEY_${i}`] = key;
    env[`GIT_CONFIG_VALUE_${i}`] = value;
  });
  return env;
}

// The headers of a CGI response, and where its body starts.
export function parseCgiHead(buffer) {
  let end = buffer.indexOf("\r\n\r\n");
  let skip = 4;
  if (end < 0) {
    end = buffer.indexOf("\n\n");
    skip = 2;
  }
  if (end < 0) return null;
  let status = 200;
  const headers = {};
  for (const line of buffer.subarray(0, end).toString("latin1").split(/\r?\n/)) {
    const at = line.indexOf(":");
    if (at < 1) continue;
    const name = line.slice(0, at).trim();
    const value = line.slice(at + 1).trim();
    if (name.toLowerCase() === "status") status = Number.parseInt(value, 10) || 500;
    else headers[name] = value;
  }
  return { status, headers, bodyStart: end + skip };
}

const REPO_NAME = /^[A-Za-z0-9._-]+$/;
const MAX_BUNDLE = 512 * 1024 * 1024;

// startSandboxGit listens on host:port. run is the plugin's command runner;
// openclaw is the argv that runs the openclaw CLI; collectionOf and
// worktreeOf are the plugin's.
export function startSandboxGit({ host, port, run, openclaw, listSessionEntries, collectionOf, worktreeOf, projectsByAgent, logger }) {
  let containers = { at: 0, byAddress: new Map() };
  // Running sandbox containers by their addresses, asked again at most every
  // ten seconds, or when an address is not known. An address shared by
  // multiple containers maps to null and cannot authorize a session.
  const containersByAddress = async (force) => {
    if (!force && Date.now() - containers.at < 10000) return containers.byAddress;
    const byAddress = new Map();
    const list = await run([...openclaw, "sandbox", "list", "--json"], { timeoutMs: 30000 });
    let running = [];
    try {
      const json = list.stdout.slice(list.stdout.indexOf("{"));
      running = (list.code === 0 ? JSON.parse(json).containers ?? [] : []).filter((c) => c.running && c.containerName && c.backendId === "docker");
    } catch {
      logger?.warn?.(`wtc sandbox git: openclaw sandbox list failed: ${(list.stderr || list.stdout).slice(0, 300)}`);
    }
    if (running.length > 0) {
      const inspect = await run(["docker", "inspect", "--format", "{{.Name}}{{range .NetworkSettings.Networks}} {{.IPAddress}}{{end}}", ...running.map((c) => c.containerName)], { timeoutMs: 30000 });
      for (const line of (inspect.code === 0 ? inspect.stdout : "").split("\n")) {
        const [name, ...addresses] = line.trim().split(/\s+/);
        const container = running.find((c) => `/${c.containerName}` === name || c.containerName === name);
        for (const address of addresses) if (container && address) {
          byAddress.set(address, byAddress.has(address) && byAddress.get(address) !== container ? null : container);
        }
      }
    }
    containers = { at: Date.now(), byAddress };
    return byAddress;
  };

  // The session a connection's sandbox works for, and the repositories its
  // collection may read.
  // A delivery or project discovery asks Docker again: an address a container gave up may
  // already be another's.
  const identify = async (address, fresh = false) => {
    let container = (await containersByAddress(fresh)).get(address);
    if (!container && !fresh) container = (await containersByAddress(true)).get(address);
    if (container === null) return { error: "this address belongs to more than one sandbox container" };
    if (!container) return { error: "this address is no sandbox container of the gateway's" };
    const id = projectionWorktreeId(container.workspaceDir);
    const agentId = String(container.sessionKey ?? "").match(/^agent:([^:]+):.+$/)?.[1];
    if (!id || !agentId) return { error: "this sandbox works on no managed worktree" };
    const owners = [...(await listSessionEntries({ agentId }))].filter((s) => s.entry?.worktree?.id === id);
    if (owners.length !== 1) return { error: "no single session owns this sandbox's worktree" };
    const self = owners[0];
    const { children } = await collectionOf(agentId, self.sessionKey);
    const repos = new Map();
    for (const { entry } of [self, ...children]) {
      const dir = worktreeOf(entry);
      const name = entry?.projectId;
      if (!dir || !name || !REPO_NAME.test(name)) continue;
      const repo = repos.get(name) ?? { dir, branches: new Set() };
      if (entry.worktree?.branch) repo.branches.add(entry.worktree.branch);
      repos.set(name, repo);
    }
    return { agentId, self: { name: self.entry.projectId, dir: worktreeOf(self.entry) }, repos };
  };

  const send = (res, status, body) => {
    res.writeHead(status, { "content-type": "application/json" });
    res.end(JSON.stringify(body));
  };
  const failure = (r) => (r.stderr || r.stdout).trim().replace(/^wtc: /, "").slice(-600) || `exit ${r.code}`;

  const uploadPack = async (req, res, repo, pathInfo, query) => {
    const common = await run(["git", "-C", repo.dir, "rev-parse", "--path-format=absolute", "--git-common-dir"]);
    if (common.code !== 0) return send(res, 500, { error: "the gateway cannot read this repository" });
    const child = spawn("git", ["http-backend"], {
      env: {
        PATH: process.env.PATH,
        HOME: process.env.HOME,
        GIT_PROJECT_ROOT: common.stdout.trim(),
        GIT_HTTP_EXPORT_ALL: "1",
        PATH_INFO: pathInfo,
        REQUEST_METHOD: req.method,
        QUERY_STRING: query,
        CONTENT_TYPE: req.headers["content-type"] ?? "",
        HTTP_CONTENT_ENCODING: req.headers["content-encoding"] ?? "",
        REMOTE_ADDR: req.socket.remoteAddress ?? "",
        ...uploadPackConfig(repo.branches),
      },
      stdio: ["pipe", "pipe", "pipe"],
    });
    let head = Buffer.alloc(0);
    let started = false;
    child.stdout.on("data", (chunk) => {
      if (started) return res.write(chunk);
      head = Buffer.concat([head, chunk]);
      const parsed = parseCgiHead(head);
      if (!parsed) return;
      started = true;
      res.writeHead(parsed.status, parsed.headers);
      res.write(head.subarray(parsed.bodyStart));
    });
    let stderr = "";
    child.stderr.on("data", (chunk) => { stderr += chunk; });
    child.on("close", (code) => {
      if (!started) send(res, 500, { error: `git http-backend failed: ${stderr.trim().slice(-300) || code}` });
      else res.end();
      if (code !== 0) logger?.warn?.(`wtc sandbox git: http-backend exited ${code}: ${stderr.trim().slice(-300)}`);
    });
    req.pipe(child.stdin);
  };

  const deliver = async (req, res, self, commit) => {
    const dir = mkdtempSync(path.join(os.tmpdir(), "wtc-deliver-"));
    try {
      const file = path.join(dir, "branch.bundle");
      let size = 0;
      await new Promise((resolve, reject) => {
        const out = createWriteStream(file);
        req.on("data", (chunk) => {
          size += chunk.length;
          if (size > MAX_BUNDLE) {
            req.destroy();
            reject(new Error("the bundle is larger than 512 MiB"));
          }
        });
        req.pipe(out);
        out.on("finish", resolve);
        out.on("error", reject);
        req.on("error", reject);
      });
      const argv = ["wtc", "openclaw", "sandbox-deliver", "--commit", commit, ...(size > 0 ? ["--bundle", file] : [])];
      const r = await run(argv, { cwd: self.dir, timeoutMs: 120000 });
      if (r.code !== 0) return send(res, 409, { error: failure(r) });
      return send(res, 200, JSON.parse(r.stdout));
    } finally {
      rmSync(dir, { recursive: true, force: true });
    }
  };

  const server = createServer(async (req, res) => {
    let projectsRequest = false;
    try {
      const url = new URL(req.url, "http://gateway");
      projectsRequest = req.method === "GET" && url.pathname === "/wtc/projects";
      if (projectsRequest) res.setHeader("cache-control", "no-store");
      const address = String(req.socket.remoteAddress ?? "").replace(/^::ffff:/, "");
      const delivery = req.method === "POST" && req.url.startsWith("/wtc/deliver");
      const who = await identify(address, delivery || projectsRequest);
      if (who.error) {
        req.resume();
        return send(res, 403, { error: who.error });
      }
      const parts = url.pathname.split("/").filter(Boolean);
      if (url.pathname === "/wtc/projects" && req.method === "GET") {
        const allowed = Object.hasOwn(projectsByAgent ?? {}, who.agentId) ? projectsByAgent[who.agentId] : null;
        if (!Array.isArray(allowed) || allowed.length === 0 || !allowed.every((p) => typeof p === "string" && /^[a-z0-9][a-z0-9-]{0,63}$/.test(p) && p !== "none" && p !== "ad-hoc")) {
          return send(res, 403, { error: "project discovery is not enabled for this agent" });
        }
        if (!who.self.dir) return send(res, 403, { error: "this session owns no repository" });
        // The caller controls neither identity nor project selection. Do not
        // return command diagnostics, which can contain gateway paths/config.
        try {
          const r = await run(["wtc", "openclaw", "sandbox-projects", ...[...new Set(allowed)].flatMap((p) => ["--project", p])], { cwd: who.self.dir, timeoutMs: 30000, env: { WTC_GATEWAY_GIT: "" } });
          if (r.code !== 0) return send(res, 503, { error: "gateway project metadata unavailable" });
          const result = JSON.parse(r.stdout);
          if (!Array.isArray(result.projects) || result.projects.some((p) => !allowed.includes(p.name))) throw new Error("invalid project metadata");
          res.setHeader("cache-control", "no-store");
          return send(res, 200, result);
        } catch {
          return send(res, 503, { error: "gateway project metadata unavailable" });
        }
      }
      if (parts[0] === "wtc" && parts[1] === "session" && req.method === "GET") {
        if (!who.self.dir) return send(res, 404, { error: "this session owns no repository" });
        const r = await run(["wtc", "openclaw", "sandbox-session"], { cwd: who.self.dir, timeoutMs: 60000 });
        if (r.code !== 0) return send(res, 500, { error: failure(r) });
        return send(res, 200, { ...JSON.parse(r.stdout), repo: who.self.name, repos: [...who.repos.keys()].sort() });
      }
      if (parts[0] === "wtc" && parts[1] === "deliver" && req.method === "POST") {
        if (!who.self.dir) return send(res, 404, { error: "this session owns no repository" });
        return await deliver(req, res, who.self, url.searchParams.get("commit") ?? "");
      }
      const repo = who.repos.get(decodeURIComponent(parts[0] ?? ""));
      const rest = "/" + parts.slice(1).join("/");
      const service = url.searchParams.get("service");
      if (repo && req.method === "GET" && rest === "/info/refs" && service === "git-upload-pack") {
        return await uploadPack(req, res, repo, rest, url.search.slice(1));
      }
      if (repo && req.method === "POST" && rest === "/git-upload-pack") {
        return await uploadPack(req, res, repo, rest, "");
      }
      req.resume();
      if (repo && (service === "git-receive-pack" || rest === "/git-receive-pack")) {
        return send(res, 403, { error: "read-only: the sandbox delivers its branch with wtc catch-up, and the gateway publishes" });
      }
      return send(res, 404, { error: repo ? "not a git request" : `no repository ${parts[0] ?? ""} in this sandbox's collection` });
    } catch (err) {
      if (!res.headersSent) send(res, projectsRequest ? 503 : 500, { error: projectsRequest ? "gateway project metadata unavailable" : String(err?.message ?? err) });
      else res.destroy();
    }
  });
  server.on("error", (err) => logger?.error?.(`wtc sandbox git on ${host}:${port}: ${err.message}`));
  server.listen(port, host);
  return server;
}
