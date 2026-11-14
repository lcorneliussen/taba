package wtc

import (
	"bufio"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"taba.sh/taba/internal/envcompat"
)

// Support says what this release does with a convention's old and new names.
const (
	// SupportBoth: both names are read; when both exist the old one wins.
	SupportBoth = "both"
	// SupportBothNewWins: both names are read; the new one wins.
	SupportBothNewWins = "both, new wins"
	// SupportOldOnly: only the old name exists so far.
	SupportOldOnly = "old only"
	// SupportNew: taba already writes the new name.
	SupportNew = "new"
	// SupportBothShip: both commands ship side by side.
	SupportBothShip = "both ship"
)

// Convention is one name a project, harness, script or agent depends on that
// carries the old wtc name. The list is the source for `taba doctor`, `taba
// conventions` and docs/conventions.md.
type Convention struct {
	ID   string `json:"id"`
	Area string `json:"area"`
	Old  string `json:"old"`
	New  string `json:"new,omitempty"`
	// Support is one of the Support* constants.
	Support string `json:"support"`
	// SupportNote replaces the Support label in the docs when one convention
	// covers names that behave differently.
	SupportNote string `json:"support_note,omitempty"`
	// Owner is "you" when the project writes it, "taba" when taba generates
	// or manages it.
	Owner   string `json:"owner"`
	Migrate string `json:"migrate"`
	// Checked marks conventions `taba doctor` looks for.
	Checked bool `json:"checked"`
}

// Conventions lists the wtc conventions in the order they are documented.
var Conventions = []Convention{
	{ID: "harness-config", Area: "harness", Old: "`wtc.toml`", New: "`taba.toml`", Support: SupportBoth, Owner: "you", Checked: true,
		Migrate: "`git mv wtc.toml taba.toml` in the harness, then commit and merge it as usual. `taba harness init` already writes `taba.toml`."},
	{ID: "harness-pin", Area: "harness", Old: "`.wtc-cli-version`", New: "`.taba-version`", Support: SupportBoth, Owner: "you", Checked: true,
		Migrate: "`git mv .wtc-cli-version .taba-version` in the harness, together with `taba.toml`. A harness with only `.taba-version` seeds new collections with `SCOPE.md`."},
	{ID: "skill-base-digest", Area: "harness", Old: "`overlays/skills/<name>/.wtc-base.sha256`", New: "`.taba-base.sha256`", Support: SupportBoth, Owner: "you", Checked: true,
		Migrate: "`git mv` each digest file to `.taba-base.sha256` in the same folder."},
	{ID: "harness-skills", Area: "harness", Old: "`skills/wtc-<name>`, `overlays/skills/wtc-<name>`", New: "`taba-<name>`", Support: SupportBoth, Owner: "you", Checked: true,
		Migrate: "Rename each folder to `taba-<name>` and update its `name:`; section patches and their base digest move with the folder. Until then the old-name override still wins and `taba-<name>` points to it."},
	{ID: "harness-hooks", Area: "harness", Old: "`hooks/wtc/`", New: "`hooks/taba/` (planned)", Support: SupportOldOnly, Owner: "you", Checked: true,
		Migrate: "No new name yet (#375). Keep `hooks/wtc/`."},
	{ID: "browse-overlay", Area: "harness", Old: "`overlays/browse/wtc-browse.lua`", New: "`taba-browse.lua` (planned)", Support: SupportOldOnly, Owner: "you", Checked: true,
		Migrate: "No new name yet (#375). Keep the old file name."},
	{ID: "harness-references", Area: "harness", Old: "`WTC_*` variables, `wtc …` commands and `wtc-*` skill names in harness files", New: "`TABA_*`, `taba …` and `taba-*`", Support: SupportBothNewWins, Owner: "you", Checked: true,
		SupportNote: "Variables: both, the new one wins. Commands: both ship. Skill names: the old ones are shims",
		Migrate:     "Replace `WTC_X` with `TABA_X`, `wtc` with `taba`, and old skill names such as `/wtc-start` with `/taba-start` in hooks, scripts, skills and instructions, at the lines listed. Other mentions of wtc, such as \"a wtc collection\" or a link to `wtc-cli`, usually become taba too; a file with only those is marked *review*, and a sentence that explains the rename can stay. Generated environments set both prefixes and ship both commands during 0.2.x. A few variables have no `TABA_` form yet: `WTC_GATEWAY_GIT`, `WTC_RETIRE_*`, `WTC_REVIEW_STATS_FILE`, `WTC_AGENT_ENV` and `WTC_DEV_*` (except `WTC_DEV_BUILD`)."},
	{ID: "scope-file", Area: "collection", Old: "`WTC-SCOPE.md`", New: "`SCOPE.md`", Support: SupportBoth, Owner: "you", Checked: true,
		Migrate: "`mv WTC-SCOPE.md SCOPE.md` at the collection root. The file is local, so nothing to commit. Older harness tooling that reads `WTC-SCOPE.md` needs the same change."},
	{ID: "local-references", Area: "collection", Old: "`WTC_*` in `.env.collection.local`", New: "`TABA_*`", Support: SupportBothNewWins, Owner: "you", Checked: true,
		Migrate: "Rename the variables in `.env.collection.local`."},
	{ID: "mise-tool-key", Area: "collection", Old: "`github:lcorneliussen/wtc-cli` in mise configuration", New: "`github:lcorneliussen/taba`", Support: SupportNew, Owner: "you", Checked: true,
		Migrate: "Run `taba env setup` to regenerate `mise.toml`. In `taba.toml` `[mise.tools]`, rename the key by hand."},
	{ID: "generated-state", Area: "collection", Old: "`.wtc/`, `.wtc-prs`, `.wtc-status.*`, `.wtc-catch-up.*`, `.wtc-reviews/`, `.wtc-review-posted/`, `.wtc-status-watchers/`, `.wtc-review-mode`, `.wtc-dev-mode.json`, `.last-wtc-status.yml`", New: "`.taba…` spellings", Support: SupportBoth, Owner: "taba", Checked: true,
		Migrate: "Nothing to do by hand: taba still writes these names and moves them itself once writes follow the harness (#375)."},
	{ID: "rendered-skills", Area: "collection", Old: "rendered `wtc-*` skills in `.claude/skills` and `.agents/skills`", New: "`taba-*`", Support: SupportNew, Owner: "taba", Checked: false,
		Migrate: "Nothing to do by hand: `taba skills render` writes `taba-*`, and each old `wtc-*` name is a short shim that points to it."},
	{ID: "control-root-env", Area: "user", Old: "`$WTC_CONFIG_ROOT/wtc.env`", New: "`taba.env`", Support: SupportBoth, Owner: "you", Checked: true,
		Migrate: "`mv wtc.env taba.env` in the control root. Every 0.2.x release reads it."},
	{ID: "user-config-dir", Area: "user", Old: "`$XDG_CONFIG_HOME/wtc`", New: "`$XDG_CONFIG_HOME/taba`", Support: SupportBoth, Owner: "you", Checked: true,
		Migrate: "`mv ~/.config/wtc ~/.config/taba` (or under `$XDG_CONFIG_HOME`) once no wtc 0.1.x client uses it."},
	{ID: "user-data-dir", Area: "user", Old: "`$XDG_DATA_HOME/wtc`", New: "`$XDG_DATA_HOME/taba`", Support: SupportBoth, Owner: "you", Checked: true,
		Migrate: "`mv ~/.local/share/wtc ~/.local/share/taba` (or under `$XDG_DATA_HOME`) once no wtc 0.1.x client uses it."},
	{ID: "repo-references", Area: "repository", Old: "`WTC_*` variables, `wtc …` commands, `wtc-*` skill names and other mentions of wtc in a repository's docs, scripts, configuration and CI", New: "`TABA_*`, `taba …`, `taba-*` and taba", Support: SupportBothNewWins, Owner: "you", Checked: true,
		SupportNote: "Variables: both, the new one wins. Commands: both ship. Skill names: the old ones are shims",
		Migrate:     "Replace them at the lines listed and merge the change through the repository's normal review. Source code is not checked, nor are changelogs and release notes. A file with only other mentions of wtc is marked *review*; a sentence that explains the rename can stay."},
	{ID: "task-metadata", Area: "repository", Old: "`x-wtc` in `.harness/dekit.tasks.yaml`", New: "`x-taba` (planned)", Support: SupportOldOnly, Owner: "you", Checked: true,
		Migrate: "No new name yet (#375). Keep `x-wtc`."},
	{ID: "command", Area: "tooling", Old: "`wtc` command", New: "`taba`", Support: SupportBothShip, Owner: "taba", Checked: false,
		Migrate: "Both commands ship in 0.2.x. Call `taba` in scripts; harness files are covered by the harness-references check."},
	{ID: "openclaw-plugin", Area: "OpenClaw", Old: "plugin id `wtc`, package `openclaw-plugin-wtc`, config `plugins.entries.wtc`, gateway state under `wtc/`", New: "`taba`", Support: SupportOldOnly, Owner: "taba", Checked: false,
		Migrate: "Nothing to do yet; `taba doctor --openclaw` will move the gateway config when the plugin is renamed (#375)."},
	{ID: "internal-names", Area: "internal", Old: "git refs `refs/wtc-catch-up/…`, `wtc-pr-<n>` remotes, review markers `wtc-review v1`, locks `.wtc-runtime.lock`, `.wtc-status-watchers.lock`, `.wtc-prs.lock`, MCP and generated-file markers, temporary files", New: "taba spellings", Support: SupportOldOnly, Owner: "taba", Checked: false,
		Migrate: "Nothing to do: taba reads and moves these itself. Locks keep their names while any supported client uses them."},
}

// ConventionFinding is one place a checked convention still uses its old name.
type ConventionFinding struct {
	ID      string `json:"id"`
	Area    string `json:"area"`
	Path    string `json:"path"`
	Detail  string `json:"detail,omitempty"`
	Migrate string `json:"migrate"`
	// Blocked findings have no supported new name yet, or are taba's to move.
	Blocked bool `json:"blocked,omitempty"`
	// Review findings only mention wtc in prose; they may stay.
	Review bool `json:"review,omitempty"`
}

func conventionByID(id string) Convention {
	for _, c := range Conventions {
		if c.ID == id {
			return c
		}
	}
	panic("unknown convention " + id)
}

func conventionFinding(id, path, detail string) ConventionFinding {
	c := conventionByID(id)
	return ConventionFinding{ID: id, Area: c.Area, Path: path, Detail: detail, Migrate: c.Migrate,
		Blocked: c.Support == SupportOldOnly || c.Owner == "taba"}
}

func conventionPathExists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// tabaSkillNames reports whether this release's embedded skills use the taba
// names; until then harness skills keep the old prefix.
func tabaSkillNames() bool {
	_, err := ReadDefault("skills/taba-start/SKILL.md")
	return err == nil
}

// ConventionFindings checks this collection, its harness and its repository
// siblings for wtc conventions. User-level names are checked separately by
// UserConventionFindings, once per machine.
func (c *Context) ConventionFindings() ([]ConventionFinding, error) {
	var out []ConventionFinding
	if !c.AdHoc && c.Harness != "" {
		found, err := harnessConventionFindings(c.Harness)
		if err != nil {
			return nil, err
		}
		out = append(out, found...)
	}
	if c.Collection != "" {
		out = append(out, collectionConventionFindings(c)...)
	}
	return out, nil
}

func harnessConventionFindings(harness string) ([]ConventionFinding, error) {
	var out []ConventionFinding
	rel := func(path string) string {
		if r, err := filepath.Rel(filepath.Dir(harness), path); err == nil {
			return filepath.ToSlash(r)
		}
		return path
	}
	if p := filepath.Join(harness, "wtc.toml"); conventionPathExists(p) {
		out = append(out, conventionFinding("harness-config", rel(p), ""))
	}
	if p := filepath.Join(harness, ".wtc-cli-version"); conventionPathExists(p) {
		out = append(out, conventionFinding("harness-pin", rel(p), ""))
	}
	for _, layer := range []string{"skills", filepath.Join("overlays", "skills")} {
		entries, err := os.ReadDir(filepath.Join(harness, layer))
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		for _, e := range entries {
			dir := filepath.Join(harness, layer, e.Name())
			if layer != "skills" {
				if p := filepath.Join(dir, ".wtc-base.sha256"); conventionPathExists(p) {
					out = append(out, conventionFinding("skill-base-digest", rel(p), ""))
				}
			}
			if strings.HasPrefix(e.Name(), "wtc-") {
				f := conventionFinding("harness-skills", rel(dir), "")
				f.Blocked = !tabaSkillNames()
				out = append(out, f)
			}
		}
	}
	if p := filepath.Join(harness, "hooks", "wtc"); conventionPathExists(p) {
		out = append(out, conventionFinding("harness-hooks", rel(p), ""))
	}
	if p := filepath.Join(harness, "overlays", "browse", "wtc-browse.lua"); conventionPathExists(p) {
		out = append(out, conventionFinding("browse-overlay", rel(p), ""))
	}
	refs, err := scanReferences(harness, rel)
	if err != nil {
		return nil, err
	}
	out = append(out, refs...)
	for _, name := range []string{"taba.toml", "wtc.toml"} {
		data, err := os.ReadFile(filepath.Join(harness, name))
		if err == nil && strings.Contains(string(data), legacyCLIMiseTool) {
			out = append(out, conventionFinding("mise-tool-key", rel(filepath.Join(harness, name)), "[mise.tools] names "+legacyCLIMiseTool))
			break
		}
	}
	return out, nil
}

var (
	wtcVariable = regexp.MustCompile(`\bWTC_[A-Z0-9_]+`)
	// wtcCommand matches `wtc <subcommand>`, not "wtc" as the noun for a
	// worktree collection, which stays. TestDoctorAllReportsConventionsFromTheWorkspaceRoot
	// checks that every CLI subcommand is in the list.
	wtcCommand = regexp.MustCompile("(?:^|[\\s;|&(`$\"'/])wtc\\s+(?:" + strings.Join(ConventionCommands, "|") + ")\\b")
	// wtcMention matches the word wtc anywhere else: "a wtc collection",
	// wtc-cli, .wtc-prs. WTC_ variables are counted separately.
	wtcMention = regexp.MustCompile(`(?i)\bwtc\b`)
)

// ConventionCommands are the subcommands and flags `taba doctor` looks for
// after `wtc`.
var ConventionCommands = []string{"add-repo", "agent-env", "browse", "catch-up", "checkouts", "commands", "conventions", "customize", "dev", "docs", "doctor", "down", "eject", "env", "harness", "host", "init", "logs", "mcp", "new", "open", "openclaw", "pr", "projects", "publish", "registry", "release-notes", "restart", "retire", "review", "secrets", "skills", "status", "svc", "up", "upgrade", "version", "--version", "--help", "help"}

// The reference scan reads the harness's top-level *.md, *.sh, taba.toml,
// wtc.toml and mise.toml, and every file under referenceRoots. It skips
// history (release-notes folders and top-level CHANGELOG/HISTORY files),
// .git and node_modules: those describe the past or are vendored, not
// instructions anyone runs today.
var referenceRoots = []string{"hooks", "skills", "overlays", "instructions", "scripts", "bin", "review"}

func scanReferences(harness string, rel func(string) string) ([]ConventionFinding, error) {
	var files []string
	entries, err := os.ReadDir(harness)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		name := e.Name()
		upper := strings.ToUpper(name)
		if strings.HasPrefix(upper, "CHANGELOG") || strings.HasPrefix(upper, "HISTORY") {
			continue
		}
		if e.Type().IsRegular() && (strings.HasSuffix(name, ".md") || strings.HasSuffix(name, ".sh") || name == "taba.toml" || name == "wtc.toml" || name == "mise.toml") {
			files = append(files, filepath.Join(harness, name))
		}
	}
	for _, root := range referenceRoots {
		err := filepath.WalkDir(filepath.Join(harness, root), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				if os.IsNotExist(err) {
					return filepath.SkipDir
				}
				return err
			}
			if d.IsDir() {
				if n := d.Name(); n == ".git" || n == "node_modules" || n == "release-notes" {
					return filepath.SkipDir
				}
				return nil
			}
			if d.Type().IsRegular() {
				files = append(files, path)
			}
			return nil
		})
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
	}
	return referenceFindings("harness-references", files, rel)
}

func referenceFindings(id string, files []string, rel func(string) string) ([]ConventionFinding, error) {
	var out []ConventionFinding
	for _, path := range files {
		r, err := countAllReferences(path)
		if err != nil {
			return nil, err
		}
		if r.total() == 0 {
			continue
		}
		f := conventionFinding(id, rel(path), r.detail())
		f.Review = r.vars+r.cmds+r.skills == 0
		out = append(out, f)
	}
	return out, nil
}

// oldSkillName matches an old `wtc-<name>` for a skill this release ships as
// `taba-<name>`; repository names such as wtc-cli are not skills.
var oldSkillName = func() *regexp.Regexp {
	var names []string
	paths, _ := DefaultPaths()
	for _, path := range paths {
		parts := strings.Split(path, "/")
		if len(parts) == 3 && parts[0] == "skills" && parts[2] == "SKILL.md" && strings.HasPrefix(parts[1], "taba-") {
			names = append(names, regexp.QuoteMeta(strings.TrimPrefix(parts[1], "taba-")))
		}
	}
	if len(names) == 0 {
		return nil
	}
	sort.Strings(names)
	return regexp.MustCompile(`\bwtc-(?:` + strings.Join(names, "|") + `)\b`)
}()

// references counts old names in one file and remembers where they are.
type references struct {
	vars, cmds, skills, mentions int
	lines                        []int
}

func (r references) total() int { return r.vars + r.cmds + r.skills + r.mentions }

func (r references) detail() string {
	var parts []string
	for _, p := range []struct {
		n    int
		what string
	}{{r.vars, "WTC_ variable(s)"}, {r.cmds, "wtc command(s)"}, {r.skills, "old skill name(s)"}, {r.mentions, "other mention(s) of wtc"}} {
		if p.n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", p.n, p.what))
		}
	}
	const shown = 10
	var lines []string
	for i, l := range r.lines {
		if i == shown {
			lines = append(lines, fmt.Sprintf("+%d more", len(r.lines)-shown))
			break
		}
		lines = append(lines, fmt.Sprint(l))
	}
	label := "line "
	if len(r.lines) > 1 {
		label = "lines "
	}
	return strings.Join(parts, ", ") + "; " + label + strings.Join(lines, ", ")
}

// countMentions counts the word wtc, leaving out `x-wtc`, which has no taba
// name yet and is reported as task-metadata.
func countMentions(line string) int {
	n := 0
	for _, m := range wtcMention.FindAllStringIndex(line, -1) {
		if !strings.HasSuffix(strings.ToLower(line[:m[0]]), "x-") {
			n++
		}
	}
	return n
}

func countAllReferences(path string) (references, error) {
	var r references
	f, err := os.Open(path)
	if err != nil {
		return r, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.Size() > 1<<20 {
		return r, err
	}
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 1<<20)
	for n := 1; scanner.Scan(); n++ {
		line := scanner.Text()
		if strings.IndexByte(line, 0) >= 0 {
			return references{}, nil
		}
		vars := len(wtcVariable.FindAllString(line, -1))
		cmds := len(wtcCommand.FindAllString(line, -1))
		skills := 0
		if oldSkillName != nil {
			skills = len(oldSkillName.FindAllString(line, -1))
		}
		// Every command and old skill name also contains the word.
		mentions := countMentions(line) - cmds - skills
		if mentions < 0 {
			mentions = 0
		}
		if vars+cmds+skills+mentions > 0 {
			r.vars, r.cmds, r.skills, r.mentions = r.vars+vars, r.cmds+cmds, r.skills+skills, r.mentions+mentions
			r.lines = append(r.lines, n)
		}
	}
	return r, scanner.Err()
}

var generatedStateNames = []string{".wtc", ".wtc-prs", ".wtc-status.json", ".wtc-status.md", ".last-wtc-status.yml", ".wtc-catch-up.json", ".wtc-catch-up.md", ".wtc-reviews", ".wtc-review-posted", ".wtc-status-watchers", ".wtc-review-mode", ".wtc-dev-mode.json"}

func collectionConventionFindings(c *Context) []ConventionFinding {
	var out []ConventionFinding
	root := c.Collection
	base := filepath.Base(root)
	if conventionPathExists(filepath.Join(root, "WTC-SCOPE.md")) {
		out = append(out, conventionFinding("scope-file", base+"/WTC-SCOPE.md", ""))
	}
	if n := countFileReferences(filepath.Join(root, ".env.collection.local")); n > 0 {
		out = append(out, conventionFinding("local-references", base+"/.env.collection.local", fmt.Sprintf("%d WTC_ variable(s)", n)))
	}
	if data, err := os.ReadFile(filepath.Join(root, "mise.toml")); err == nil && strings.Contains(string(data), legacyCLIMiseTool) {
		out = append(out, conventionFinding("mise-tool-key", base+"/mise.toml", ""))
	}
	var state []string
	for _, name := range generatedStateNames {
		if conventionPathExists(filepath.Join(root, name)) {
			state = append(state, name)
		}
	}
	if len(state) > 0 {
		out = append(out, conventionFinding("generated-state", base, strings.Join(state, ", ")))
	}
	for _, repo := range c.Registry.Repos {
		// The harness has its own, wider check.
		if dir := filepath.Join(root, repo.Name); dir == filepath.Clean(c.Harness) {
			continue
		}
		if found, err := repoReferenceFindings(root, repo.Name); err == nil {
			out = append(out, found...)
		}
		p := filepath.Join(root, repo.Name, ".harness", "dekit.tasks.yaml")
		if data, err := os.ReadFile(p); err == nil && strings.Contains(string(data), "x-wtc") {
			out = append(out, conventionFinding("task-metadata", base+"/"+repo.Name+"/.harness/dekit.tasks.yaml", ""))
		}
	}
	return out
}

func countFileReferences(path string) int {
	r, err := countAllReferences(path)
	if err != nil {
		return 0
	}
	return r.vars
}

// historyFile reports files that describe the past rather than instruct:
// changelogs, history and release notes keep the names they shipped with.
func historyFile(rel string) bool {
	for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
		if part == "release-notes" || part == "testdata" || part == "vendor" || part == "node_modules" {
			return true
		}
	}
	upper := strings.ToUpper(filepath.Base(rel))
	return strings.HasPrefix(upper, "CHANGELOG") || strings.HasPrefix(upper, "HISTORY")
}

// repoReferenceFile picks the tracked files people and agents follow or run:
// docs, scripts, configuration and CI. Source code is left out; old names
// there are usually deliberate compatibility code.
func repoReferenceFile(rel string) bool {
	if historyFile(rel) {
		return false
	}
	rel = filepath.ToSlash(rel)
	base := filepath.Base(rel)
	if strings.Contains(strings.ToLower(base), "lock") {
		return false
	}
	switch strings.ToLower(filepath.Ext(base)) {
	case ".md", ".mdx", ".txt", ".sh", ".bash", ".zsh", ".fish", ".toml", ".yml", ".yaml", ".envrc", ".mk":
		return true
	}
	switch base {
	case "Makefile", "Justfile", "justfile", "Dockerfile", "Procfile", ".envrc", "package.json":
		return true
	}
	if strings.HasPrefix(base, ".env") {
		return true
	}
	for _, dir := range []string{"bin/", "scripts/", "hooks/", ".github/", ".harness/"} {
		if strings.HasPrefix(rel, dir) || strings.Contains(rel, "/"+dir) {
			return filepath.Ext(base) == ""
		}
	}
	return false
}

// repoReferenceFindings scans one repository sibling's tracked files.
func repoReferenceFindings(collection, repo string) ([]ConventionFinding, error) {
	dir := filepath.Join(collection, repo)
	out, err := exec.Command("git", "-C", dir, "ls-files", "-z").Output()
	if err != nil {
		return nil, nil
	}
	var files []string
	for _, rel := range strings.Split(string(out), "\x00") {
		if rel != "" && repoReferenceFile(rel) {
			files = append(files, filepath.Join(dir, rel))
		}
	}
	base := filepath.Base(collection)
	return referenceFindings("repo-references", files, func(path string) string {
		r, err := filepath.Rel(collection, path)
		if err != nil {
			return path
		}
		return base + "/" + filepath.ToSlash(r)
	})
}

// UserConventionFindings checks the control root and the per-user
// directories under XDG settings or their defaults. An explicit
// WTC_DATA_HOME/TABA_DATA_HOME replaces the data directory outright, so a
// wtc-named default is not checked then.
func UserConventionFindings(configRoot string) []ConventionFinding {
	var out []ConventionFinding
	if configRoot != "" {
		if p := filepath.Join(configRoot, "wtc.env"); conventionPathExists(p) {
			out = append(out, conventionFinding("control-root-env", p, ""))
		}
	}
	home, _ := os.UserHomeDir()
	base := func(env, fallback string) string {
		if dir := os.Getenv(env); dir != "" {
			return dir
		}
		if home == "" {
			return ""
		}
		return filepath.Join(home, fallback)
	}
	if dir := base("XDG_CONFIG_HOME", ".config"); dir != "" && conventionPathExists(filepath.Join(dir, "wtc")) {
		out = append(out, conventionFinding("user-config-dir", filepath.Join(dir, "wtc"), ""))
	}
	if envcompat.Getenv("WTC_DATA_HOME") != "" {
		return out
	}
	if dir := base("XDG_DATA_HOME", filepath.Join(".local", "share")); dir != "" && conventionPathExists(filepath.Join(dir, "wtc")) {
		out = append(out, conventionFinding("user-data-dir", filepath.Join(dir, "wtc"), ""))
	}
	return out
}

// ConventionsMarkdown renders the conventions list for docs/conventions.md.
func ConventionsMarkdown() string {
	var b strings.Builder
	areas := []string{}
	seen := map[string]bool{}
	for _, c := range Conventions {
		if !seen[c.Area] {
			seen[c.Area] = true
			areas = append(areas, c.Area)
		}
	}
	support := map[string]string{
		SupportBoth:        "Both names; the old one wins if both exist",
		SupportBothNewWins: "Both names; the new one wins",
		SupportOldOnly:     "Old name only",
		SupportNew:         "New name written",
		SupportBothShip:    "Both ship",
	}
	for _, area := range areas {
		fmt.Fprintf(&b, "### %s\n\n", conventionAreaTitle(area))
		b.WriteString("| Old | New | In 0.2.x | Written by | `doctor` | Migrate |\n|---|---|---|---|---|---|\n")
		for _, c := range Conventions {
			if c.Area != area {
				continue
			}
			checked := "no"
			if c.Checked {
				checked = "yes"
			}
			label := support[c.Support]
			if c.SupportNote != "" {
				label = c.SupportNote
			}
			fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %s |\n", c.Old, c.New, label, c.Owner, checked, c.Migrate)
		}
		b.WriteString("\n")
	}
	return b.String()
}

func conventionAreaTitle(area string) string {
	switch area {
	case "harness":
		return "Harness"
	case "collection":
		return "Collection"
	case "user":
		return "User and control root"
	case "repository":
		return "Repositories"
	case "tooling":
		return "Command"
	case "internal":
		return "Internal names"
	}
	return area
}

// SortConventionFindings orders findings by area as documented, then path.
func SortConventionFindings(findings []ConventionFinding) {
	order := map[string]int{}
	for i, c := range Conventions {
		if _, ok := order[c.ID]; !ok {
			order[c.ID] = i
		}
	}
	sort.SliceStable(findings, func(i, j int) bool {
		if order[findings[i].ID] != order[findings[j].ID] {
			return order[findings[i].ID] < order[findings[j].ID]
		}
		return findings[i].Path < findings[j].Path
	})
}
