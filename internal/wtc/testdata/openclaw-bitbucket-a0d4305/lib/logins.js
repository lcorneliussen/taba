// One login file per person, readable by the gateway's OS user only.
//
// A file, not the plugin's keyed store: the credential helper and `bb` run
// in the agent's shell and must read the login without the gateway.
import { createHash, randomBytes } from "node:crypto";
import { chmod, mkdir, readFile, rename, rm, writeFile } from "node:fs/promises";
import path from "node:path";
import { parseScopeCheck } from "./scopes.js";

const FIELDS = ["profile", "user", "name", "token", "stored"];

export function loginsDir(stateDir) {
  return path.join(stateDir, "plugins", "bitbucket", "logins");
}

// The profile id never becomes a path segment.
export function loginFile(stateDir, profileId) {
  const digest = createHash("sha256").update(profileId).digest("hex").slice(0, 40);
  return path.join(loginsDir(stateDir), `${digest}.login`);
}

export function parseLogin(text) {
  const login = {};
  for (const line of text.split("\n")) {
    const at = line.indexOf("=");
    if (at > 0) login[line.slice(0, at)] = line.slice(at + 1);
  }
  if (login.scopeCheck) {
    try { login.scopeCheck = parseScopeCheck(JSON.parse(login.scopeCheck)); }
    catch { delete login.scopeCheck; }
  }
  return login.user && login.token ? login : undefined;
}

export async function readLogin(stateDir, profileId) {
  try {
    const login = parseLogin(await readFile(loginFile(stateDir, profileId), "utf8"));
    // A digest collision or a copied file must not hand over another login.
    return login?.profile === profileId ? login : undefined;
  } catch (error) {
    if (error?.code === "ENOENT") return undefined;
    throw error;
  }
}

export async function writeLogin(stateDir, login) {
  for (const field of FIELDS) {
    if (typeof login[field] !== "string" || /[\r\n]/.test(login[field])) {
      throw new Error(`login field ${field} is missing or has a line break`);
    }
  }
  const dir = loginsDir(stateDir);
  await mkdir(dir, { recursive: true, mode: 0o700 });
  await chmod(dir, 0o700);
  const file = loginFile(stateDir, login.profile);
  const temp = `${file}.${randomBytes(6).toString("hex")}.tmp`;
  const check = login.scopeCheck === undefined ? undefined : parseScopeCheck(login.scopeCheck);
  if (login.scopeCheck !== undefined && !check) throw new Error("invalid scope-check metadata");
  const text = FIELDS.map((field) => `${field}=${login[field]}\n`).join("") +
    (check ? `scopeCheck=${JSON.stringify(check)}\n` : "");
  try {
    await writeFile(temp, text, { mode: 0o600, flag: "wx" });
    await rename(temp, file);
  } catch (error) {
    await rm(temp, { force: true });
    throw error;
  }
  return file;
}

export async function removeLogin(stateDir, profileId) {
  await rm(loginFile(stateDir, profileId), { force: true });
}
