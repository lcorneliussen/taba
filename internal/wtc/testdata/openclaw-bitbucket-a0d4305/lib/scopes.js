export const TOKEN_SETTINGS_URL = "https://id.atlassian.com/manage-profile/security/api-tokens";

// These are API-token scopes, not the similarly named OAuth scopes.
export const REQUIRED_SCOPES = Object.freeze([
  "read:user:bitbucket",
  "read:repository:bitbucket",
  "write:repository:bitbucket",
  "read:pullrequest:bitbucket",
  "write:pullrequest:bitbucket",
  "read:pipeline:bitbucket",
]);

const scopeName = /^(read|write|admin|delete):[a-z-]+:bitbucket$/;
export function scopeList(value) {
  if (!Array.isArray(value) || value.length > 128 || !value.every(s => typeof s === "string" && scopeName.test(s))) return undefined;
  return [...new Set(value)];
}

export function missingScopes(granted) {
  return REQUIRED_SCOPES.filter(scope => !granted.includes(scope));
}

export function scopeCheck(granted, now = new Date()) {
  const scopes = scopeList(granted);
  return {
    status: scopes ? (missingScopes(scopes).length ? "missing" : "verified") : "unknown",
    ...(scopes ? { granted: scopes } : {}),
    checkedAt: now.toISOString(),
  };
}

// Old login files have no check. Invalid metadata cannot turn one into a
// permission-verified login; the credential itself remains readable.
export function parseScopeCheck(value) {
  if (!value || !["unknown", "missing", "verified", "rejected"].includes(value.status) ||
      typeof value.checkedAt !== "string" || !Number.isFinite(Date.parse(value.checkedAt))) return undefined;
  const granted = scopeList(value.granted);
  if (["missing", "verified"].includes(value.status) && (!granted || scopeCheck(granted).status !== value.status)) return undefined;
  return { status: value.status, ...(granted ? { granted } : {}), checkedAt: value.checkedAt };
}
