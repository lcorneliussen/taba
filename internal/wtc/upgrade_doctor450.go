package wtc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"taba.sh/taba/internal/envcompat"
)

// UpgradeDoctor is the advisory diagnostics result after a version
// transition: the selected target release's own `doctor`, run read-only in
// the receiving collection. It never changes the upgrade's own outcome.
//
// Status is one of:
//   - ok: the target's doctor ran and every check passed
//   - warnings: it ran; optional checks reported findings
//   - failed: it ran and reported failing required checks, or did not
//     produce a result
//   - unavailable: diagnostics did not run (target not installed, executable
//     reports another version, or the release has no doctor) — not a pass
//   - planned: a dry run; nothing was executed
//   - skipped: no applied transition to diagnose
type UpgradeDoctor struct {
	Status     string                 `json:"status"`
	Version    string                 `json:"version,omitempty"`
	Collection string                 `json:"collection,omitempty"`
	Reason     string                 `json:"reason,omitempty"`
	Findings   []UpgradeDoctorFinding `json:"findings,omitempty"`
	// Truncated reports findings beyond the bound below that were left out.
	Truncated int `json:"truncated,omitempty"`
}

// UpgradeDoctorFinding is one check the target's doctor did not pass.
type UpgradeDoctorFinding struct {
	Name   string `json:"name"`
	Detail string `json:"detail,omitempty"`
}

const (
	upgradeDoctorMaxFindings = 20
	upgradeDoctorMaxDetail   = 300
	upgradeDoctorMaxOutput   = 1 << 20
)

// upgradeDoctorTimeout bounds each target-binary call.
var upgradeDoctorTimeout = 2 * time.Minute

// collectionIdentityEnv names the generated per-collection variables a
// receiving collection must never inherit from the initiating one.
var collectionIdentityEnv = []string{"COLLECTION_PORT_BASE",
	"WTC_COLLECTION", "TABA_COLLECTION", "WTC_AGENT_NAME", "TABA_AGENT_NAME", "WTC_CONFIG_ROOT", "TABA_CONFIG_ROOT"}

// upgradeDoctor runs the target release's doctor for receiver, a collection
// whose installed tools this process just brought to version. The release is
// selected through the same mise tool key and committed pin the environment
// and status reload use, with automatic installs and dev handover off, and
// its reported version is checked before any diagnostics run. The initiator's
// generated environment is replaced by the receiver's.
func (c *Context) upgradeDoctor(receiver *Context, version string) UpgradeDoctor {
	d := UpgradeDoctor{Version: version, Collection: receiver.Collection}
	unavailable := func(reason string) UpgradeDoctor {
		d.Status, d.Reason = "unavailable", reason
		return d
	}
	if !cliVersionPattern.MatchString(version) {
		return unavailable("no valid target release selected")
	}
	mise, err := exec.LookPath("mise")
	if err != nil {
		return unavailable("mise not found; the target release cannot be selected")
	}
	env := c.upgradeDoctorEnv(receiver)
	tool := cliMiseTool + "@" + version
	run := func(args ...string) ([]byte, []byte, error) {
		ctx, cancel := context.WithTimeout(context.Background(), upgradeDoctorTimeout)
		defer cancel()
		cmd := exec.CommandContext(ctx, mise, args...)
		cmd.Dir = receiver.Collection
		cmd.Env = env
		var stdout, stderr limitedBuffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		if ctx.Err() != nil {
			err = fmt.Errorf("timed out after %s", upgradeDoctorTimeout)
		}
		return stdout.Bytes(), stderr.Bytes(), err
	}
	if out, _, err := run("where", tool); err != nil || strings.TrimSpace(string(out)) == "" {
		return unavailable(fmt.Sprintf("wtc %s is not installed for this collection; diagnostics did not run", version))
	}
	out, _, err := run("exec", tool, "--", "wtc", "--version")
	if err != nil {
		return unavailable(fmt.Sprintf("the selected wtc %s did not report its version; diagnostics did not run", version))
	}
	if reported := reportedCLIVersion(out); reported != version {
		return unavailable(fmt.Sprintf("the selected executable reports %q, not %s; diagnostics did not run", boundDetail(reported), version))
	}
	out, stderr, err := run("exec", tool, "--", "wtc", "doctor", "--json")
	var envelope struct {
		OK   *bool            `json:"ok"`
		Data []map[string]any `json:"data"`
	}
	if jsonErr := json.Unmarshal(out, &envelope); jsonErr != nil || envelope.OK == nil {
		if bytes.Contains(stderr, []byte("unknown command")) || bytes.Contains(stderr, []byte("unknown flag")) {
			return unavailable(fmt.Sprintf("wtc %s does not support doctor --json; run its diagnostics by hand", version))
		}
		d.Status = "failed"
		d.Reason = "doctor produced no result"
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			d.Reason = fmt.Sprintf("doctor exited with status %d without a result", exit.ExitCode())
		} else if err != nil {
			d.Reason = "doctor did not complete: " + boundDetail(err.Error())
		}
		return d
	}
	for _, check := range envelope.Data {
		if ok, _ := check["ok"].(bool); ok {
			continue
		}
		if len(d.Findings) == upgradeDoctorMaxFindings {
			d.Truncated++
			continue
		}
		name, _ := check["name"].(string)
		detail := ""
		if value, present := check["detail"]; present && value != nil {
			detail = fmt.Sprint(value)
		}
		d.Findings = append(d.Findings, UpgradeDoctorFinding{Name: boundDetail(name), Detail: boundDetail(detail)})
	}
	switch {
	case !*envelope.OK:
		d.Status, d.Reason = "failed", "required checks failed"
	case len(d.Findings) > 0 || d.Truncated > 0:
		d.Status, d.Reason = "warnings", "suggestions only"
	default:
		d.Status = "ok"
	}
	return d
}

// upgradeDoctorEnv is the receiver's environment for the target binary: the
// caller's process environment without any variable the initiating or the
// receiving collection generates or overrides, then the receiver's generated
// values. mise adds the receiver's env files itself when it runs there.
func (c *Context) upgradeDoctorEnv(receiver *Context) []string {
	drop := map[string]bool{"PWD": true}
	for _, name := range collectionIdentityEnv {
		drop[name] = true
	}
	for _, dir := range []string{c.Collection, receiver.Collection} {
		for _, file := range []string{".env.collection", ".env.collection.local"} {
			if data, err := readRegularFile(filepath.Join(dir, file)); err == nil {
				for name := range envcompat.Assignments(data) {
					drop[name] = true
				}
			}
		}
	}
	for _, name := range []string{DevBuildDisableEnv, LegacyDevBuildDisableEnv, "MISE_AUTO_INSTALL", "MISE_EXEC_AUTO_INSTALL"} {
		drop[name] = true
	}
	var env []string
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if !drop[name] {
			env = append(env, entry)
		}
	}
	if data, err := readRegularFile(filepath.Join(receiver.Collection, ".env.collection")); err == nil {
		for name, value := range envcompat.Assignments(data) {
			env = append(env, name+"="+value)
		}
	}
	return append(env, "PWD="+receiver.Collection, DevBuildDisableEnv+"=off", LegacyDevBuildDisableEnv+"=off",
		"MISE_AUTO_INSTALL=0", "MISE_EXEC_AUTO_INSTALL=0")
}

// readRegularFile reads a generated env file, refusing links and special files.
func readRegularFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", filepath.Base(path))
	}
	return os.ReadFile(path)
}

// reportedCLIVersion reads `<name> version <label>` from --version output.
// A dev build's label carries more words and so never matches a release.
func reportedCLIVersion(out []byte) string {
	line, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	_, label, found := strings.Cut(strings.TrimSpace(line), " version ")
	if !found {
		return strings.TrimSpace(line)
	}
	return strings.TrimPrefix(strings.TrimSpace(label), "v")
}

// boundDetail keeps one line of at most upgradeDoctorMaxDetail runes.
func boundDetail(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= upgradeDoctorMaxDetail {
		return s
	}
	runes := []rune(s)
	return string(runes[:upgradeDoctorMaxDetail-1]) + "…"
}

// limitedBuffer keeps the first upgradeDoctorMaxOutput bytes it is given.
type limitedBuffer struct{ bytes.Buffer }

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if room := upgradeDoctorMaxOutput - b.Len(); room > 0 {
		if len(p) > room {
			b.Buffer.Write(p[:room])
		} else {
			b.Buffer.Write(p)
		}
	}
	return len(p), nil
}

// catchUpUpgradeDoctor adds the rollout's single doctor row for a harness
// target after its env, tools and renders. Only a harness this sweep updated
// and whose pinned release it installed is diagnosed; every other case is an
// explicit row, and none of them changes the sweep's exit status.
func (c *Context) catchUpUpgradeDoctor(report *CatchUpReport, t catchUpTarget, opt CatchUpOptions, receiver *Context, openErr error) {
	repoOutcome, toolsOutcome := "", ""
	for _, row := range report.Outcomes {
		if row.Collection != t.collection {
			continue
		}
		if row.Kind == "repo" && row.Repo == t.repo {
			repoOutcome = row.Outcome
		}
		if row.Kind == "hook" && row.Repo == "tools" {
			toolsOutcome = row.Outcome
		}
	}
	add := func(d UpgradeDoctor) {
		outcome := d.Status
		if outcome == "failed" {
			// Advisory: a failing diagnosis is not a failed rollout step.
			outcome = "problems"
		}
		reason := "doctor " + d.Status
		if d.Version != "" {
			reason = "wtc " + d.Version + " doctor: " + d.Status
		}
		if d.Reason != "" {
			reason += "; " + d.Reason
		}
		for _, finding := range d.Findings {
			reason += "; " + finding.Name
		}
		report.add("hook", t.collection, "doctor", outcome, reason, "", "", "")
		report.Outcomes[len(report.Outcomes)-1].Doctor = &d
	}
	skip := func(reason string) { add(UpgradeDoctor{Status: "skipped", Reason: reason}) }
	switch {
	case openErr != nil:
		skip("target collection unavailable; diagnostics did not run")
		return
	case opt.DryRun && repoOutcome == "planned":
		add(UpgradeDoctor{Status: "planned", Collection: receiver.Collection, Reason: "would run the pinned release's read-only doctor after install"})
		return
	case opt.DryRun || repoOutcome != "updated":
		skip("harness not updated by this rollout; no transition to diagnose")
		return
	case toolsOutcome == "skipped":
		add(UpgradeDoctor{Status: "unavailable", Collection: receiver.Collection, Reason: "pinned release not installed; diagnostics did not run"})
		return
	case toolsOutcome != "ok":
		skip("tool install did not complete; diagnostics did not run")
		return
	}
	version, err := receiver.installedPin()
	if err != nil {
		add(UpgradeDoctor{Status: "unavailable", Collection: receiver.Collection, Reason: "cannot read the pinned release: " + boundDetail(err.Error())})
		return
	}
	add(c.upgradeDoctor(receiver, version))
}

// installedPin is the harness's committed CLI pin, read through the same
// tracked-input guard the upgrade and status reload use.
func (c *Context) installedPin() (string, error) {
	pin, err := pinInput(c.Harness)
	if err != nil {
		return "", err
	}
	pin, err = upgradeInput(c.Harness, pin)
	if err != nil {
		return "", err
	}
	raw, err := os.ReadFile(pin)
	if err != nil {
		return "", err
	}
	version, err := releaseVersion(strings.TrimSpace(string(raw)))
	if err != nil {
		return "", err
	}
	return version.String(), nil
}
