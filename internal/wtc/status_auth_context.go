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
	"syscall"
	"time"

	"golang.org/x/sys/unix"
	"gopkg.in/yaml.v3"
)

// A credential is only compared through a keyed digest. The random key belongs
// to the disposable response cache, not to any credential store. Losing it
// invalidates observations; it never migrates unscoped observations.
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
		for _, name := range []string{"GH_TOKEN", "GITHUB_TOKEN", "GH_HOST", "GH_REPO"} {
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
		if host := os.Getenv("GH_HOST"); host != "" && host != forge {
			return statusAuthContext{}
		}
		if token == "" && hostsOK && configOK {
			var hostsConfig map[string]struct {
				Token string `yaml:"oauth_token"`
			}
			if yaml.Unmarshal(hosts, &hostsConfig) == nil {
				known = hostsConfig[forge].Token != ""
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
		user, pass := bitbucketCredentials()
		known = ok && valid && user != "" && pass != "" && config.OAuth == ""
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
	return kind == "budget" || kind == "cool" || kind == "pending"
}

type statusAuthOperationKey struct{}
type statusAuthSelection map[string]statusAuthContext

// Freeze local selection for one observation. Every cache reader, lock and
// publication uses that namespace; a changed selection invalidates the work.
func statusAuthOperation(ctx context.Context) context.Context {
	if _, ok := ctx.Value(statusAuthOperationKey{}).(statusAuthSelection); ok {
		return ctx
	}
	return context.WithValue(ctx, statusAuthOperationKey{}, statusAuthSelection{
		"github.com":    statusResolveAuthContext("github.com"),
		"bitbucket.org": statusResolveAuthContext("bitbucket.org"),
	})
}
func statusAuthForContext(ctx context.Context, forge string) statusAuthContext {
	if selection, ok := ctx.Value(statusAuthOperationKey{}).(statusAuthSelection); ok {
		return selection[forge]
	}
	return statusResolveAuthContext(forge)
}
func statusAuthCurrent(ctx context.Context, forge string) bool {
	auth := statusAuthForContext(ctx, forge)
	return auth.known && auth == statusResolveAuthContext(forge)
}
func statusAuthSnapshotScope(ctx context.Context) string {
	selection, _ := statusAuthOperation(ctx).Value(statusAuthOperationKey{}).(statusAuthSelection)
	return selection["github.com"].scope + ":" + selection["bitbucket.org"].scope
}

func statusAuthSelectionCurrent(ctx context.Context) bool {
	return statusAuthForContext(ctx, "github.com") == statusResolveAuthContext("github.com") && statusAuthForContext(ctx, "bitbucket.org") == statusResolveAuthContext("bitbucket.org")
}
