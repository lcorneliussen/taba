package wtc

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Synthetic host without bb (#471): a fake twg, git with a fake per-repository
// credential helper, and poison token markers that must never appear anywhere
// but the matching read child's environment.

const (
	poisonTokenAlpha = "poison-token-alpha-7f3e"
	poisonTokenBeta  = "poison-token-beta-91c2"
)

type noBBHost struct {
	bin, log, helperLog, networkLog string
}

// Network containment: every synthetic host shadows network tools and bb with
// scripts that record the attempt and fail, points the REST root at a local
// guard that fails the test on any request a test did not route itself, and
// the fake twg refuses any target outside the synthetic workspace. The test
// fails at cleanup if anything tried to leave the host.
var poisonNetworkTools = []string{"bb", "curl", "wget", "nc", "ssh", "http", "https"}

const poisonToolScript = `#!/bin/sh
printf 'network-tool:%s\n' "$(basename "$0")" >> "$FAKE_NETWORK_LOG"
exit 97
`

// fakeTWG answers by subcommand and records argv and the token it received.
const fakeTWGScript = `#!/bin/sh
log="$FAKE_TWG_LOG"
if [ "$1" = "--version" ]; then echo 1.4.4; exit 0; fi
ws=""; prev=""
for a in "$@"; do [ "$prev" = "--workspace" ] && ws="$a"; prev="$a"; done
if [ "$ws" != "acme" ]; then printf 'network-tool:twg-target:%s\n' "$ws" >> "$FAKE_NETWORK_LOG"; exit 98; fi
printf 'argv:%s\n' "$*" >> "$log"
printf 'token:%s user-set:%s user:[%s]\n' "${TWG_BBC_TOKEN:-none}" "${TWG_BBC_USER+yes}" "${TWG_BBC_USER-}" >> "$log"
case "$FAKE_TWG_MODE" in
denied) echo '{"ok":false,"error":{"code":"AUTH_INVALID","kind":"auth","statusCode":401,"message":"secret '"$TWG_BBC_TOKEN"'"}}'; exit 1;;
esac
repo=""; prev=""
for a in "$@"; do [ "$prev" = "--repo" ] && repo="$a"; prev="$a"; done
case "$*" in
*"pull-requests get"*) echo '{"items":[{"input":"7","ok":true,"data":{"id":7,"state":"OPEN","title":"Synthetic","destination":{"branch":{"name":"main"},"repository":{"full_name":"acme/'"$repo"'"}},"source":{"commit":{"hash":"aaaaaaaaaaaa"}},"participants":[]}}]}';;
*"pull-requests activity"*) echo '{"items":[{"input":"7","ok":true,"data":[{"pull_request":{"id":7},"update":{"state":"MERGED","date":"2026-11-20T10:00:00Z"}}]}]}';;
*"comment query"*) echo '{"values":[{"id":1,"content":{"raw":"hello"},"created_on":"2026-11-20T10:00:00Z"}]}';;
*"repo get"*) echo '{"full_name":"acme/'"$repo"'","is_private":true}';;
*"pull-requests query"*) if [ "$FAKE_TWG_MODE" = no-pr ]; then echo '{"values":[]}'; else echo '{"values":[{"id":7,"source":{"branch":{"name":"feature"}}}]}'; fi;;
*"comment create"*) [ "$FAKE_TWG_MODE" = write-denied ] && { echo '{"ok":false,"error":{"code":"FORBIDDEN","statusCode":403}}'; exit 1; }
  echo '{"id":555,"links":{"html":{"href":"https://bitbucket.org/acme/'"$repo"'/pull-requests/7#comment-555"}}}';;
*"comment update"*) echo '{"id":41}';;
*"comment resolve"*) echo '{}';;
*"pull-requests update"*) echo '{"id":7,"draft":false}';;
*) echo '{"ok":false,"error":{"code":"UNKNOWN_COMMAND"}}'; exit 2;;
esac
`

// The helper answers per repository path, records each query, and prints a
// username that must be ignored.
const fakeHelperScript = `#!/bin/sh
[ "$1" = get ] || exit 0
path=""
while IFS= read -r line; do [ -z "$line" ] && break; case "$line" in path=*) path="${line#path=}";; esac; done
echo "query:$path" >> "$FAKE_HELPER_LOG"
if [ -n "$FAKE_TOKEN_DIR" ] && [ -f "$FAKE_TOKEN_DIR/${path%.git}" ]; then
  printf 'username=x-token-auth\npassword=%s\n' "$(cat "$FAKE_TOKEN_DIR/${path%.git}")"; exit 0
fi
case "$path" in
acme/alpha.git) printf 'username=x-token-auth\npassword=%s\n' "` + poisonTokenAlpha + `";;
acme/beta.git) printf 'username=someone\npassword=%s\n' "` + poisonTokenBeta + `";;
esac
`

func newNoBBHost(t *testing.T) noBBHost {
	t.Helper()
	dir := t.TempDir()
	h := noBBHost{bin: filepath.Join(dir, "bin"), log: filepath.Join(dir, "twg.log"), helperLog: filepath.Join(dir, "helper.log"), networkLog: filepath.Join(dir, "network.log")}
	if err := os.Mkdir(h.bin, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, tool := range poisonNetworkTools {
		if err := os.WriteFile(filepath.Join(h.bin, tool), []byte(poisonToolScript), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	guard := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unrouted Bitbucket REST request in a synthetic test: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusTeapot)
	}))
	oldAPI := statusBBAPI
	statusBBAPI = guard.URL
	t.Cleanup(func() {
		statusBBAPI = oldAPI
		guard.Close()
		if raw, _ := os.ReadFile(h.networkLog); len(raw) > 0 {
			t.Errorf("synthetic host tried to leave containment:\n%s", raw)
		}
	})
	if err := os.WriteFile(filepath.Join(h.bin, "twg"), []byte(fakeTWGScript), 0o755); err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join(dir, "helper.sh")
	if err := os.WriteFile(helper, []byte(fakeHelperScript), 0o755); err != nil {
		t.Fatal(err)
	}
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not available")
	}
	if err := os.Symlink(gitPath, filepath.Join(h.bin, "git")); err != nil {
		t.Fatal(err)
	}
	gitConfig := filepath.Join(dir, "gitconfig")
	if err := os.WriteFile(gitConfig, []byte("[credential]\n\thelper = \n\thelper = "+helper+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// /bin and /usr/bin stay for sh and coreutils; neither holds bb.
	t.Setenv("PATH", h.bin+":/bin:/usr/bin")
	t.Setenv("GIT_CONFIG_GLOBAL", gitConfig)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("FAKE_TWG_LOG", h.log)
	t.Setenv("FAKE_HELPER_LOG", h.helperLog)
	t.Setenv("FAKE_TWG_MODE", "")
	t.Setenv("FAKE_NETWORK_LOG", h.networkLog)
	t.Setenv("GIT_ALLOW_PROTOCOL", "file")
	t.Setenv("GIT_SSH_COMMAND", filepath.Join(h.bin, "ssh"))
	t.Setenv("TABA_CONFIG_ROOT", "")
	t.Setenv("WTC_CONFIG_ROOT", "")
	unsetEnv(t, "TABA_BITBUCKET_TRANSPORT", "WTC_BITBUCKET_TRANSPORT", "TABA_BITBUCKET_READ_CREDENTIAL", "WTC_BITBUCKET_READ_CREDENTIAL", "TWG_BBC_TOKEN", "TWG_BBC_USER")
	if path, _ := exec.LookPath("bb"); path != filepath.Join(h.bin, "bb") {
		t.Fatalf("synthetic host must shadow bb with its poison tool, found %q", path)
	}
	twgVersionChecks = sync.Map{}
	bitbucketHostCredentials = sync.Map{}
	t.Cleanup(func() { bitbucketHostCredentials = sync.Map{} })
	t.Setenv("FAKE_TOKEN_DIR", "")
	return h
}

func unsetEnv(t *testing.T, names ...string) {
	t.Helper()
	for _, name := range names {
		value, set := os.LookupEnv(name)
		os.Unsetenv(name)
		if set {
			t.Cleanup(func() { os.Setenv(name, value) })
		}
	}
}

func (h noBBHost) read(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(raw)
}

func assertNoPoison(t *testing.T, where, text string) {
	t.Helper()
	for _, token := range []string{poisonTokenAlpha, poisonTokenBeta} {
		if strings.Contains(text, token) {
			t.Fatalf("%s leaked a host token: %q", where, text)
		}
	}
}

func TestBitbucketReadsWithoutBBUseTWGByDefault(t *testing.T) {
	h := newNoBBHost(t)
	ctx := statusAuthOperation(context.Background())
	raw, err := statusFetchPRRawContext(ctx, PRRecord{Repo: "alpha", Number: "7"}, "acme/alpha", "bitbucket.org")
	if err != nil {
		t.Fatalf("status PR read without bb: %v", err)
	}
	detail, err := statusBBDetail(raw, PRRecord{Number: "7"})
	if err != nil || detail.State != "OPEN" || detail.HeadCommit != "aaaaaaaaaaaa" || detail.Base != "main" {
		t.Fatalf("detail = %+v, %v", detail, err)
	}
	merged := statusBBCompleteMergeTimeContext(ctx, statusPRDetail{Number: "7", State: "MERGED"}, "acme/alpha")
	if merged.MergedOn != "2026-11-20T10:00:00Z" {
		t.Fatalf("merge time from TWG activity = %q", merged.MergedOn)
	}
	comments, err := bitbucketReadPRComments(ctx, "acme/alpha", "7", 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parseBitbucketBundleComments(comments); err != nil {
		t.Fatalf("TWG comments must satisfy the strict bundle parser: %v", err)
	}
	if got := reviewRepositoryAudience(t.TempDir(), "bitbucket", "acme/alpha"); got != "private" {
		t.Fatalf("audience = %q", got)
	}
	if _, err := BitbucketReadOpenPRs("", "acme/alpha", "feature", 100); err != nil {
		t.Fatalf("open PR discovery: %v", err)
	}
	log := h.read(t, h.log)
	if !strings.Contains(log, "argv:bitbucket pull-requests get 7 --strict --workspace acme --repo alpha --output json --output-summary none") {
		t.Fatalf("PR read argv not pinned:\n%s", log)
	}
	if strings.Contains(log, "token:poison") || strings.Contains(log, "user-set:yes") {
		t.Fatalf("personal reads must not receive a host credential:\n%s", log)
	}
	if h.read(t, h.helperLog) != "" {
		t.Fatal("personal reads must not ask the credential helper")
	}
}

func TestBitbucketHostReadCredentialReachesOnlyItsRepositoryChild(t *testing.T) {
	h := newNoBBHost(t)
	t.Setenv("TABA_BITBUCKET_READ_CREDENTIAL", "git-credential")
	ctx := statusAuthOperation(context.Background())
	for _, repo := range []string{"alpha", "beta", "alpha"} {
		if _, err := bitbucketReadPR(ctx, "acme/"+repo, "7"); err != nil {
			t.Fatalf("%s: %v", repo, err)
		}
	}
	log := h.read(t, h.log)
	entries := strings.Split(strings.TrimSpace(log), "\n")
	if len(entries) != 6 {
		t.Fatalf("log:\n%s", log)
	}
	want := []string{poisonTokenAlpha, poisonTokenBeta, poisonTokenAlpha}
	for i, token := range want {
		argv, env := entries[2*i], entries[2*i+1]
		assertNoPoison(t, "argv", argv)
		if env != "token:"+token+" user-set:yes user:[]" {
			t.Fatalf("read %d env = %q, want token %s with an empty user", i, env, token)
		}
	}
	// Once per repository per operation, whatever the helper's username said.
	if got := h.read(t, h.helperLog); got != "query:acme/alpha.git\nquery:acme/beta.git\n" {
		t.Fatalf("helper queries = %q", got)
	}
	if _, set := os.LookupEnv("TWG_BBC_TOKEN"); set {
		t.Fatal("a host token entered this process's environment")
	}
	// Within the memory window another operation reuses the answer; after
	// it, the next operation asks again, so rotation applies.
	if _, err := bitbucketReadPR(statusAuthOperation(context.Background()), "acme/alpha", "7"); err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(h.read(t, h.helperLog), "query:acme/alpha.git"); got != 1 {
		t.Fatalf("alpha queried %d times within the memory window", got)
	}
	old := bitbucketHostCredentialTTL
	bitbucketHostCredentialTTL = 0
	defer func() { bitbucketHostCredentialTTL = old }()
	if _, err := bitbucketReadPR(statusAuthOperation(context.Background()), "acme/alpha", "7"); err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(h.read(t, h.helperLog), "query:acme/alpha.git"); got != 2 {
		t.Fatalf("alpha queried %d times after the memory window", got)
	}
}

func TestBitbucketHostReadCredentialRESTBearerIsPerRepository(t *testing.T) {
	newNoBBHost(t)
	t.Setenv("TABA_BITBUCKET_READ_CREDENTIAL", "git-credential")
	t.Setenv("BB_USERNAME", "personal-user")
	t.Setenv("BB_API_TOKEN", "personal-token")
	var mu sync.Mutex
	seen := map[string]string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen[strings.Split(strings.TrimPrefix(r.URL.Path, "/repositories/acme/"), "/")[0]] = r.Header.Get("Authorization")
		mu.Unlock()
		w.Write([]byte(`{"values":[]}`))
	}))
	defer server.Close()
	old := statusBBAPI
	statusBBAPI = server.URL
	defer func() { statusBBAPI = old }()
	ctx := statusAuthOperation(context.Background())
	for _, repo := range []string{"alpha", "beta"} {
		if _, err := statusFetchBBBranchContext(ctx, "acme/"+repo, "feature"); err != nil {
			t.Fatalf("%s: %v", repo, err)
		}
	}
	if seen["alpha"] != "Bearer "+poisonTokenAlpha || seen["beta"] != "Bearer "+poisonTokenBeta {
		t.Fatalf("authorization by repository = %v", seen)
	}
}

func TestBitbucketHostReadCredentialFailsClosed(t *testing.T) {
	h := newNoBBHost(t)
	t.Setenv("TABA_BITBUCKET_READ_CREDENTIAL", "git-credential")
	t.Setenv("BB_USERNAME", "personal-user")
	t.Setenv("BB_API_TOKEN", "personal-token")
	ctx := statusAuthOperation(context.Background())
	// No helper answer: unavailable, and neither the personal REST login nor
	// the personal TWG login is used instead.
	_, err := bitbucketReadPR(ctx, "acme/gamma", "7")
	if err == nil || !strings.Contains(err.Error(), "no host read credential") {
		t.Fatalf("err = %v", err)
	}
	if _, _, err := bitbucketRESTCredentials(ctx, "acme/gamma"); err == nil {
		t.Fatal("REST must not fall back to the personal login")
	}
	if strings.Contains(h.read(t, h.log), "argv:") {
		t.Fatal("no read may run without the selected credential")
	}
	// bb cannot carry a host credential: refused before any child runs.
	t.Setenv("TABA_BITBUCKET_TRANSPORT", "bb")
	_, err = bitbucketReadPR(statusAuthOperation(context.Background()), "acme/alpha", "7")
	if err == nil || !strings.Contains(err.Error(), "needs the twg client") {
		t.Fatalf("err = %v", err)
	}
	// An unknown credential selection is refused, never the default.
	t.Setenv("TABA_BITBUCKET_TRANSPORT", "")
	t.Setenv("TABA_BITBUCKET_READ_CREDENTIAL", "shared")
	_, err = bitbucketReadPR(statusAuthOperation(context.Background()), "acme/alpha", "7")
	if err == nil || !strings.Contains(err.Error(), "unsupported "+bitbucketReadCredentialVariable) {
		t.Fatalf("err = %v", err)
	}
}

func TestBitbucketDeniedReadIsClassifiedWithoutEcho(t *testing.T) {
	h := newNoBBHost(t)
	t.Setenv("TABA_BITBUCKET_READ_CREDENTIAL", "git-credential")
	t.Setenv("FAKE_TWG_MODE", "denied")
	_, err := bitbucketReadPR(statusAuthOperation(context.Background()), "acme/alpha", "7")
	var classified *bitbucketReadError
	if !errors.As(err, &classified) || classified.cause != bitbucketCauseAuth {
		t.Fatalf("err = %v", err)
	}
	assertNoPoison(t, "diagnostic", err.Error())
	if got := strings.Count(h.read(t, h.log), "argv:"); got != 1 {
		t.Fatalf("a denied read ran %d times; it must not be retried", got)
	}
}

func TestBitbucketReadSelectionIsPartOfAuthContext(t *testing.T) {
	newNoBBHost(t)
	t.Setenv("BB_USERNAME", "personal-user")
	t.Setenv("BB_API_TOKEN", "personal-token")
	twg := statusResolveAuthContext("bitbucket.org")
	// The forge context covers the REST credential; PR detail through an
	// unfingerprinted TWG profile is operation-private per entry.
	if !twg.known || statusAuthEntry(statusAuthOperation(context.Background()), "pr", "bitbucket.org", "acme/alpha").known {
		t.Fatal("REST context must stay known; TWG profile PR detail must stay operation-private")
	}
	t.Setenv("TABA_BITBUCKET_READ_CREDENTIAL", "git-credential")
	host := statusResolveAuthContext("bitbucket.org")
	if host.known || host.scope == twg.scope {
		t.Fatalf("host credential forge context = %+v, personal TWG = %+v", host, twg)
	}
	t.Setenv("TABA_BITBUCKET_READ_CREDENTIAL", "")
	t.Setenv("TABA_BITBUCKET_TRANSPORT", "bb")
	bb := statusResolveAuthContext("bitbucket.org")
	if bb.scope == twg.scope || bb.scope == host.scope {
		t.Fatal("each selection needs its own context")
	}
	// A selection change during an operation makes it no longer current.
	ctx := statusAuthOperation(context.Background())
	t.Setenv("TABA_BITBUCKET_TRANSPORT", "twg")
	if statusAuthSelectionMatches(ctx, "bitbucket.org") {
		t.Fatal("a changed selection must not match the frozen operation")
	}
	if bitbucketReadFor(ctx).selection.Client != bitbucketClientBB {
		t.Fatal("the operation's reader must stay on its frozen selection")
	}
}

func TestBitbucketListReadsFailClosedOnLimitsAndIdentity(t *testing.T) {
	if _, err := twgValues([]byte(`{"values":[],"next":"https://example.invalid/page2"}`)); err == nil {
		t.Fatal("a reply with a next page is incomplete")
	}
	if _, err := twgValues([]byte(`{"values":[],"partial":true}`)); err == nil {
		t.Fatal("a partial reply is incomplete")
	}
	if err := twgPinPullRequest([]byte(`{"id":8,"destination":{"repository":{"full_name":"acme/alpha"}}}`), "acme", "alpha", "7"); err == nil {
		t.Fatal("another PR id must be refused")
	}
	if err := twgPinPullRequest([]byte(`{"id":7,"destination":{"repository":{"full_name":"acme/other"}}}`), "acme", "alpha", "7"); err == nil {
		t.Fatal("another repository must be refused")
	}
	for _, slug := range []string{"acme", "acme/../x", "acme/a b", "/alpha"} {
		if _, _, err := splitBitbucketSlug(slug); err == nil {
			t.Fatalf("slug %q accepted", slug)
		}
	}
}

// The containment itself is tested: a stray network tool or an unrouted REST
// read must be caught, not silently pass.
func TestNoBBHostContainmentCatchesEscapes(t *testing.T) {
	h := newNoBBHost(t)
	out, err := exec.Command("curl", "https://example.invalid").CombinedOutput()
	if err == nil {
		t.Fatalf("curl was not poisoned: %s", out)
	}
	if !strings.Contains(h.read(t, h.networkLog), "network-tool:curl") {
		t.Fatal("poisoned tool did not record the attempt")
	}
	if _, err := bitbucketReadPR(statusAuthOperation(context.Background()), "other/alpha", "7"); err == nil {
		t.Fatal("a target outside the synthetic workspace must fail")
	}
	if !strings.Contains(h.read(t, h.networkLog), "twg-target:other") {
		t.Fatal("twg target guard did not record the escape")
	}
	if strings.HasPrefix(statusBBAPI, "https://api.bitbucket.org") {
		t.Fatal("REST root must be the local guard")
	}
	// Clear the expected records so cleanup checks only unexpected escapes.
	os.WriteFile(h.networkLog, nil, 0o600)
}

func TestBitbucketHostReadCredentialIsNotForwardedCrossHost(t *testing.T) {
	newNoBBHost(t)
	t.Setenv("TABA_BITBUCKET_READ_CREDENTIAL", "git-credential")
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("cross-host redirect target received a request (authorization present: %v)", r.Header.Get("Authorization") != "")
	}))
	defer other.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+r.URL.Path, http.StatusFound)
	}))
	defer origin.Close()
	statusBBAPI = origin.URL // restored by newNoBBHost's cleanup
	_, err := statusFetchBBBranchContext(statusAuthOperation(context.Background()), "acme/alpha", "feature")
	if err == nil || !strings.Contains(err.Error(), "unsafe Bitbucket API redirect") {
		t.Fatalf("err = %v", err)
	}
	assertNoPoison(t, "diagnostic", err.Error())
}

// Cached observations follow the credential that produced them (#471).
func TestBitbucketCacheContextsFollowTheReadCredential(t *testing.T) {
	h := newNoBBHost(t)
	tokens := t.TempDir()
	t.Setenv("FAKE_TOKEN_DIR", tokens)
	if err := os.MkdirAll(filepath.Join(tokens, "acme"), 0o700); err != nil {
		t.Fatal(err)
	}
	setToken := func(repo, token string) {
		t.Helper()
		bitbucketHostCredentials = sync.Map{} // a new process, or the window passed
		if token == "" {
			os.Remove(filepath.Join(tokens, "acme", repo))
			return
		}
		if err := os.WriteFile(filepath.Join(tokens, "acme", repo), []byte(token), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	key := strconv.FormatInt(time.Now().UnixNano(), 10)
	write := func(kind, slug, data string) {
		t.Helper()
		if !statusWriteForgeEntryObservedContext(statusAuthOperation(context.Background()), kind, "bitbucket.org", slug, key, []byte(data), time.Now()) {
			t.Fatalf("write %s %s refused", kind, slug)
		}
	}
	read := func(kind, slug string) string {
		t.Helper()
		data, _, ok := statusReadForgeEntryObservedContext(statusAuthOperation(context.Background()), kind, "bitbucket.org", slug, key, time.Hour)
		if !ok {
			return ""
		}
		return string(data)
	}
	t.Setenv("TABA_BITBUCKET_READ_CREDENTIAL", "git-credential")
	setToken("alpha", "synthetic-alpha-1")
	setToken("beta", "synthetic-beta-1")

	// Unchanged known token: another operation (refresh, pane, process)
	// shares the observation and the in-flight path.
	write("pr", "acme/alpha", `{"id":7}`)
	setToken("alpha", "synthetic-alpha-1")
	if read("pr", "acme/alpha") != `{"id":7}` {
		t.Fatal("an unchanged host token must share cached observations across operations")
	}
	a := statusForgeEntryPathContext(statusAuthOperation(context.Background()), "pr", "bitbucket.org", "acme/alpha", key)
	b := statusForgeEntryPathContext(statusAuthOperation(context.Background()), "pr", "bitbucket.org", "acme/alpha", key)
	if a != b {
		t.Fatal("two operations with the same token must share one in-flight path")
	}
	// Another repository's token is another context.
	write("pr", "acme/beta", `{"id":8}`)
	if read("pr", "acme/beta") != `{"id":8}` || read("pr", "acme/alpha") != `{"id":7}` {
		t.Fatal("repositories must keep their own contexts")
	}
	// Rotation: the old observation is not reused.
	setToken("alpha", "synthetic-alpha-2")
	if read("pr", "acme/alpha") != "" {
		t.Fatal("a rotated host token must not reuse the old token's observations")
	}
	// Logout or no answer (the helper has nothing for gamma): operation-
	// private, never on disk.
	if statusEntryKnown(statusAuthOperation(context.Background()), "pr", "bitbucket.org", "acme/gamma", key) {
		t.Fatal("a repository without a host credential must not have a known context")
	}
	write("checks", "acme/gamma", `{"head":"x"}`)
	if read("checks", "acme/gamma") != "" {
		t.Fatal("an unknown context must not share observations")
	}
	// Switching back to personal reads never sees host-token observations.
	setToken("alpha", "synthetic-alpha-1")
	t.Setenv("TABA_BITBUCKET_READ_CREDENTIAL", "")
	if read("pr", "acme/alpha") != "" {
		t.Fatal("personal reads must not see host-credential observations")
	}

	// Personal TWG with a profile taba cannot fingerprint: PR detail stays
	// operation-private, REST checks keep their own shared context.
	t.Setenv("BB_USERNAME", "synthetic-user")
	t.Setenv("BB_API_TOKEN", "synthetic-rest-token")
	write("pr", "acme/alpha", `{"id":9}`)
	if read("pr", "acme/alpha") != "" {
		t.Fatal("an unfingerprinted TWG profile must keep PR detail operation-private")
	}
	write("checks", "acme/alpha", `{"head":"y"}`)
	if read("checks", "acme/alpha") != `{"head":"y"}` {
		t.Fatal("REST observations must stay shared under the personal TWG default")
	}
	// TWG token from the environment: fingerprinted, shared, and separate
	// from another token.
	t.Setenv("TWG_BBC_TOKEN", "synthetic-twg-1")
	write("pr", "acme/alpha", `{"id":10}`)
	if read("pr", "acme/alpha") != `{"id":10}` {
		t.Fatal("a fingerprinted TWG token must share PR detail")
	}
	t.Setenv("TWG_BBC_TOKEN", "synthetic-twg-2")
	if read("pr", "acme/alpha") != "" {
		t.Fatal("another TWG token must not reuse PR detail")
	}
	// No cache file name or content holds a token.
	entries, _ := os.ReadDir(statusForgeCacheDir())
	for _, entry := range entries {
		raw, _ := os.ReadFile(filepath.Join(statusForgeCacheDir(), entry.Name()))
		for _, secret := range []string{"synthetic-alpha-1", "synthetic-alpha-2", "synthetic-beta-1", "synthetic-twg-1", "synthetic-rest-token"} {
			if strings.Contains(entry.Name(), secret) || strings.Contains(string(raw), secret) {
				t.Fatalf("cache entry %s holds a credential", entry.Name())
			}
		}
	}
	_ = h
}

func TestBitbucketWritesUseThePersonsTWGLogin(t *testing.T) {
	h := newNoBBHost(t)
	// Reads use the host credential; writes must never see it.
	t.Setenv("TABA_BITBUCKET_READ_CREDENTIAL", "git-credential")
	unsetEnv(t, "BB_API_USER", "BB_API_PASS", "BB_USERNAME", "BB_API_TOKEN", "BITBUCKET_PUBLISH")
	m := ReviewManifest{Forge: "bitbucket", Slug: "acme/alpha", PR: "7", URL: "https://bitbucket.org/acme/alpha/pull-requests/7"}
	created, err := postReviewComment(m, "", "-- looks like a flag\nsummary")
	if err != nil || created.CommentID != "555" {
		t.Fatalf("create = %+v, %v", created, err)
	}
	if updated, err := postReviewComment(m, "41", "updated"); err != nil || updated.CommentID != "41" {
		t.Fatalf("update = %+v, %v", updated, err)
	}
	if err := replyReviewInline(m, "41", "reply"); err != nil {
		t.Fatal(err)
	}
	if err := resolveReviewInline(m, "41", ""); err != nil {
		t.Fatal(err)
	}
	if err := BitbucketMarkReady("", t.TempDir(), "acme/alpha", "7", "aaaaaaaaaaaa"); err != nil {
		t.Fatal(err)
	}
	log := h.read(t, h.log)
	for _, want := range []string{
		"argv:bitbucket pull-requests comment create --pull-request 7 --text=-- looks like a flag",
		"argv:bitbucket pull-requests comment update --pull-request 7 --comment 41 --text=updated --workspace acme --repo alpha",
		"argv:bitbucket pull-requests comment create --pull-request 7 --text=reply --reply-to 41",
		"argv:bitbucket pull-requests comment resolve --pull-request 7 --comment 41 --workspace acme --repo alpha",
		"argv:bitbucket pull-requests update --pull-request 7 --ready --workspace acme --repo alpha",
	} {
		if !strings.Contains(log, want) {
			t.Fatalf("missing %q in:\n%s", want, log)
		}
	}
	if strings.Contains(log, "token:poison") || strings.Contains(log, "user-set:yes") || h.read(t, h.helperLog) != "" {
		t.Fatalf("a write saw or asked for the host read credential:\n%s", log)
	}
}

func TestBitbucketWriteRoutesAreExplicit(t *testing.T) {
	h := newNoBBHost(t)
	unsetEnv(t, "BB_API_USER", "BB_API_PASS", "BB_USERNAME", "BB_API_TOKEN", "BITBUCKET_PUBLISH")
	m := ReviewManifest{Forge: "bitbucket", Slug: "acme/alpha", PR: "7"}
	// A gateway: no direct write at all, not even through a login at hand.
	t.Setenv("BITBUCKET_PUBLISH", "gateway")
	t.Setenv("BB_USERNAME", "synthetic-user")
	t.Setenv("BB_API_TOKEN", "synthetic-token")
	for name, err := range map[string]error{
		"create":  func() error { _, err := postReviewComment(m, "", "x"); return err }(),
		"reply":   replyReviewInline(m, "41", "x"),
		"resolve": resolveReviewInline(m, "41", ""),
	} {
		if !errors.Is(err, errBitbucketBrokered) {
			t.Fatalf("%s on a gateway = %v", name, err)
		}
	}
	// Ready on a gateway asks for the person's confirmation instead.
	repo := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", repo).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	if err := BitbucketMarkReady("", repo, "acme/alpha", "7", "aaaaaaaaaaaa"); !errors.Is(err, ErrBitbucketAwaitingConfirmation) {
		t.Fatalf("ready on a gateway = %v", err)
	}
	if err := BitbucketMarkReady("", repo, "acme/alpha", "7", ""); err == nil {
		t.Fatal("a brokered ready without a head was asked for")
	}
	if strings.Contains(h.read(t, h.log), "argv:") {
		t.Fatal("a gateway write ran a client")
	}
	// A refused TWG write is an error and is not retried anywhere.
	t.Setenv("BITBUCKET_PUBLISH", "")
	unsetEnv(t, "BB_USERNAME", "BB_API_TOKEN")
	t.Setenv("FAKE_TWG_MODE", "write-denied")
	if _, err := postReviewComment(m, "", "x"); err == nil {
		t.Fatal("a refused create succeeded")
	}
	if got := strings.Count(h.read(t, h.log), "comment create"); got != 1 {
		t.Fatalf("refused create ran %d times", got)
	}
	// Explicit REST credentials keep the REST comment route; bb only when
	// selected.
	t.Setenv("BB_USERNAME", "synthetic-user")
	t.Setenv("BB_API_TOKEN", "synthetic-token")
	if route, _ := selectedBitbucketWrite("", true); route != bitbucketWriteREST {
		t.Fatalf("route = %s", route)
	}
	if route, _ := selectedBitbucketWrite("", false); route != bitbucketWriteTWG {
		t.Fatalf("non-comment route = %s", route)
	}
	t.Setenv("TABA_BITBUCKET_TRANSPORT", "bb")
	unsetEnv(t, "BB_USERNAME", "BB_API_TOKEN")
	if route, _ := selectedBitbucketWrite("", true); route != bitbucketWriteBB {
		t.Fatalf("bb route = %s", route)
	}
}

func TestBitbucketBranchPullRequestForTheGatewayCard(t *testing.T) {
	newNoBBHost(t)
	t.Setenv("TABA_BITBUCKET_READ_CREDENTIAL", "git-credential")
	var mu sync.Mutex
	auth := map[string]bool{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		auth[r.Header.Get("Authorization")] = true
		mu.Unlock()
		w.Write([]byte(`{"values":[]}`))
	}))
	defer server.Close()
	statusBBAPI = server.URL // restored by newNoBBHost's cleanup
	pr, err := BitbucketBranchPullRequest("", "acme/alpha", "feature")
	if err != nil || pr == nil || pr.Number != 7 || pr.State != "open" || pr.URL != "https://bitbucket.org/acme/alpha/pull-requests/7" || pr.Head != "aaaaaaaaaaaa" {
		t.Fatalf("pr = %+v, %v", pr, err)
	}
	if pr.Checks != "none" && pr.Checks != "unknown" {
		t.Fatalf("checks = %q", pr.Checks)
	}
	for header := range auth {
		if header != "Bearer "+poisonTokenAlpha {
			t.Fatalf("check reads used %q, not alpha's host credential", header)
		}
	}
	// No pull request for the branch: nil, not an error.
	t.Setenv("FAKE_TWG_MODE", "no-pr")
	if pr, err := BitbucketBranchPullRequest("", "acme/alpha", "feature"); pr != nil || err != nil {
		t.Fatalf("no PR = %+v, %v", pr, err)
	}
	// A denied read is an error, never "no pull request".
	t.Setenv("FAKE_TWG_MODE", "denied")
	if pr, err := BitbucketBranchPullRequest("", "acme/alpha", "feature"); pr != nil || err == nil {
		t.Fatalf("denied = %+v, %v", pr, err)
	}
}
