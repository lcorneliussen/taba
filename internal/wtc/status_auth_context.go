package wtc

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
	"gopkg.in/yaml.v3"
)

// Read only a regular, owner-only, 32-byte cache key; otherwise fail closed.
func statusAuthReadCacheKey(path string) []byte {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() != 32 {
		return nil
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Getuid()) {
		return nil
	}
	key, err := io.ReadAll(io.LimitReader(f, 33))
	if err != nil || len(key) != 32 {
		return nil
	}
	return key
}

// A credential is only compared through a keyed digest. The random key belongs
// to the disposable response cache, not to any credential store. Losing it
// invalidates observations; it never migrates unscoped observations.
func statusAuthCacheKey() []byte {
	dir := statusForgeCacheDir()
	if !statusSecureCacheDir(dir) {
		return nil
	}
	path := filepath.Join(dir, ".auth-context-key-v1")
	if key := statusAuthReadCacheKey(path); key != nil {
		return key
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	unlock, err := statusLockCacheContext(ctx, path)
	if err != nil {
		return nil
	}
	defer unlock()
	if key := statusAuthReadCacheKey(path); key != nil {
		return key
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		return nil
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return nil
	}
	_, err = f.Write(key)
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		_ = os.Remove(path)
		return nil
	}
	return key
}

// Unknown selection is deliberately distinct from an authenticated context and
// never reuses observations. A known context is keyed by the configuration
// that selects the credential, not by the token: a token replaced in the
// keychain for the same account, even one with narrower permission scopes,
// shares the account's observations.
type statusAuthContext struct {
	scope string
	known bool
}

func statusResolveAuthContext(forge string) statusAuthContext {
	return statusResolveAuthContextWith(forge, selectedBitbucketRead(""))
}

// statusResolveAuthContextWith resolves forge's context with one Bitbucket
// read selection, so an operation's frozen reader and its context agree.
func statusResolveAuthContextWith(forge string, selection bitbucketReadSelection) statusAuthContext {
	key := statusAuthCacheKey()
	if len(key) == 0 {
		return statusAuthContext{}
	}
	mac := hmac.New(sha256.New, key)
	add := func(value string) { _, _ = io.WriteString(mac, value); _, _ = mac.Write([]byte{0}) }
	add("status-auth-v1")
	add(forge)
	home, _ := os.UserHomeDir()
	known := false
	switch forge {
	case "github.com":
		// The context is the configuration that selects the credential: the
		// environment tokens, the gh config directory and its hosts.yml and
		// config.yml. A keychain login names its host and user in hosts.yml, so
		// it is a known context too (#562); switching account, host or store
		// changes the digest. A token replaced inside the keychain for the same
		// account does not, and observations are shared across it.
		token := os.Getenv("GH_TOKEN")
		if token == "" {
			token = os.Getenv("GITHUB_TOKEN")
		}
		for _, name := range []string{"GH_TOKEN", "GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN", "GH_HOST", "GH_REPO"} {
			add(name)
			add(os.Getenv(name))
		}
		dir := os.Getenv("GH_CONFIG_DIR")
		if dir == "" {
			dir = os.Getenv("XDG_CONFIG_HOME")
			if dir == "" {
				dir = filepath.Join(home, ".config")
			}
			dir = filepath.Join(dir, "gh")
		}
		add(dir)
		hosts, hostsOK := statusAuthReadFile(filepath.Join(dir, "hosts.yml"))
		add(string(hosts))
		config, configOK := statusAuthReadFile(filepath.Join(dir, "config.yml"))
		add(string(config))
		known = token != "" && configOK
		if token == "" && hostsOK && configOK {
			var hostsConfig map[string]struct {
				Token string `yaml:"oauth_token"`
				User  string `yaml:"user"`
			}
			if yaml.Unmarshal(hosts, &hostsConfig) == nil {
				known = hostsConfig[forge].Token != "" || hostsConfig[forge].User != ""
			}
		}
		// Unresolved host routing cannot establish which host's keychain is used.
		// Keep the fingerprint for invalidation, while collecting live data only.
		if host := os.Getenv("GH_HOST"); host != "" && host != forge {
			known = false
		}
		if os.Getenv("GH_HOST") == "" && len(hosts) > 0 {
			var configured map[string]any
			if yaml.Unmarshal(hosts, &configured) != nil {
				known = false
			} else {
				for host := range configured {
					if host != forge {
						known = false
					}
				}
			}
		}

	case "bitbucket.org":
		// The direct HTTP reader and the external CLI have different precedence.
		// Hash their complete local candidate set rather than equating usernames.
		for _, name := range []string{"BB_API_USER", "BB_API_PASS", "BB_USERNAME", "BB_API_TOKEN", "ATLASSIAN_ACCOUNT_EMAIL", "ATLASSIAN_API_TOKEN"} {
			add(name)
			add(os.Getenv(name))
		}
		path := filepath.Join(home, ".config", "bb", "config.json")
		add(path)
		raw, ok := statusAuthReadFile(path)
		add(string(raw))
		var config struct {
			Username string `json:"username"`
			Token    string `json:"apiToken"`
			OAuth    string `json:"oauthAccessToken"`
		}
		valid := len(raw) == 0 || json.Unmarshal(raw, &config) == nil
		user, pass := "", ""
		for _, pair := range [][2]string{{"BB_API_USER", "BB_API_PASS"}, {"BB_USERNAME", "BB_API_TOKEN"}, {"ATLASSIAN_ACCOUNT_EMAIL", "ATLASSIAN_API_TOKEN"}} {
			if u, p := os.Getenv(pair[0]), os.Getenv(pair[1]); u != "" && p != "" {
				user, pass = u, p
				break
			}
		}
		if user == "" {
			user, pass = config.Username, config.Token
		}
		known = ok && valid && user != "" && pass != "" && config.OAuth == ""
		// The selected read client and credential are part of the context
		// (#471). This forge-level context covers the REST credential; reads
		// through TWG or a host credential get a per-entry context from
		// statusAuthEntry.
		add(selection.fingerprint())
		known = known && selection.err == nil && selection.Credential == bitbucketCredentialPersonal
	default:
		return statusAuthContext{}
	}
	return statusAuthContext{scope: hex.EncodeToString(mac.Sum(nil)), known: known}
}

// Missing files are stable local evidence; unreadable, oversized or nonregular
// inputs are unknown. Only the bounded bytes enter the keyed digest.
func statusAuthReadFile(path string) ([]byte, bool) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NONBLOCK, 0)
	if os.IsNotExist(err) {
		return nil, true
	}
	if err != nil {
		return nil, false
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 65536 {
		return nil, false
	}
	raw, err := io.ReadAll(io.LimitReader(f, 65537))
	return raw, err == nil && len(raw) <= 65536
}

func statusAuthSharedKind(kind string) bool {
	return kind == "budget" || kind == "cool" || kind == "pending" || kind == "identity-schedule-v1"
}

type statusAuthOperationKey struct{}
type statusAuthSelection map[string]statusAuthContext
type statusAuthObservation struct {
	data     []byte
	observed time.Time
}
type statusAuthOperationState struct {
	selection    statusAuthSelection
	nonce        string
	mu           sync.Mutex
	observations map[string]statusAuthObservation
	gates        map[string]chan struct{}
	calls        map[string]*statusOperationCall
}

// statusOperationCall is one keyed read shared by the callers of a status
// operation.
type statusOperationCall struct {
	done chan struct{}
	raw  []byte
	err  error
}

// statusOperationShared runs fetch at most once per key in this status
// operation: concurrent and later callers in the same operation wait for that
// call and receive its outcome, error included. The record lives and dies
// with the operation, so a new operation calls again. Without an operation,
// fetch runs directly.
func statusOperationShared(ctx context.Context, key string, fetch func() ([]byte, error)) ([]byte, error) {
	state, ok := ctx.Value(statusAuthOperationKey{}).(*statusAuthOperationState)
	if !ok {
		return fetch()
	}
	state.mu.Lock()
	call := state.calls[key]
	if call == nil {
		call = &statusOperationCall{done: make(chan struct{})}
		state.calls[key] = call
		state.mu.Unlock()
		defer close(call.done)
		call.raw, call.err = fetch()
		return call.raw, call.err
	}
	state.mu.Unlock()
	select {
	case <-call.done:
		return call.raw, call.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Freeze local selection for one observation. Unknown encrypted credentials can
// supply live data, but that data exists only in this operation's memory. It
// cannot become a reusable observation in another process or refresh.
func statusAuthOperation(ctx context.Context) context.Context {
	if _, ok := ctx.Value(statusAuthOperationKey{}).(*statusAuthOperationState); ok {
		return ctx
	}
	var nonce [16]byte
	_, _ = rand.Read(nonce[:])
	// An operation that already chose its Bitbucket reader (for a given
	// control root) is fingerprinted with that same selection.
	read := selectedBitbucketRead("")
	if attached, ok := ctx.Value(bitbucketReadKey{}).(*bitbucketReadState); ok {
		read = attached.selection
	}
	state := &statusAuthOperationState{selection: statusAuthSelection{
		"github.com": statusResolveAuthContext("github.com"), "bitbucket.org": statusResolveAuthContextWith("bitbucket.org", read)}, nonce: hex.EncodeToString(nonce[:]), observations: map[string]statusAuthObservation{}, gates: map[string]chan struct{}{}, calls: map[string]*statusOperationCall{}}
	ctx = context.WithValue(ctx, statusAuthOperationKey{}, state)
	if _, ok := ctx.Value(bitbucketReadKey{}).(*bitbucketReadState); ok {
		return ctx
	}
	return context.WithValue(ctx, bitbucketReadKey{}, &bitbucketReadState{selection: read, tokens: map[string]*bitbucketRepoToken{}})
}
func statusAuthForContext(ctx context.Context, forge string) statusAuthContext {
	if state, ok := ctx.Value(statusAuthOperationKey{}).(*statusAuthOperationState); ok {
		return state.selection[forge]
	}
	return statusResolveAuthContext(forge)
}
func statusAuthSelectionMatches(ctx context.Context, forge string) bool {
	return statusAuthForContext(ctx, forge) == statusResolveAuthContext(forge)
}
func statusAuthCurrent(ctx context.Context, forge string) bool {
	return statusAuthForContext(ctx, forge).known && statusAuthSelectionMatches(ctx, forge)
}
func statusAuthSnapshotScope(ctx context.Context) string {
	state, _ := statusAuthOperation(ctx).Value(statusAuthOperationKey{}).(*statusAuthOperationState)
	return state.selection["github.com"].scope + ":" + state.selection["bitbucket.org"].scope
}
func statusAuthSelectionCurrent(ctx context.Context) bool {
	return statusAuthSelectionMatches(ctx, "github.com") && statusAuthSelectionMatches(ctx, "bitbucket.org")
}
func statusAuthMemoryRead(ctx context.Context, path string, age time.Duration) ([]byte, time.Time, bool) {
	state, ok := ctx.Value(statusAuthOperationKey{}).(*statusAuthOperationState)
	if !ok {
		return nil, time.Time{}, false
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	entry, ok := state.observations[path]
	if !ok || time.Since(entry.observed) >= age {
		return nil, time.Time{}, false
	}
	return append([]byte(nil), entry.data...), entry.observed, len(entry.data) > 0
}
func statusAuthMemoryWrite(ctx context.Context, path string, data []byte, observed time.Time) bool {
	state, ok := ctx.Value(statusAuthOperationKey{}).(*statusAuthOperationState)
	if !ok || len(data) == 0 {
		return false
	}
	if observed.IsZero() {
		observed = time.Now()
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	state.observations[path] = statusAuthObservation{data: append([]byte(nil), data...), observed: observed}
	return true
}

func statusAuthMemoryPath(ctx context.Context, path string) {
	state, ok := ctx.Value(statusAuthOperationKey{}).(*statusAuthOperationState)
	if !ok {
		return
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.gates[path] == nil {
		gate := make(chan struct{}, 1)
		gate <- struct{}{}
		state.gates[path] = gate
	}
}
func statusAuthMemoryLock(ctx context.Context, path string) (func(), bool, error) {
	state, ok := ctx.Value(statusAuthOperationKey{}).(*statusAuthOperationState)
	if !ok {
		return nil, false, nil
	}
	state.mu.Lock()
	gate := state.gates[path]
	state.mu.Unlock()
	if gate == nil {
		return nil, false, nil
	}
	select {
	case <-ctx.Done():
		return nil, true, ctx.Err()
	case <-gate:
		if err := ctx.Err(); err != nil {
			gate <- struct{}{}
			return nil, true, err
		}
		return func() { gate <- struct{}{} }, true, nil
	}
}

// statusAuthEntry is the authenticated context of one cached observation.
// GitHub and shared kinds use the forge context. Bitbucket entries depend on
// which credential produced them (#471):
//
//   - host read credential: the forge context plus a keyed digest of that
//     repository's token. An unchanged token shares cached observations and
//     in-flight reads across refreshes and panes; a rotated token, another
//     repository, or no answer from the helper is a different (or
//     operation-private) context.
//   - personal TWG: PR detail and activity come from TWG's own profile. When
//     TWG takes its token from the environment, that token is fingerprinted;
//     a profile kept in TWG's own store cannot be fingerprinted locally, so
//     those observations stay operation-private. REST lists, checks and
//     builds keep the forge context of their own credential.
//   - bb (deprecated): the forge context, as before.
func statusAuthEntry(ctx context.Context, kind, forge, slug string) statusAuthContext {
	base := statusAuthForContext(ctx, forge)
	if forge != "bitbucket.org" || statusAuthSharedKind(kind) {
		return base
	}
	read := bitbucketReadFor(ctx)
	selection := read.selection
	if selection.err != nil {
		return statusAuthContext{scope: base.scope}
	}
	if selection.hostCredential() {
		workspace, repo, err := splitBitbucketSlug(slug)
		if err != nil {
			return statusAuthContext{scope: base.scope}
		}
		user, token, err := read.repoCredential(ctx, workspace, repo)
		if err != nil || token == "" {
			return statusAuthContext{scope: base.scope + ":host-unknown"}
		}
		// The exact identity: credential type, user and token.
		digest := statusAuthKeyedDigest("bitbucket-host-read-v2", selection.Credential, strings.ToLower(slug), user, token)
		if digest == "" {
			return statusAuthContext{scope: base.scope + ":host-unknown"}
		}
		return statusAuthContext{scope: base.scope + ":host:" + digest, known: true}
	}
	if selection.Client == bitbucketClientTWG && statusKindReadByClient(kind) {
		if token := os.Getenv("TWG_BBC_TOKEN"); token != "" {
			digest := statusAuthKeyedDigest("bitbucket-twg-env-v1", os.Getenv("TWG_BBC_USER"), token, os.Getenv("TWG_SITE"))
			if digest != "" {
				return statusAuthContext{scope: base.scope + ":twg-env:" + digest, known: true}
			}
		}
		return statusAuthContext{scope: base.scope + ":twg-profile"}
	}
	return base
}

// statusKindReadByClient reports the cache kinds whose data the selected
// Bitbucket client (rather than REST) reads.
func statusKindReadByClient(kind string) bool {
	switch kind {
	case "pr", "activity", "merge":
		return true
	}
	return false
}

// statusAuthKeyedDigest digests values with the cache's random key; empty
// when the key is unavailable. Values never leave this function.
func statusAuthKeyedDigest(label string, values ...string) string {
	key := statusAuthCacheKey()
	if len(key) == 0 {
		return ""
	}
	mac := hmac.New(sha256.New, key)
	_, _ = io.WriteString(mac, label)
	for _, value := range values {
		_, _ = mac.Write([]byte{0})
		_, _ = io.WriteString(mac, value)
	}
	return hex.EncodeToString(mac.Sum(nil))
}
