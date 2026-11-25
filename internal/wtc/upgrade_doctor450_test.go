package wtc

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// doctor450Fixture installs a fake mise that selects releases from a fake
// install tree, one directory per installed version, the way mise exec puts
// the selected release first on PATH. Every executable that must not run —
// the ambient PATH copy, the stale previous release, a dev build — writes
// POISON to the log and fails.
type doctor450Fixture struct {
	bin, tools, log string
}

func newDoctor450Fixture(t *testing.T, bin string) doctor450Fixture {
	t.Helper()
	root := filepath.Dir(bin)
	f := doctor450Fixture{bin: bin, tools: filepath.Join(root, "fake-installs"), log: filepath.Join(root, "doctor450.log")}
	t.Setenv("MOCK_LOG", f.log)
	t.Setenv("MOCK_TOOLS", f.tools)
	fixtureFile(t, filepath.Join(bin, "mise"), `#!/bin/sh
printf 'mise %s|%s\n' "$*" "$PWD" >> "$MOCK_LOG"
case "$1" in
  install)
    case "$PWD" in *"$MOCK_INSTALL_FAIL"*) [ -n "$MOCK_INSTALL_FAIL" ] && { echo 'synthetic install failure' >&2; exit 1; } ;; esac
    exit 0 ;;
  trust) exit 0 ;;
  where)
    v=${2#*@}
    [ -d "$MOCK_TOOLS/$v" ] || { echo "$2 is not installed" >&2; exit 1; }
    [ -n "$MOCK_WHERE" ] && { printf '%s\n' "$MOCK_WHERE"; exit 0; }
    echo "$MOCK_TOOLS/$v" ;;
  exec)
    v=${2#*@}
    [ "$3" = "--" ] || exit 64
    shift 3
    [ -d "$MOCK_TOOLS/$v" ] || { echo "not installed" >&2; exit 1; }
    # MOCK_COMPETING is another tool in the active toolset whose bin mise
    # puts ahead of the selected one when it resolves a bare program name.
    PATH="${MOCK_COMPETING:+$MOCK_COMPETING:}$MOCK_TOOLS/$v:$PATH" exec "$@" ;;
esac
`, 0755)
	// Ambient PATH copy and the previous release: selecting either is a bug.
	poison := "#!/bin/sh\nprintf 'POISON %s %s\\n' \"$0\" \"$*\" >> \"$MOCK_LOG\"\nexit 97\n"
	fixtureFile(t, filepath.Join(bin, "wtc"), poison, 0755)
	fixtureFile(t, filepath.Join(f.tools, "0.1.9", "wtc"), poison, 0755)
	return f
}

// release writes a fake target executable that reports version (which may
// differ from the directory it is installed under) and answers doctor with
// the given JSON and exit status.
func (f doctor450Fixture) release(t *testing.T, dir, version, doctorJSON string, exit int) {
	t.Helper()
	fixtureFile(t, filepath.Join(f.tools, dir, "wtc"), `#!/bin/sh
if [ "$TABA_DEV_BUILD" != off ] && [ "$WTC_DEV_BUILD" != off ]; then
  printf 'POISON dev handover allowed %s\n' "$*" >> "$MOCK_LOG"; exit 97
fi
case "$1" in
  --version) echo "wtc version `+version+`" ;;
  release-notes) printf '%s\n' '{"ok":true,"data":[{"version":"0.1.12","content":"target notes\n"}]}' ;;
  doctor)
    pin=$(sed -n 's/^"github:lcorneliussen\/taba" = "\(.*\)"$/\1/p' mise.toml)
    printf 'doctor %s|%s|cwd=%s|coll=%s|root=%s|leak=%s|pin=%s\n' "$*" "$0" "$PWD" "$WTC_COLLECTION" "$TABA_CONFIG_ROOT" "$INITIATOR_ONLY" "$pin" >> "$MOCK_LOG"
    printf '%s\n' '`+doctorJSON+`'
    exit `+strconv.Itoa(exit)+` ;;
  *) exit 2 ;;
esac
`, 0755)
}

func (f doctor450Fixture) read(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(f.log)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(data)
}

func (f doctor450Fixture) reset(t *testing.T) {
	t.Helper()
	if err := os.Remove(f.log); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

const doctor450Clean = `{"ok":true,"data":[{"name":"registry","ok":true,"detail":"2 repositories"},{"name":"git","ok":true,"detail":"/usr/bin/git"}],"summary":"collection diagnostics"}`
const doctor450Warn = `{"ok":true,"data":[{"name":"git","ok":true,"detail":"/usr/bin/git"},{"name":"named-tunnels","ok":false,"detail":"1 of 1 definitions do not resolve; declare x-wtc.port_env on widget/web"}],"summary":"collection diagnostics"}`
const doctor450Fail = `{"ok":false,"data":[{"name":"compatibility","ok":false,"detail":"requires >=0.2,<0.3; running 0.1.12"}],"summary":"collection diagnostics"}`

// A real upgrade installs the target, then runs that release's doctor in this
// collection after the install and the version check.
func TestUpgradeRunsTargetDoctorAfterInstall450(t *testing.T) {
	c, bin := upgradeFixture(t)
	f := newDoctor450Fixture(t, bin)
	f.release(t, "0.1.12", "0.1.12", doctor450Warn, 0)
	t.Setenv("WTC_DEV_BUILD", "")
	t.Setenv("TABA_DEV_BUILD", "")
	result, err := c.Upgrade(UpgradeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	log := f.read(t)
	if strings.Contains(log, "POISON") {
		t.Fatalf("a non-target executable ran:\n%s", log)
	}
	target := filepath.Join(f.tools, "0.1.12", "wtc")
	install := strings.Index(log, "mise install|")
	doctor := strings.Index(log, "mise exec "+cliMiseTool+"@0.1.12 -- "+target+" doctor --json|"+c.Collection)
	version := strings.Index(log, "mise exec "+cliMiseTool+"@0.1.12 -- "+target+" --version|"+c.Collection)
	if install < 0 || version < install || doctor < version {
		t.Fatalf("doctor not run with the installed target after install and version check:\n%s", log)
	}
	if !strings.Contains(log, "doctor --json|"+target+"|cwd="+c.Collection+"|") || !strings.Contains(log, "|pin=0.1.12") {
		t.Fatalf("doctor ran from the wrong executable, directory or pin:\n%s", log)
	}
	d := result.Doctor
	if d == nil || d.Status != "warnings" || d.Version != "0.1.12" || len(d.Findings) != 1 || d.Findings[0].Name != "named-tunnels" ||
		!strings.Contains(d.Findings[0].Detail, "x-wtc.port_env") {
		t.Fatalf("doctor warnings not reported: %+v", d)
	}
	if result.To != "0.1.12" || result.NotesSource != "wtc 0.1.12 release-notes" {
		t.Fatalf("upgrade result changed: %+v", result)
	}
}

func TestUpgradeDoctorIsAdvisoryAndExplicitWhenUnavailable450(t *testing.T) {
	c, bin := upgradeFixture(t)
	f := newDoctor450Fixture(t, bin)
	pin := filepath.Join(c.Harness, ".wtc-cli-version")
	restore := func() {
		t.Helper()
		fixtureGit(t, "-C", c.Harness, "checkout", "--", ".wtc-cli-version", "wtc.toml")
		f.reset(t)
	}

	// Required checks failing do not undo or fail the completed upgrade.
	f.release(t, "0.1.12", "0.1.12", doctor450Fail, 1)
	result, err := c.Upgrade(UpgradeOptions{})
	if err != nil || result.Doctor == nil || result.Doctor.Status != "failed" || len(result.Doctor.Findings) != 1 {
		t.Fatalf("failing doctor not reported as advisory: %+v %v", result.Doctor, err)
	}
	if data, _ := os.ReadFile(pin); string(data) != "0.1.12\n" {
		t.Fatalf("doctor failure rolled back the pin: %q", data)
	}
	restore()

	// The selected executable reports another version: nothing is diagnosed.
	f.release(t, "0.1.12", "0.1.11", doctor450Clean, 0)
	result, err = c.Upgrade(UpgradeOptions{})
	if err != nil || result.Doctor == nil || result.Doctor.Status != "unavailable" || !strings.Contains(result.Doctor.Reason, "0.1.11") {
		t.Fatalf("mismatched target not explicit: %+v %v", result.Doctor, err)
	}
	if log := f.read(t); strings.Contains(log, "doctor --json") || strings.Contains(log, "POISON") {
		t.Fatalf("doctor ran with a mismatched executable:\n%s", log)
	}
	restore()

	// The target is not installed for this collection.
	if err := os.RemoveAll(filepath.Join(f.tools, "0.1.12")); err != nil {
		t.Fatal(err)
	}
	result, err = c.Upgrade(UpgradeOptions{})
	if err != nil || result.Doctor == nil || result.Doctor.Status != "unavailable" || !strings.Contains(result.Doctor.Reason, "not installed") {
		t.Fatalf("missing target not explicit: %+v %v", result.Doctor, err)
	}
	if log := f.read(t); strings.Contains(log, "doctor") || strings.Contains(log, "POISON") {
		t.Fatalf("doctor ran without its target:\n%s", log)
	}
	restore()

	// A target without doctor support is unavailable coverage, not a pass.
	fixtureFile(t, filepath.Join(f.tools, "0.1.12", "wtc"), `#!/bin/sh
case "$1" in --version) echo "wtc version 0.1.12" ;; *) echo "Error: unknown command \"$1\" for \"wtc\"" >&2; exit 1 ;; esac
`, 0755)
	result, err = c.Upgrade(UpgradeOptions{})
	if err != nil || result.Doctor == nil || result.Doctor.Status != "unavailable" || !strings.Contains(result.Doctor.Reason, "does not support") {
		t.Fatalf("unsupported doctor not explicit: %+v %v", result.Doctor, err)
	}
	restore()

	// Without mise nothing is installed, so nothing is diagnosed.
	if err := os.Remove(filepath.Join(bin, "mise")); err != nil {
		t.Fatal(err)
	}
	result, err = c.Upgrade(UpgradeOptions{})
	if err != nil || !result.NotInstalled || result.Doctor == nil || result.Doctor.Status != "unavailable" {
		t.Fatalf("uninstalled target not explicit: %+v %v", result.Doctor, err)
	}
}

func TestUpgradeNeverRunsDoctorWithoutAnAppliedTransition450(t *testing.T) {
	c, bin := upgradeFixture(t)
	f := newDoctor450Fixture(t, bin)
	f.release(t, "0.1.12", "0.1.12", doctor450Clean, 0)

	dry, err := c.Upgrade(UpgradeOptions{DryRun: true})
	if err != nil || dry.Doctor == nil || dry.Doctor.Status != "planned" {
		t.Fatalf("dry run: %+v %v", dry.Doctor, err)
	}
	if _, err := c.Upgrade(UpgradeOptions{Version: "0.1.99"}); err == nil {
		t.Fatal("unpublished target accepted")
	}
	if _, err := c.Upgrade(UpgradeOptions{Version: "0.1.9"}); err == nil {
		t.Fatal("same version accepted")
	}
	t.Setenv("MOCK_INSTALL_FAIL", c.Collection)
	if _, err := c.Upgrade(UpgradeOptions{}); err == nil || !strings.Contains(err.Error(), "not installed") {
		t.Fatalf("failed install not reported: %v", err)
	}
	if log := f.read(t); strings.Contains(log, "doctor") || strings.Contains(log, "--version") || strings.Contains(log, "POISON") {
		t.Fatalf("doctor ran without an applied transition:\n%s", log)
	}
}

// Rollout diagnoses each updated, installed receiving collection once, in
// its own context, and leaves skipped and needs-owner targets alone.
func TestRolloutDiagnosesEachUpdatedCollectionInItsOwnContext450(t *testing.T) {
	c := newWorkspaceFixture(t)
	for _, slug := range []string{"quiet", "busy", "dirty", "broken"} {
		if _, err := c.NewCollection(NewOptions{Slug: slug}); err != nil {
			t.Fatal(err)
		}
	}
	bin := filepath.Join(c.Workspace, "bin")
	fixtureFile(t, filepath.Join(bin, "herdr"), `#!/bin/sh
shift 2
case "$1 $2" in
  'workspace list') printf '%s\n' '{"result":{"workspaces":[{"label":"busy","workspace_id":"w2"},{"label":"quiet","workspace_id":"w3"}]}}' ;;
  'pane list')
    case "$4" in
      w2) printf '%s\n' '{"result":{"panes":[{"label":"agent","pane_id":"w2:p1","agent":"claude","agent_status":"working"}]}}' ;;
      *) printf '%s\n' '{"result":{"panes":[]}}' ;;
    esac ;;
esac
`, 0755)
	f := newDoctor450Fixture(t, bin)
	f.release(t, "0.1.12", "0.1.12", doctor450Warn, 0)
	t.Setenv("PATH", bin+":"+fixtureSystemPath(t))
	t.Setenv("HERDR_SESSION", "fixture")
	// The initiating collection's identity and a key only it defines must not
	// reach a receiving collection's doctor.
	t.Setenv("WTC_COLLECTION", "main")
	t.Setenv("TABA_COLLECTION", "main")
	t.Setenv("INITIATOR_ONLY", "leak")
	fixtureFile(t, filepath.Join(c.Collection, ".env.collection.local"), "INITIATOR_ONLY=leak\n", 0600)

	// The merged harness upgrade.
	source := filepath.Join(c.Workspace, "source-agent-harness")
	fixtureFile(t, filepath.Join(source, ".wtc-cli-version"), "0.1.12\n", 0644)
	fixtureGit(t, "-C", source, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qam", "pin 0.1.12")
	fixtureFile(t, filepath.Join(c.Workspace, "dirty", "harness", "README.md"), "local edit\n", 0644)
	t.Setenv("MOCK_INSTALL_FAIL", filepath.Join(c.Workspace, "broken"))

	opt := CatchUpOptions{All: true, Repos: []string{"harness"}, CleanOnly: true, IdleOnly: true, InstallTools: true, UpgradeDoctor: true, NoSkills: true, NoMCP: true, NoSecrets: true}
	report, _ := c.CatchUp(opt)
	repo, doctor := map[string]string{}, map[string][]CatchUpRow{}
	for _, row := range report.Outcomes {
		if row.Kind == "repo" {
			repo[row.Collection] = row.Outcome
		}
		if row.Repo == "doctor" {
			doctor[row.Collection] = append(doctor[row.Collection], row)
		}
	}
	if repo["busy"] != "needs-owner" || repo["dirty"] == "updated" || repo["quiet"] != "updated" || repo["main"] != "updated" || repo["broken"] != "updated" {
		t.Fatalf("unexpected sweep: %v", repo)
	}
	for _, name := range []string{"main", "quiet"} {
		rows := doctor[name]
		if len(rows) != 1 || rows[0].Outcome != "warnings" || rows[0].Doctor == nil || rows[0].Doctor.Version != "0.1.12" ||
			rows[0].Doctor.Collection != filepath.Join(c.Workspace, name) {
			t.Fatalf("%s not diagnosed exactly once: %+v", name, rows)
		}
	}
	if rows := doctor["broken"]; len(rows) != 1 || rows[0].Outcome != "skipped" || !strings.Contains(rows[0].Reason, "install") {
		t.Fatalf("failed install not left undiagnosed: %+v", rows)
	}
	if len(doctor["busy"]) != 0 || len(doctor["dirty"]) != 0 {
		t.Fatalf("doctor reached a skipped target: %+v", doctor)
	}
	log := f.read(t)
	if strings.Contains(log, "POISON") || strings.Contains(log, "leak=leak") {
		t.Fatalf("wrong executable or inherited initiator context:\n%s", log)
	}
	for _, name := range []string{"main", "quiet"} {
		dir := filepath.Join(c.Workspace, name)
		want := "cwd=" + dir + "|coll=" + name + "|root=" + c.ConfigRoot + "|leak=|pin=0.1.12"
		if strings.Count(log, want) != 1 {
			t.Fatalf("%s doctor context wrong (want %q once):\n%s", name, want, log)
		}
		if strings.Index(log, "mise install|"+dir) > strings.Index(log, want) {
			t.Fatalf("%s diagnosed before install:\n%s", name, log)
		}
	}
	for _, name := range []string{"busy", "dirty", "broken"} {
		if strings.Contains(log, "cwd="+filepath.Join(c.Workspace, name)+"|") {
			t.Fatalf("doctor ran in %s:\n%s", name, log)
		}
	}
	// Advisory findings do not fail the rollout by themselves; the
	// needs-owner and failed-install rows do.
	if report.ExitStatus != 1 {
		t.Fatalf("exit status: %d", report.ExitStatus)
	}

	// A second run finds every harness current: no transition, no doctor.
	f.reset(t)
	t.Setenv("MOCK_INSTALL_FAIL", "")
	report, _ = c.CatchUp(opt)
	for _, row := range report.Outcomes {
		if row.Repo == "doctor" && row.Outcome != "skipped" {
			t.Fatalf("doctor ran without a transition: %+v", row)
		}
	}
	if strings.Contains(f.read(t), "doctor --json") {
		t.Fatalf("doctor executed on a current harness:\n%s", f.read(t))
	}

	// A dry run plans and executes nothing.
	fixtureFile(t, filepath.Join(source, ".wtc-cli-version"), "0.1.13\n", 0644)
	fixtureGit(t, "-C", source, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qam", "pin 0.1.13")
	// A dry run does not fetch; the owner already has the new tip.
	fixtureGit(t, "--git-dir="+filepath.Join(c.Workspace, ".bare", "agent-harness.git"), "fetch", "-q", "origin", "+refs/heads/*:refs/remotes/origin/*")
	f.reset(t)
	opt.DryRun = true
	report, _ = c.CatchUp(opt)
	planned, repoPlanned := 0, map[string]bool{}
	for _, row := range report.Outcomes {
		if row.Kind == "repo" && row.Outcome == "planned" {
			repoPlanned[row.Collection] = true
		}
		if row.Repo == "doctor" {
			if (row.Outcome == "planned") != repoPlanned[row.Collection] || row.Outcome != "planned" && row.Outcome != "skipped" {
				t.Fatalf("dry run doctor row: %+v", row)
			}
			if row.Outcome == "planned" {
				planned++
			}
		}
	}
	if planned == 0 || strings.Contains(f.read(t), "mise exec") {
		t.Fatalf("dry run planned %d doctor rows; log:\n%s", planned, f.read(t))
	}
}

func TestOnlyRolloutRequestsUpgradeDoctor450(t *testing.T) {
	if !RolloutOptions(false).UpgradeDoctor || !RolloutOptions(true).UpgradeDoctor {
		t.Fatal("rollout does not request diagnostics")
	}
	if (CatchUpOptions{All: true, InstallTools: true}).UpgradeDoctor {
		t.Fatal("ordinary catch-up requests diagnostics")
	}
}

// Findings are bounded: failing checks only, at most 20, one line each of at
// most 300 runes; the rest are counted, not listed.
func TestUpgradeDoctorBoundsFindings450(t *testing.T) {
	c, bin := upgradeFixture(t)
	f := newDoctor450Fixture(t, bin)
	checks := []string{`{"name":"git","ok":true,"detail":"fine"}`, `{"name":"long","ok":false,"detail":"first line\n  second line` + strings.Repeat("é", 400) + `"}`}
	for i := 0; i < 24; i++ {
		checks = append(checks, `{"name":"check-`+strconv.Itoa(i)+`","ok":false,"detail":{"n":`+strconv.Itoa(i)+`}}`)
	}
	f.release(t, "0.1.12", "0.1.12", `{"ok":true,"data":[`+strings.Join(checks, ",")+`]}`, 0)
	result, err := c.Upgrade(UpgradeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	d := result.Doctor
	if d == nil || d.Status != "warnings" || len(d.Findings) != upgradeDoctorMaxFindings || d.Truncated != 25-upgradeDoctorMaxFindings {
		t.Fatalf("findings not bounded: %+v", d)
	}
	long := d.Findings[0]
	if long.Name != "long" || utf8.RuneCountInString(long.Detail) != upgradeDoctorMaxDetail || !strings.HasPrefix(long.Detail, "first line second line") || !strings.HasSuffix(long.Detail, "…") {
		t.Fatalf("detail not bounded to one line: %d runes %q", utf8.RuneCountInString(long.Detail), long.Detail)
	}
	if d.Findings[1].Name != "check-0" || d.Findings[1].Detail != "map[n:0]" {
		t.Fatalf("structured detail: %+v", d.Findings[1])
	}
}

// A doctor that hangs is cut off at the timeout even when a child it started
// still holds the output pipes, and is reported as a failed diagnosis.
func TestUpgradeDoctorTimesOutAndReleasesPipes450(t *testing.T) {
	c, bin := upgradeFixture(t)
	f := newDoctor450Fixture(t, bin)
	fixtureFile(t, filepath.Join(f.tools, "0.1.12", "wtc"), `#!/bin/sh
case "$1" in --version) echo "wtc version 0.1.12" ;; doctor) sleep 30; echo '{"ok":true,"data":[]}' ;; esac
`, 0755)
	saved := upgradeDoctorTimeout
	upgradeDoctorTimeout = 500 * time.Millisecond
	t.Cleanup(func() { upgradeDoctorTimeout = saved })
	start := time.Now()
	d := c.upgradeDoctor(c, "0.1.12")
	if elapsed := time.Since(start); elapsed > 15*time.Second {
		t.Fatalf("doctor wait not bounded: %s", elapsed)
	}
	if d.Status != "failed" || !strings.Contains(d.Reason, "timed out") {
		t.Fatalf("timeout not reported: %+v", d)
	}
}

func TestLimitedBufferKeepsTheBound450(t *testing.T) {
	var b limitedBuffer
	chunk := []byte(strings.Repeat("x", 4096))
	for i := 0; i < upgradeDoctorMaxOutput/len(chunk)+3; i++ {
		if n, err := b.Write(chunk); n != len(chunk) || err != nil {
			t.Fatalf("write %d: %d %v", i, n, err)
		}
	}
	if b.Len() != upgradeDoctorMaxOutput {
		t.Fatalf("kept %d bytes", b.Len())
	}
}

// A failing diagnosis alone never fails the rollout.
func TestRolloutDoctorFailureIsAdvisory450(t *testing.T) {
	c := newWorkspaceFixture(t)
	if _, err := c.NewCollection(NewOptions{Slug: "quiet"}); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(c.Workspace, "bin")
	f := newDoctor450Fixture(t, bin)
	f.release(t, "0.1.12", "0.1.12", doctor450Fail, 1)
	t.Setenv("PATH", bin+":"+fixtureSystemPath(t))
	source := filepath.Join(c.Workspace, "source-agent-harness")
	fixtureFile(t, filepath.Join(source, ".wtc-cli-version"), "0.1.12\n", 0644)
	fixtureGit(t, "-C", source, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qam", "pin 0.1.12")
	report, err := c.CatchUp(CatchUpOptions{Collections: []string{"quiet"}, Repos: []string{"harness"}, InstallTools: true, UpgradeDoctor: true, NoSkills: true, NoMCP: true, NoSecrets: true})
	var doctor *CatchUpRow
	for i, row := range report.Outcomes {
		if row.Repo == "doctor" {
			doctor = &report.Outcomes[i]
		}
	}
	if doctor == nil || doctor.Outcome != "problems" || doctor.Doctor == nil || doctor.Doctor.Status != "failed" || !strings.Contains(doctor.Reason, "compatibility") {
		t.Fatalf("failing doctor row: %+v", doctor)
	}
	if err != nil || report.ExitStatus != 0 {
		t.Fatalf("advisory failure failed the rollout: %v, exit %d, %+v", err, report.ExitStatus, report.Outcomes)
	}
}

// #533: mise exec resolves a bare program name across the whole active
// toolset, so another tool's `wtc` (a pre-rename 0.1.x client pinned
// globally) can win over the selected release's. The doctor runs the
// executable inside the selected release's own install directory instead.
func TestUpgradeDoctorRunsTheSelectedReleaseNotAnotherToolsWtc533(t *testing.T) {
	c, bin := upgradeFixture(t)
	f := newDoctor450Fixture(t, bin)
	f.release(t, "0.1.12", "0.1.12", doctor450Clean, 0)
	competing := filepath.Join(filepath.Dir(bin), "competing-tool")
	fixtureFile(t, filepath.Join(competing, "wtc"), "#!/bin/sh\nprintf 'POISON competing %s\\n' \"$*\" >> \"$MOCK_LOG\"\necho 'wtc version 0.1.39'\n", 0755)
	t.Setenv("MOCK_COMPETING", competing)

	d := c.upgradeDoctor(c, "0.1.12")
	log := f.read(t)
	if d.Status != "ok" || strings.Contains(log, "POISON") {
		t.Fatalf("another tool's wtc was selected: %+v\n%s", d, log)
	}
	target := filepath.Join(f.tools, "0.1.12", "wtc")
	for _, want := range []string{
		"mise exec " + cliMiseTool + "@0.1.12 -- " + target + " --version|" + c.Collection,
		"mise exec " + cliMiseTool + "@0.1.12 -- " + target + " doctor --json|" + c.Collection,
		"doctor --json|" + target + "|cwd=" + c.Collection + "|",
	} {
		if !strings.Contains(log, want) {
			t.Fatalf("missing %q:\n%s", want, log)
		}
	}
}

// A release that ships `taba` runs it; its `wtc` beside it is not needed.
// A release that ships only `wtc` (before the rename) runs that, as above.
func TestUpgradeDoctorPrefersTheReleasesTabaExecutable533(t *testing.T) {
	c, bin := upgradeFixture(t)
	f := newDoctor450Fixture(t, bin)
	f.release(t, "0.1.12", "0.1.12", doctor450Clean, 0)
	dir := filepath.Join(f.tools, "0.1.12")
	if err := os.Rename(filepath.Join(dir, "wtc"), filepath.Join(dir, "taba")); err != nil {
		t.Fatal(err)
	}
	fixtureFile(t, filepath.Join(dir, "wtc"), "#!/bin/sh\nprintf 'POISON release wtc %s\\n' \"$*\" >> \"$MOCK_LOG\"\nexit 97\n", 0755)

	d := c.upgradeDoctor(c, "0.1.12")
	log := f.read(t)
	if d.Status != "ok" || strings.Contains(log, "POISON") || !strings.Contains(log, "doctor --json|"+filepath.Join(dir, "taba")+"|") {
		t.Fatalf("release's taba not run: %+v\n%s", d, log)
	}
}

// Whatever `mise where` names must be an absolute directory holding a
// regular executable; anything else is unavailable, and nothing runs.
func TestUpgradeDoctorNeedsAnExecutableInTheInstallDirectory533(t *testing.T) {
	c, bin := upgradeFixture(t)
	f := newDoctor450Fixture(t, bin)
	f.release(t, "0.1.12", "0.1.12", doctor450Clean, 0)
	dir := filepath.Join(f.tools, "0.1.12")
	empty := filepath.Join(filepath.Dir(bin), "empty-install")
	if err := os.MkdirAll(filepath.Join(empty, "wtc"), 0755); err != nil { // a directory, not an executable
		t.Fatal(err)
	}
	notExec := filepath.Join(filepath.Dir(bin), "not-executable")
	fixtureFile(t, filepath.Join(notExec, "taba"), "#!/bin/sh\nexit 0\n", 0644)
	for _, where := range []string{"relative/install", dir + "\n" + dir, empty, notExec} {
		f.reset(t)
		t.Setenv("MOCK_WHERE", where)
		d := c.upgradeDoctor(c, "0.1.12")
		if d.Status != "unavailable" || !strings.Contains(d.Reason, "diagnostics did not run") {
			t.Fatalf("where %q: %+v", where, d)
		}
		if log := f.read(t); strings.Contains(log, "mise exec") || strings.Contains(log, "POISON") {
			t.Fatalf("where %q ran something:\n%s", where, log)
		}
	}
}
