package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/Masterminds/semver/v3"
	"gopkg.in/yaml.v3"
)

const sha = "5f525fa5abcdef0123456789abcdef0123456789"

var commitTime = time.Date(2026, 11, 27, 15, 12, 0, 0, time.UTC)

func notes(names ...string) fstest.MapFS {
	fsys := fstest.MapFS{"unreleased/README.md": {Data: []byte("# Unreleased\n")}}
	for _, n := range names {
		fsys[n+".md"] = &fstest.MapFile{Data: []byte("# taba/wtc " + n + "\n")}
	}
	return fsys
}

func TestVersionFollowsTheNewestNumberedNotes(t *testing.T) {
	for _, tc := range []struct {
		notes []string
		want  string
	}{
		{[]string{"0.2.9", "0.2.11", "0.2.10"}, "0.2.12-edge.20261127151200.g5f525fa5abcd"},
		{[]string{"0.2.11", "0.2.12-rc.1"}, "0.2.12-edge.20261127151200.g5f525fa5abcd"},
		{[]string{"0.2.12-rc.2", "0.2.12"}, "0.2.13-edge.20261127151200.g5f525fa5abcd"},
	} {
		got, err := Version(notes(tc.notes...), sha, commitTime.In(time.FixedZone("x", 3600)))
		if err != nil || got != tc.want {
			t.Fatalf("%v: got %q, %v; want %q", tc.notes, got, err, tc.want)
		}
	}
}

func TestVersionIsolatesEdgeFromReleasesAndTheirTags(t *testing.T) {
	edge, err := Version(notes("0.2.11"), sha, commitTime)
	if err != nil {
		t.Fatal(err)
	}
	later, _ := Version(notes("0.2.11"), "0"+sha[1:], commitTime.Add(time.Second))
	v := semver.MustParse(edge)
	for _, above := range []string{later, "0.2.12-rc.1", "0.2.12"} {
		if !v.LessThan(semver.MustParse(above)) {
			t.Fatalf("%s must sort below %s", edge, above)
		}
	}
	if !v.GreaterThan(semver.MustParse("0.2.11")) || v.Prerelease() == "" {
		t.Fatalf("%s must be a prerelease above the current release", edge)
	}
	// The stable/RC workflow publishes v* tags; `upgrade --version` looks up v<version>.
	if matched, _ := path.Match("v*", edge); matched || !edgeVersion.MatchString(edge) {
		t.Fatalf("edge tag %s must not look like a release tag", edge)
	}
}

func TestVersionRejectsMissingInputs(t *testing.T) {
	if _, err := Version(notes("0.2.11"), sha[:12], commitTime); err == nil {
		t.Fatal("short SHA accepted")
	}
	if _, err := Version(notes("0.2.11"), strings.ToUpper(sha), commitTime); err == nil {
		t.Fatal("uppercase SHA accepted")
	}
	if _, err := Version(notes("0.2.11"), sha, time.Unix(0, 0)); err == nil {
		t.Fatal("missing commit time accepted")
	}
	if _, err := Version(notes(), sha, commitTime); err == nil {
		t.Fatal("no numbered notes accepted")
	}
}

func runJSON(t *testing.T, runs ...map[string]any) []byte {
	t.Helper()
	data, err := json.Marshal(map[string]any{"total_count": len(runs), "workflow_runs": runs})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func ciRun(id int, change func(map[string]any)) map[string]any {
	r := map[string]any{
		"id": id, "head_sha": sha, "head_branch": "main", "event": "push",
		"path": ".github/workflows/ci.yml", "status": "completed", "conclusion": "success",
		"html_url":        fmt.Sprintf("https://github.com/o/taba/actions/runs/%d", id),
		"repository":      map[string]any{"full_name": "o/taba"},
		"head_repository": map[string]any{"full_name": "o/taba"},
	}
	if change != nil {
		change(r)
	}
	return r
}

func TestEvidenceRequiresSuccessfulCIForTheExactMainCommit(t *testing.T) {
	url, err := Evidence(runJSON(t, ciRun(7, nil), ciRun(9, nil)), sha, "o/taba")
	if err != nil || url != "https://github.com/o/taba/actions/runs/9" {
		t.Fatalf("got %q, %v", url, err)
	}
	for name, tc := range map[string]struct {
		runs []map[string]any
		want string
	}{
		"missing":           {nil, "no .github/workflows/ci.yml push run"},
		"other commit":      {[]map[string]any{ciRun(1, func(r map[string]any) { r["head_sha"] = "0" + sha[1:] })}, "no "},
		"pull request":      {[]map[string]any{ciRun(1, func(r map[string]any) { r["event"] = "pull_request" })}, "no "},
		"other branch":      {[]map[string]any{ciRun(1, func(r map[string]any) { r["head_branch"] = "topic" })}, "no "},
		"other workflow":    {[]map[string]any{ciRun(1, func(r map[string]any) { r["path"] = ".github/workflows/edge.yml" })}, "no "},
		"fork head":         {[]map[string]any{ciRun(1, func(r map[string]any) { r["head_repository"] = map[string]any{"full_name": "x/taba"} })}, "no "},
		"other repository":  {[]map[string]any{ciRun(1, func(r map[string]any) { r["repository"] = map[string]any{"full_name": "x/taba"} })}, "no "},
		"running":           {[]map[string]any{ciRun(1, func(r map[string]any) { r["status"] = "in_progress"; r["conclusion"] = nil })}, "in_progress"},
		"failed":            {[]map[string]any{ciRun(1, func(r map[string]any) { r["conclusion"] = "failure" })}, "concluded failure"},
		"failed rerun":      {[]map[string]any{ciRun(1, nil), ciRun(2, func(r map[string]any) { r["conclusion"] = "cancelled" })}, "concluded cancelled"},
		"failed older run":  {[]map[string]any{ciRun(2, nil), ciRun(1, func(r map[string]any) { r["conclusion"] = "failure" })}, "concluded failure"},
		"skipped is no run": {[]map[string]any{ciRun(1, func(r map[string]any) { r["conclusion"] = "skipped" })}, "concluded skipped"},
	} {
		if _, err := Evidence(runJSON(t, tc.runs...), sha, "o/taba"); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want %q", name, err, tc.want)
		}
	}
	if _, err := Evidence(runJSON(t, ciRun(1, nil)), sha[:7], "o/taba"); err == nil {
		t.Error("short SHA accepted")
	}
	if _, err := Evidence([]byte("{"), sha, "o/taba"); err == nil {
		t.Error("malformed runs accepted")
	}
}

type entry struct {
	name string
	mode int64
	data string
}

func script(version string) string {
	return "#!/bin/sh\necho \"taba version " + version + "\"\n"
}

func goodEntries(version string) []entry {
	return []entry{{"LICENSE", 0o644, "MIT"}, {"taba", 0o755, script(version)}, {"wtc", 0o755, script(version)}}
}

func writeArchive(t *testing.T, path string, entries []entry) {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		if err := tw.WriteHeader(&tar.Header{Name: e.name, Mode: e.mode, Size: int64(len(e.data)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(e.data)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

// dist builds a synthetic, unpublished GoReleaser output. change may replace
// one platform's archive entries before checksums are written.
func dist(t *testing.T, version string, change func(platform string, entries []entry) []entry) string {
	t.Helper()
	dir := t.TempDir()
	var sums strings.Builder
	for _, p := range platforms {
		entries := goodEntries(version)
		if change != nil {
			entries = change(p, entries)
		}
		if entries == nil {
			continue
		}
		name := fmt.Sprintf("taba_%s_%s.tar.gz", version, p)
		writeArchive(t, filepath.Join(dir, name), entries)
		data, _ := os.ReadFile(filepath.Join(dir, name))
		sum := sha256.Sum256(data)
		fmt.Fprintf(&sums, "%s  %s\n", hex.EncodeToString(sum[:]), name)
	}
	if err := os.WriteFile(filepath.Join(dir, "checksums.txt"), []byte(sums.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

const version = "0.2.12-edge.20261127151200.g5f525fa5abcd"

func host() string { return runtime.GOOS + "_" + runtime.GOARCH }

func TestVerifyAcceptsAnUnpublishedEdgePackage(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("synthetic executables are shell scripts")
	}
	dir := dist(t, version, nil)
	m, err := Verify(dir, version, sha, "https://example.invalid/run/1", host())
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Artifacts) != len(platforms) || m.Tag != version || m.SourceSHA != sha || m.Channel != "edge" ||
		!reflect.DeepEqual(m.Executables, []string{"taba", "wtc"}) {
		t.Fatalf("unexpected manifest %+v", m)
	}
	manifest, body := filepath.Join(dir, "edge.json"), filepath.Join(dir, "notes.md")
	if err := writeOutputs(m, manifest, body); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(body)
	for _, want := range []string{sha, "Not a release", "docs/edge-builds.md", m.Artifacts[0].SHA256} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("notes lack %q:\n%s", want, data)
		}
	}
	if strings.Contains(string(data), "brew install") || strings.Contains(string(data), "brew tap") {
		t.Fatal("notes must not offer Homebrew")
	}
	var round Manifest
	raw, _ := os.ReadFile(manifest)
	if err := json.Unmarshal(raw, &round); err != nil || !reflect.DeepEqual(round, m) {
		t.Fatalf("manifest round trip: %v", err)
	}
}

func TestVerifyRejectsBrokenPackages(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("synthetic executables are shell scripts")
	}
	only := func(platform string, f func([]entry) []entry) func(string, []entry) []entry {
		return func(p string, e []entry) []entry {
			if p == platform {
				return f(e)
			}
			return e
		}
	}
	other := "linux_arm64"
	if host() == other {
		other = "darwin_amd64"
	}
	cases := map[string]struct {
		change func(string, []entry) []entry
		want   string
	}{
		"missing platform": {only(other, func([]entry) []entry { return nil }), "missing " + other},
		"missing wtc":      {only(other, func(e []entry) []entry { return e[:2] }), "missing executable wtc"},
		"not executable":   {only(other, func(e []entry) []entry { e[2].mode = 0o644; return e }), "wtc must be one executable"},
		"extra executable": {only(other, func(e []entry) []entry { return append(e, entry{"helper", 0o755, "x"}) }), "unexpected entry helper"},
		"wrong version":    {only(host(), func(e []entry) []entry { e[1].data = script("0.2.11"); return e }), "not " + version},
	}
	for name, tc := range cases {
		if _, err := Verify(dist(t, version, tc.change), version, sha, "", host()); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want %q", name, err, tc.want)
		}
	}

	dir := dist(t, version, nil)
	tampered := filepath.Join(dir, fmt.Sprintf("taba_%s_%s.tar.gz", version, other))
	writeArchive(t, tampered, append(goodEntries(version), entry{"README.md", 0o644, "changed"}))
	if _, err := Verify(dir, version, sha, "", host()); err == nil || !strings.Contains(err.Error(), "does not match checksums.txt") {
		t.Errorf("tampered archive: %v", err)
	}

	dir = dist(t, version, nil)
	writeArchive(t, filepath.Join(dir, "taba_0.2.11_linux_amd64.tar.gz"), goodEntries("0.2.11"))
	if _, err := Verify(dir, version, sha, "", host()); err == nil || !strings.Contains(err.Error(), "unexpected archive") {
		t.Errorf("stray archive: %v", err)
	}

	dir = dist(t, version, nil)
	if _, err := Verify(dir, version, "0"+sha[1:], "", host()); err == nil || !strings.Contains(err.Error(), "does not name source commit") {
		t.Errorf("mismatched source commit: %v", err)
	}
	if _, err := Verify(dir, "0.2.12", sha, "", host()); err == nil || !strings.Contains(err.Error(), "not an edge version") {
		t.Errorf("release version: %v", err)
	}
	if _, err := Verify(dir, "v"+version, sha, "", host()); err == nil {
		t.Error("v-prefixed edge tag accepted")
	}
}

func readYAML(t *testing.T, name string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", name))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return doc
}

// The edge package is the stable package under another version, without a
// tap formula or a GoReleaser publication.
func TestEdgeConfigMatchesStableBuildsWithoutPublishing(t *testing.T) {
	stable, edge := readYAML(t, ".goreleaser.yaml"), readYAML(t, ".goreleaser.edge.yaml")
	for _, key := range []string{"version", "project_name", "before", "builds", "archives", "checksum"} {
		if !reflect.DeepEqual(stable[key], edge[key]) {
			t.Errorf("%s differs between .goreleaser.yaml and .goreleaser.edge.yaml", key)
		}
	}
	for _, key := range []string{"brews", "homebrew_casks", "nfpms", "dockers", "announce", "blobs", "publishers"} {
		if _, ok := edge[key]; ok {
			t.Errorf("edge config must not have %s", key)
		}
	}
	if release, _ := edge["release"].(map[string]any); release["disable"] != true {
		t.Error("edge config must disable GoReleaser's release")
	}
	if snapshot, _ := edge["snapshot"].(map[string]any); snapshot["version_template"] != "{{ .Env.TABA_EDGE_VERSION }}" {
		t.Error("edge snapshot version must come from tools/edge-release version")
	}
	var built []string
	for _, b := range stable["builds"].([]any) {
		build := b.(map[string]any)
		for _, goos := range build["goos"].([]any) {
			for _, goarch := range build["goarch"].([]any) {
				built = append(built, fmt.Sprint(goos)+"_"+fmt.Sprint(goarch))
			}
		}
		if !strings.Contains(strings.Join(executables, " "), fmt.Sprint(build["binary"])) {
			t.Errorf("build %v is not a verified executable", build["binary"])
		}
	}
	for _, p := range platforms {
		if !strings.Contains(strings.Join(built, " "), p) {
			t.Errorf("platform %s is not built by the stable config", p)
		}
	}
}

func TestEdgeWorkflowPublishesOnlyBehindItsGates(t *testing.T) {
	wf := readYAML(t, ".github/workflows/edge.yml")
	on := wf["on"].(map[string]any)
	if _, ok := on["push"]; ok {
		t.Error("edge must not run on push")
	}
	if _, ok := on["workflow_run"]; ok {
		t.Error("edge must not start automatically from CI")
	}
	if perms := wf["permissions"].(map[string]any); perms["contents"] != "read" || len(perms) != 1 {
		t.Errorf("default permissions must be read-only: %v", perms)
	}
	jobs := wf["jobs"].(map[string]any)
	for name, j := range jobs {
		job := j.(map[string]any)
		if perms, _ := job["permissions"].(map[string]any); perms["contents"] == "write" && name != "publish" {
			t.Errorf("job %s must not write contents", name)
		}
	}
	publish := jobs["publish"].(map[string]any)
	// The whole condition, so a weakened operator or a dropped gate fails.
	const gate = "github.event_name == 'workflow_dispatch' && github.ref == 'refs/heads/main' && " +
		"vars.TABA_EDGE_PUBLISH == 'true' && needs.evidence.result == 'success' && needs.package.result == 'success'"
	if cond := strings.Join(strings.Fields(fmt.Sprint(publish["if"])), " "); cond != gate {
		t.Errorf("publish condition = %q, want %q", cond, gate)
	}
	if needs := fmt.Sprint(publish["needs"]); needs != "[evidence package]" {
		t.Errorf("publish needs = %s", needs)
	}
	// Every action is pinned to a commit: the package job's output is attested and published.
	pinned := regexp.MustCompile(`^[\w.-]+/[\w.-]+@[0-9a-f]{40}$`)
	for name, j := range jobs {
		for _, s := range j.(map[string]any)["steps"].([]any) {
			if uses, ok := s.(map[string]any)["uses"].(string); ok && !pinned.MatchString(uses) {
				t.Errorf("job %s uses unpinned action %s", name, uses)
			}
		}
	}
	if publish["environment"] != "edge" {
		t.Error("publish must run in the protected edge environment")
	}
	var run strings.Builder
	for _, s := range publish["steps"].([]any) {
		run.WriteString(fmt.Sprint(s.(map[string]any)["run"]))
	}
	for _, want := range []string{"--prerelease", "--latest=false", "git/ref/tags/$VERSION", "sha256sum --check"} {
		if !strings.Contains(run.String(), want) {
			t.Errorf("publish steps lack %q", want)
		}
	}
	for _, forbidden := range []string{"release-notes", "homebrew", "HOMEBREW", "git push", "--clobber", "upload"} {
		if strings.Contains(run.String(), forbidden) {
			t.Errorf("publish steps must not use %q", forbidden)
		}
	}

	stable := readYAML(t, ".github/workflows/release.yml")["on"].(map[string]any)
	tags := stable["push"].(map[string]any)["tags"].([]any)
	if len(tags) != 1 || tags[0] != "v*" {
		t.Fatalf("stable release trigger changed (%v); recheck edge tag isolation", tags)
	}
}
