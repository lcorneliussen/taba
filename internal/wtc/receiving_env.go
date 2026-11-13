package wtc

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"taba.sh/taba/internal/envcompat"
)

// ReceivingEnvironment validates policy at use time and replaces root aliases
// rather than appending duplicates. It does not isolate provider credentials.
func (c *Context) ReceivingEnvironment(base []string) ([]string, error) {
	scope, err := c.ValidateControlRoot()
	if err != nil {
		return nil, err
	}
	return scope.environment(base, c.Collection), nil
}

func (s ResolvedScope) environment(base []string, collection string) []string {
	var env []string
	for _, setting := range base {
		key, _, _ := strings.Cut(setting, "=")
		switch key {
		case "TABA_CONFIG_ROOT", "WTC_CONFIG_ROOT", "TABA_COLLECTION", "WTC_COLLECTION":
			continue
		}
		env = append(env, setting)
	}
	if collection != "" {
		env = append(env, envcompat.Both("WTC_COLLECTION", filepath.Base(collection))...)
	}
	return append(env, envcompat.Both("WTC_CONFIG_ROOT", s.Root)...)
}

// ReceivingCommand preserves local shell environment behavior, but verifies
// reserved roots after both layers are evaluated and before the program runs.
// Local shell configuration remains executable, trusted receiver-owned code.
func (c *Context) ReceivingCommand(program string, args ...string) (*exec.Cmd, error) {
	env, err := c.ReceivingEnvironment(os.Environ())
	if err != nil {
		return nil, err
	}
	scope, err := c.ValidateControlRoot()
	if err != nil {
		return nil, err
	}
	spellings := map[string]string{"TABA_CONFIG_ROOT": scope.Root, "WTC_CONFIG_ROOT": scope.Root}
	for _, name := range []string{".env.collection", ".env.collection.local"} {
		data, err := os.ReadFile(filepath.Join(c.Collection, name))
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		values := envcompat.Assignments(data)
		if err := scope.checkAliases(values, name); err != nil {
			return nil, err
		}
		for _, key := range rootAliases {
			if value, ok := values[key]; ok {
				spellings[key] = value
			}
		}
	}
	argv := []string{"-c", `set -ea
[ ! -f "$1" ] || . "$1"
[ ! -f "$2" ] || . "$2"
if { [ "${TABA_CONFIG_ROOT-}" != "$3" ] && [ "${TABA_CONFIG_ROOT-}" != "$4" ]; } ||
   { [ "${WTC_CONFIG_ROOT-}" != "$3" ] && [ "${WTC_CONFIG_ROOT-}" != "$5" ]; }; then
  echo 'collection environment conflicts with receiving secrets policy; regenerate env' >&2
  exit 1
fi
export TABA_CONFIG_ROOT="$3" WTC_CONFIG_ROOT="$3"
shift 5
exec "$@"`, "taba-receiver", filepath.Join(c.Collection, ".env.collection"), filepath.Join(c.Collection, ".env.collection.local"), scope.Root, spellings["TABA_CONFIG_ROOT"], spellings["WTC_CONFIG_ROOT"], program}
	cmd := exec.Command("/bin/sh", append(argv, args...)...)
	cmd.Env, cmd.Dir = env, c.Collection
	return cmd, nil
}
