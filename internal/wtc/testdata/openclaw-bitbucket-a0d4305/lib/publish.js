// Publishing a session's worktree to Bitbucket from the gateway, as the
// person who asks and only once they confirm.
//
// The counterpart of OpenClaw's own sessions.github.publish for a personal
// GitHub connection: the token stays in the gateway, the agent is not
// involved, and the confirmation names the exact commit, branch and account.
//
//   options  what would be published, and with whose login
//   publish  → needs_confirmation {requestId, confirmation: {account, requestDigest, …}}
//   confirm  {requestId, requestDigest} → published {url, number, created}
//
// It pushes the branch the worktree is on, as it is: no commit is made, no
// branch is rewritten, nothing is forced. Uncommitted files stay behind.
//
// The worktree is the agent's, and so are its git config and hooks. The
// token never meets them: the confirmed commit is fetched into a repository
// of the gateway's own, without system or global config, and pushed from
// there to the confirmed repository's https URL. Reading the worktree runs
// with its hooks and fsmonitor switched off.
import { createHash, randomUUID } from "node:crypto";
import { execFile } from "node:child_process";
import { mkdtemp, rm } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { promisify } from "node:util";

import { readLogin } from "./logins.js";

const execFileAsync = promisify(execFile);
const REQUEST_TTL_MS = 10 * 60_000;
const API = "https://api.bitbucket.org/2.0";
const NAME = /^[A-Za-z0-9][A-Za-z0-9._\/-]*$/;
// The token reaches git through its environment, never a URL or a config file.
const HELPER = "!f() { [ \"$1\" = get ] && printf 'username=x-bitbucket-api-token-auth\\npassword=%s\\n' \"$BITBUCKET_PUBLISH_TOKEN\"; }; f";

export class PublishRefused extends Error {}

// bitbucket.org/<workspace>/<repo> from an https or ssh remote.
export function parseBitbucketRemote(url) {
  const m = String(url ?? "").trim().match(/^(?:https:\/\/(?:[^@/]+@)?bitbucket\.org\/|git@bitbucket\.org:|ssh:\/\/git@bitbucket\.org\/)([A-Za-z0-9_.-]+)\/([A-Za-z0-9_.-]+?)(?:\.git)?\/?$/);
  return m ? { owner: m[1], repo: m[2] } : undefined;
}

// A managed worktree, as the gateway records it on the session.
const worktreeOf = (entry) => (entry?.worktree ? entry.workspaceDir || entry.spawnedCwd || entry.worktree.path || null : null);

// A commit's subject: the first line after its header.
export function subjectOf(commit) {
  const at = commit.indexOf("\n\n");
  return at < 0 ? "" : commit.slice(at + 2).split("\n")[0].trim();
}

export function digestOf(request) {
  const fields = ["sessionKey", "owner", "repo", "branch", "head", "dest", "title", "description", "draft", "account"];
  return createHash("sha256").update(JSON.stringify(fields.map((f) => request[f] ?? null))).digest("hex");
}

export function createPublisher({ stateDir, getSessionEntry, currentProfile, fetchImpl = fetch, gitEnv = {}, apiUrl = API, now = Date.now }) {
  const requests = new Map();

  // Git in the agent's worktree: nothing of its configuration may run code.
  const SAFE = [
    "-c", "core.fsmonitor=false", "-c", "core.hooksPath=" + os.devNull,
    "-c", "log.showSignature=false", "-c", "gpg.program=false", "-c", "gpg.ssh.program=false", "-c", "gpg.x509.program=false",
  ];
  const wt = (cwd, args) => git(cwd, [...SAFE, ...args]);
  // Git in a repository of the gateway's own, without system or global config.
  const CLEAN = { GIT_CONFIG_NOSYSTEM: "1", GIT_CONFIG_GLOBAL: os.devNull };

  const git = async (cwd, args, env = {}) => {
    try {
      const { stdout } = await execFileAsync("git", ["-C", cwd, ...args], {
        env: { ...process.env, ...gitEnv, ...env, GIT_TERMINAL_PROMPT: "0" },
        timeout: 120_000,
        maxBuffer: 4 * 1024 * 1024,
      });
      return stdout.trim();
    } catch (error) {
      const err = new Error(String(error.stderr || error.message).trim());
      err.stderr = String(error.stderr ?? "");
      throw err;
    }
  };

  // The person asking, and their own login. Nobody else's is ever used.
  async function requester() {
    const profileId = currentProfile()?.profileId;
    if (!profileId) throw new PublishRefused("sign in as a person: publishing uses your own Bitbucket login, and this connection has none");
    const login = await readLogin(stateDir, profileId);
    return { profileId, login };
  }

  async function bitbucket(login, method, path, payload) {
    const res = await fetchImpl(`${apiUrl}${path}`, {
      method,
      headers: {
        authorization: "Basic " + Buffer.from(`${login.user}:${login.token}`).toString("base64"),
        accept: "application/json",
        ...(payload ? { "content-type": "application/json" } : {}),
      },
      body: payload ? JSON.stringify(payload) : undefined,
      redirect: "error",
      signal: AbortSignal.timeout(30_000),
    });
    const text = await res.text();
    if (!res.ok) throw new PublishRefused(`Bitbucket answered ${res.status} to ${method} ${path.split("?")[0]}: ${text.slice(0, 300)}`);
    return text ? JSON.parse(text) : null;
  }

  // What the worktree of a session would publish, read now.
  async function inspect({ sessionKey, agentId }, payload = {}, login) {
    if (!sessionKey) throw new PublishRefused("name the session whose worktree to publish");
    const entry = await getSessionEntry({ sessionKey, ...(agentId ? { agentId } : {}) });
    const cwd = worktreeOf(entry);
    if (!cwd) throw new PublishRefused("this session owns no managed worktree");
    const remote = parseBitbucketRemote(await wt(cwd, ["config", "--get", "remote.origin.url"]).catch(() => ""));
    if (!remote) throw new PublishRefused("this worktree's origin is not a Bitbucket repository");
    const branch = await wt(cwd, ["branch", "--show-current"]);
    if (!branch) throw new PublishRefused("the worktree is not on a branch");
    if (!NAME.test(branch)) throw new PublishRefused(`the branch name ${branch} cannot be published`);
    if (await wt(cwd, ["rev-parse", "-q", "--verify", "MERGE_HEAD"]).catch(() => "")) {
      throw new PublishRefused("a merge is in progress in the worktree: finish it first");
    }
    const head = await wt(cwd, ["rev-parse", "HEAD"]);
    let dest = String(payload.dest ?? "");
    if (dest && !NAME.test(dest)) throw new PublishRefused("the destination is not a branch name");
    if (!dest) {
      const ref = await wt(cwd, ["symbolic-ref", "-q", "--short", "refs/remotes/origin/HEAD"]).catch(() => "");
      dest = ref.replace(/^origin\//, "");
      // No origin/HEAD in the worktree: the repository's main branch, from Bitbucket.
      if (!dest && login) dest = (await bitbucket(login, "GET", `/repositories/${remote.owner}/${remote.repo}`))?.mainbranch?.name ?? "";
      if (!dest && login) throw new PublishRefused("cannot tell which branch the pull request goes into: name it");
    }
    // Only plumbing that reads refs and objects: comparing file contents (git
    // status, git diff) runs the worktree's filter drivers, and git log may
    // run its signature program, as the gateway's user.
    const title = String(payload.title ?? "").trim() || subjectOf(await wt(cwd, ["cat-file", "commit", head]));
    const description = String(payload.description ?? "");
    return { cwd, ...remote, branch, head, dest, title, description, draft: payload.draft !== false };
  }

  async function options(action) {
    const { login } = await requester();
    const target = await inspect(action, action.payload, login);
    return {
      account: login ? { user: login.user, name: login.name } : null,
      repo: { owner: target.owner, repo: target.repo, branch: target.branch, dest: target.dest, head: target.head },
    };
  }

  async function publish(action) {
    const { profileId, login } = await requester();
    if (!login) return { status: "not_connected" };
    const target = await inspect(action, action.payload, login);
    for (const [id, r] of requests) if (r.expires <= now()) requests.delete(id);
    const request = {
      ...target,
      sessionKey: action.sessionKey,
      agentId: action.agentId,
      profileId,
      account: login.user,
      expires: now() + REQUEST_TTL_MS,
    };
    request.requestDigest = digestOf(request);
    const requestId = randomUUID();
    requests.set(requestId, request);
    return {
      status: "needs_confirmation",
      requestId,
      confirmation: {
        account: login.user,
        name: login.name,
        requestDigest: request.requestDigest,
        owner: request.owner,
        repo: request.repo,
        branch: request.branch,
        head: request.head,
        dest: request.dest,
        title: request.title,
        description: request.description,
        draft: request.draft,
      },
    };
  }

  async function confirm(action) {
    const { profileId, login } = await requester();
    const requestId = String(action.payload?.requestId ?? "");
    const request = requests.get(requestId);
    if (!request || request.expires <= now()) {
      requests.delete(requestId);
      throw new PublishRefused("this publication request has expired: ask again");
    }
    // Only the person who asked confirms, for exactly what they were shown.
    if (request.profileId !== profileId || request.sessionKey !== action.sessionKey) {
      throw new PublishRefused("only the person who asked can confirm this publication");
    }
    if (action.payload?.requestDigest !== request.requestDigest) {
      throw new PublishRefused("the confirmation does not match the request: ask again");
    }
    if (!login || login.user !== request.account) throw new PublishRefused("your Bitbucket login changed since you asked: ask again");
    requests.delete(requestId);

    // The worktree must still be what was confirmed.
    const current = await inspect(request, { dest: request.dest, title: request.title, description: request.description, draft: request.draft }, login);
    if (current.head !== request.head || current.branch !== request.branch || current.owner !== request.owner || current.repo !== request.repo) {
      throw new PublishRefused("the worktree moved on since you asked: ask again to publish what is there now");
    }

    const trackingRef = `refs/remotes/origin/${request.branch}`;
    // Expected old value for update-ref: a concurrent fetch must not be
    // overwritten. A zero OID below means the ref must still be absent.
    const trackedBeforePush = await wt(request.cwd, ["rev-parse", "--verify", trackingRef]).catch(() => "");
    // A plain push: when the branch on Bitbucket has commits this one lacks,
    // it fails, and that is the answer. Catch up and publish again; nothing
    // is forced.
    await pushConfirmed(request, login);
    // The isolated push cannot update this checkout's remote-tracking ref.
    // Record it before PR API calls: those may fail after the push succeeded.
    let warning;
    try {
      const remote = parseBitbucketRemote(await wt(request.cwd, ["config", "--get", "remote.origin.url"]));
      if (remote?.owner !== request.owner || remote?.repo !== request.repo) throw new Error("origin changed");
      await wt(request.cwd, ["update-ref", "-m", "Bitbucket publication", trackingRef, request.head, trackedBeforePush || "0".repeat(request.head.length)]);
    } catch {
      // Recording can fail through a changed origin, ref conflict or lock.
      // Recheck origin before accepting concurrent evidence as sufficient.
      const remote = parseBitbucketRemote(await wt(request.cwd, ["config", "--get", "remote.origin.url"]).catch(() => ""));
      const contains = remote?.owner === request.owner && remote?.repo === request.repo &&
        await wt(request.cwd, ["merge-base", "--is-ancestor", request.head, trackingRef]).then(() => true, () => false);
      if (!contains) warning = "The push succeeded, but local status could not be updated. Check origin, run git fetch --prune origin, then refresh status.";
    }

    const base = `/repositories/${request.owner}/${request.repo}/pullrequests`;
    const q = `source.repository.full_name="${request.owner}/${request.repo}" AND source.branch.name="${request.branch}" AND destination.branch.name="${request.dest}" AND state="OPEN"`;
    const open = (await bitbucket(login, "GET", `${base}?q=${encodeURIComponent(q)}`))?.values?.[0];
    const pr = open ?? (await bitbucket(login, "POST", base, {
      title: request.title,
      description: request.description,
      draft: request.draft,
      source: { branch: { name: request.branch } },
      destination: { branch: { name: request.dest } },
    }));
    return {
      status: "published",
      account: login.user,
      branch: request.branch,
      head: request.head,
      dest: request.dest,
      number: pr?.id ?? null,
      url: pr?.links?.html?.href ?? null,
      created: !open,
      ...(warning ? { warning } : {}),
    };
  }

  async function pushConfirmed(request, login) {
    const dir = await mkdtemp(path.join(os.tmpdir(), "bitbucket-publish-"));
    try {
      await git(dir, ["init", "-q", "--bare"], CLEAN);
      // Only objects come over from the worktree; its config and hooks stay there.
      // origin's branches come along too, so that the push sends only what
      // Bitbucket lacks.
      await git(dir, ["fetch", "-q", "--no-tags", request.cwd, `+refs/heads/${request.branch}:refs/heads/${request.branch}`, "+refs/remotes/origin/*:refs/remotes/origin/*"], CLEAN);
      if ((await git(dir, ["rev-parse", `refs/heads/${request.branch}`], CLEAN)) !== request.head) {
        throw new PublishRefused("the worktree moved on since you asked: ask again to publish what is there now");
      }
      await git(dir, [
        "-c", "credential.helper=",
        "-c", `credential.helper=${HELPER}`,
        "push", "--no-verify", `https://bitbucket.org/${request.owner}/${request.repo}.git`,
        `${request.head}:refs/heads/${request.branch}`,
      ], { ...CLEAN, BITBUCKET_PUBLISH_TOKEN: login.token });
    } catch (error) {
      if (error instanceof PublishRefused) throw error;
      const detail = String(error.stderr || error.message).trim();
      const why = detail.split("\n").filter((l) => /^remote:|^ ! /.test(l)).map((l) => l.replace(/^remote:\s*/, "").trim()).filter(Boolean);
      throw new PublishRefused(/non-fast-forward|fetch first/.test(detail)
        ? `${request.branch} on Bitbucket has commits this branch lacks: catch up (merge, never rebase) and publish again`
        : `git push failed: ${(why.length ? why : detail.split("\n").slice(-1)).join(" ")}`);
    } finally {
      await rm(dir, { recursive: true, force: true });
    }
  }

  // Session action handlers: a refusal is an answer, not a failure.
  const handle = (fn) => async (action) => {
    try {
      return { ok: true, result: await fn(action) };
    } catch (error) {
      if (error instanceof PublishRefused) return { ok: false, error: error.message, code: "refused" };
      throw error;
    }
  };

  return { options: handle(options), publish: handle(publish), confirm: handle(confirm), requests };
}
