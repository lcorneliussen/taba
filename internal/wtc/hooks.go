package wtc

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"taba.sh/taba/internal/branding"
	"taba.sh/taba/internal/envcompat"
)

// RunHook invokes an optional harness-level lifecycle hook. Pre-hooks can veto;
// post-hooks report failures without hiding a completed operation.
func (c *Context) RunHook(event string, values map[string]string) error {
	if event == "" || strings.ContainsAny(event, "/\\") || strings.Contains(event, "..") {
		return fmt.Errorf("invalid hook event %q", event)
	}
	path := filepath.Join(c.Harness, "hooks", "wtc", event+".sh")
	post := strings.HasSuffix(event, ".post")
	fail := func(err error) error {
		if post {
			fmt.Fprintf(os.Stderr, branding.Text("{{command}}: warning: hook %s failed: %v\n"), event, err)
			return nil
		}
		return fmt.Errorf("hook %s failed: %w", event, err)
	}
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fail(err)
	}
	if !info.Mode().IsRegular() || info.Mode()&0111 == 0 {
		return fail(fmt.Errorf("hook must be an executable file: %s", path))
	}
	if values == nil {
		values = map[string]string{}
	}
	payload, err := json.Marshal(map[string]any{
		"event": event, "collection": c.Collection, "harness": c.Harness,
		"workspace": c.Workspace, "values": values,
	})
	if err != nil {
		return fail(err)
	}
	command := exec.Command(path)
	command.Dir = c.Collection
	command.Env = append(append(os.Environ(), envcompat.Both("WTC_COLLECTION", filepath.Base(c.Collection))...), envcompat.Both("WTC_CONFIG_ROOT", c.ConfigRoot)...)
	command.Stdin = bytes.NewReader(payload)
	command.Stdout = os.Stderr
	command.Stderr = os.Stderr
	if err := command.Run(); err != nil {
		return fail(err)
	}
	return nil
}
