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

// Unknown selection is deliberately distinct from an authenticated context.
// In particular, a login name in a config file cannot identify a keychain
// credential or distinguish two tokens with different permission scopes.
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
		// Include both supported sources and store provenance conservatively. No
		// equality claim is made for a credential hidden in encrypted storage.
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
			}
			if yaml.Unmarshal(hosts, &hostsConfig) == nil {
				known = hostsConfig[forge].Token != ""
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
		// (#471). TWG's profile and a host's per-repository token cannot be
		// fingerprinted locally, so their observations stay operation-private.
		add(selection.fingerprint())
		known = known && selection.accountKnown()
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
	read := selectedBitbucketRead("")
	state := &statusAuthOperationState{selection: statusAuthSelection{
		"github.com": statusResolveAuthContext("github.com"), "bitbucket.org": statusResolveAuthContextWith("bitbucket.org", read)}, nonce: hex.EncodeToString(nonce[:]), observations: map[string]statusAuthObservation{}, gates: map[string]chan struct{}{}}
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
