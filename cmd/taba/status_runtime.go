package main

import (
	"net/url"
	"slices"
	"strings"
	"unicode"

	"taba.sh/taba/internal/wtc"
)

// statusRuntimeGroup is one line of the detail table: a service with its
// tunnels, or a repository's resource hooks.
type statusRuntimeGroup struct {
	repo, name string
	members    []wtc.RuntimeItem
	local      string
	localLive  bool              // the service with the local URL is running
	localTone  string            // its state colour
	tunnels    []wtc.RuntimeItem // shown endpoints, in task order
	notes      []string
}

func statusRuntimeGroups(snapshot wtc.StatusSnapshot) []statusRuntimeGroup {
	var groups []statusRuntimeGroup
	index := map[string]int{}
	for _, item := range snapshot.Runtime {
		key := item.Collection + "\x00" + item.Group
		at, ok := index[key]
		if !ok {
			at, index[key] = len(groups), len(groups)
			groups = append(groups, statusRuntimeGroup{repo: item.Dir, name: strings.TrimPrefix(item.Group, item.Dir+"/")})
		}
		g := &groups[at]
		g.members = append(g.members, item)
		if item.Kind == "endpoint" {
			if statusRuntimeTunnelShown(item) {
				g.tunnels = append(g.tunnels, item)
			}
		} else if item.URL != "" && g.local == "" {
			g.local, g.localLive = item.URL, statusRuntimeLive(item)
			_, g.localTone = statusRuntimeMark(item)
		}
		for _, note := range []string{item.Reason, item.Error} {
			if note != "" {
				g.notes = append(g.notes, note)
			}
		}
	}
	return groups
}

// statusRuntimeSlug names a tunnel address within its provider's column: the
// port of a tailnet address (the host is this machine), otherwise the first
// label of the host, such as calm-river of calm-river.trycloudflare.com.
func statusRuntimeSlug(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return raw
	}
	host := u.Hostname()
	if wtc.RuntimeTunnelProvider(raw) == "tailscale" && u.Port() != "" && u.Port() != "443" {
		return ":" + u.Port()
	}
	if wtc.RuntimeTunnelProvider(raw) != "" {
		return strings.SplitN(host, ".", 2)[0]
	}
	return host
}

// statusRuntimeURLLabel is the short clickable name of a URL: the port for a
// loopback address, the provider for a known tunnel domain, else the host.
func statusRuntimeURLLabel(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return raw
	}
	host := u.Hostname()
	if host == "localhost" || strings.HasPrefix(host, "127.") || host == "::1" {
		if u.Port() != "" {
			return ":" + u.Port()
		}
		return host
	}
	if provider := wtc.RuntimeTunnelProvider(raw); provider != "" {
		return provider
	}
	return host
}

// state is the member state that most needs attention, with its tone.
func (g statusRuntimeGroup) state() (string, string) {
	rank := map[string]int{statusToneDim: 1, statusToneLabel: 2, statusToneSuccess: 2, statusToneWarning: 3, statusToneFailure: 4}
	state, tone := "", ""
	for _, item := range g.members {
		if _, t := statusRuntimeMark(item); rank[t] > rank[tone] {
			state, tone = statusRuntimeState(item), t
		}
	}
	return state, tone
}

type statusRuntimeLayout struct {
	marks, repo, name, state, local int
	tunnels                         []statusRuntimeColumn
}

// statusRuntimeColumn is one tunnel provider's column, such as TAILSCALE.
type statusRuntimeColumn struct {
	provider string
	width    int
}

func (c statusRuntimeColumn) header() string {
	if c.provider == "" {
		return "TUNNEL"
	}
	return strings.ToUpper(c.provider)
}

// statusRuntimeTunnelShown includes tunnels in use, declaration defaults,
// explicit on choices and lifecycle problems.
func statusRuntimeTunnelShown(item wtc.RuntimeItem) bool {
	switch item.State {
	case "starting", "running", "ready", "failed", "exited":
		return true
	}
	return item.URL != "" || item.Autostart || item.TunnelIntent == "on" || item.Lifecycle != ""
}

func statusTUIRuntimeLayout(snapshot wtc.StatusSnapshot, width int) statusRuntimeLayout {
	l := statusRuntimeLayout{marks: len("KEY"), repo: statusTUIPRLayout(width).repo, name: 7, state: 11, local: 5}
	index := map[string]int{}
	for _, g := range statusRuntimeGroups(snapshot) {
		l.marks = max(l.marks, len(statusRuntimeSlots(g.members)))
		l.name = max(l.name, len([]rune(g.name)))
		l.local = max(l.local, len([]rune(statusRuntimeURLLabel(g.local))), len([]rune(g.resourceType())))
		for _, provider := range statusRuntimeProviders(g.tunnels) {
			at, ok := index[provider]
			if !ok {
				at, index[provider] = len(l.tunnels), len(l.tunnels)
				l.tunnels = append(l.tunnels, statusRuntimeColumn{provider: provider})
				l.tunnels[at].width = len(l.tunnels[at].header())
			}
			l.tunnels[at].width = max(l.tunnels[at].width, statusRuntimeLinksWidth(statusRuntimeOf(g.tunnels, provider)))
		}
	}
	l.name, l.local = min(l.name, max(7, width/6)), min(l.local, max(5, width/3))
	return l
}

func statusRuntimeProviders(tunnels []wtc.RuntimeItem) []string {
	var providers []string
	for _, item := range tunnels {
		if !slices.Contains(providers, item.Provider) {
			providers = append(providers, item.Provider)
		}
	}
	return providers
}

func statusRuntimeOf(tunnels []wtc.RuntimeItem, provider string) []wtc.RuntimeItem {
	var of []wtc.RuntimeItem
	for _, item := range tunnels {
		if item.Provider == provider {
			of = append(of, item)
		}
	}
	return of
}

func (l statusRuntimeLayout) localStart() int {
	return l.marks + 1 + l.repo + 1 + l.name + 1 + l.state + 1
}

// tunnelStart is where the column of a provider begins, or -1.
func (l statusRuntimeLayout) tunnelStart(provider string) int {
	start := l.localStart() + l.local + 1
	for _, c := range l.tunnels {
		if c.provider == provider {
			return start
		}
		start += c.width + 1
	}
	return -1
}

// statusRuntimeTunnelLabel shows the address or state with an explicit label
// for public or unknown exposure.
func statusRuntimeTunnelLabel(item wtc.RuntimeItem) string {
	label := statusRuntimeState(item)
	if item.URL != "" {
		label = statusRuntimeSlug(item.URL)
	}
	if item.Exposure == wtc.ExposurePublic {
		return label + " [public]"
	}
	if statusRuntimeExposed(item) {
		return label + " [exposure unknown]"
	}
	return label
}

func statusRuntimeLinksWidth(tunnels []wtc.RuntimeItem) int {
	width := 0
	for i, item := range tunnels {
		if i > 0 {
			width += 2
		}
		width += len([]rune(statusRuntimeTunnelLabel(item)))
	}
	return width
}

// urlAt is the URL drawn at a column of a group's line.
func (l statusRuntimeLayout) urlAt(g statusRuntimeGroup, x int) string {
	if x >= l.localStart() && x < l.localStart()+min(l.local, len([]rune(statusRuntimeURLLabel(g.local)))) {
		if !g.localLive {
			return ""
		}
		return g.local
	}
	for _, c := range l.tunnels {
		start := l.tunnelStart(c.provider)
		for _, item := range statusRuntimeOf(g.tunnels, c.provider) {
			label := len([]rune(statusRuntimeTunnelLabel(item)))
			if x >= start && x < start+label {
				if !statusRuntimeLive(item) {
					return ""
				}
				return item.URL
			}
			start += label + 2
		}
	}
	return ""
}

// statusRuntimeState shortens the hook fact the way the repo summary does.
func statusRuntimeState(item wtc.RuntimeItem) string {
	switch {
	case item.State == "unknown":
		return "unknown"
	case item.TunnelIntent == "stale":
		return "choice stale"
	case item.Lifecycle != "":
		return "tunnel missing"
	case item.Kind == "resources" && item.State == "provision-hook-completed":
		return "provisioned"
	case item.State == "absent" || item.State == "idle":
		// No runner, or a task it is not running: stopped, in plain words.
		return "stopped"
	case statusRuntimeTrouble(item) && item.Answering == "no":
		return "no answer"
	case statusRuntimeTrouble(item):
		return "login"
	}
	return item.State
}

// statusRuntimeTrouble includes tunnel lifecycle problems and live tasks that
// cannot answer or whose provider cannot serve: ready is only a startup probe.
func statusRuntimeTrouble(item wtc.RuntimeItem) bool {
	running := item.State == "ready" || item.State == "running"
	return item.Lifecycle != "" || running && (item.Answering == "no" || item.Login != "")
}

// statusTUIRuntimeLines draws one line per service in the style of the PR
// list: the LOCAL marks of the service and its tunnels, repo, name, the state
// that most needs attention, the local URL, then one column per tunnel
// provider holding the tunnel's slug; hover and click give the full address.
func statusTUIRuntimeLines(snapshot wtc.StatusSnapshot, width int, styled bool, position ...func(int, statusRuntimeGroup)) []string {
	if len(snapshot.Runtime) == 0 {
		return nil
	}
	l := statusTUIRuntimeLayout(snapshot, width)
	heading := "SERVICES"
	header := statusTUIFit("KEY", l.marks) + " " + statusTUIFit("REPO", l.repo) + " " + statusTUIFit("NAME", l.name) + " " +
		statusTUIFit("STATE", l.state) + " " + statusTUIFit("URL", l.local)
	for _, c := range l.tunnels {
		header += " " + statusTUIFit(c.header(), c.width)
	}
	header = statusTUIFit(header, width)
	if styled {
		heading, header = statusTUIStyle(heading, statusToneHeading), statusTUIStyle(header, statusToneDim)
	}
	lines := []string{"", heading, header}
	for _, g := range statusRuntimeGroups(snapshot) {
		marks := statusRuntimeCell(g.members, l.marks, styled)
		state, tone := g.state()
		repo, name := statusTUIFit(statusTUISafe(g.repo), l.repo), statusTUIFit(statusTUISafe(g.name), l.name)
		stateCell := statusTUIFit(statusTUISafe(state), l.state)
		local := statusTUIFit(statusTUISafe(statusRuntimeURLLabel(g.local)), l.local)
		kind := g.resourceType()
		if styled {
			repo, name, stateCell = statusTUIStyle(repo, statusToneLabel), statusTUIStyle(name, statusToneLabel), statusTUIStyle(stateCell, tone)
			// The port takes its service's state colour: grey before it starts,
			// green while it runs and answers, red when it failed or doesn't
			// answer now; a link only while it runs.
			if statusTUIURL(g.local) != "" && g.localLive {
				local = statusTUIFitANSI(statusTUILinkTone(strings.TrimRight(local, " "), g.local, g.localTone), l.local)
			} else if g.local != "" {
				local = statusTUIStyle(local, g.localTone)
			}
		}
		if kind != "" && g.local == "" {
			// A resource has no address; say what it is instead.
			local = statusTUIFit(statusTUISafe(kind), l.local)
			if styled {
				local = statusTUIStyle(local, statusToneDim)
			}
		}
		var tail string
		for n, c := range l.tunnels {
			if n > 0 {
				tail += " "
			}
			of := statusRuntimeOf(g.tunnels, c.provider)
			for i, item := range of {
				if i > 0 {
					tail += "  "
				}
				text := statusTUISafe(statusRuntimeTunnelLabel(item))
				// Unknown exposure needs attention; public exposure is labeled.
				// Known exposure follows the task's health color.
				_, tone := statusRuntimeMark(item)
				if item.Exposure != wtc.ExposurePublic && statusRuntimeExposed(item) && statusRuntimeLive(item) && !statusRuntimeTrouble(item) {
					tone = statusToneWarning
				}
				if styled && statusTUIURL(item.URL) != "" && statusRuntimeLive(item) {
					text = statusTUILinkTone(text, item.URL, tone)
				} else if styled {
					text = statusTUIStyle(text, tone)
				}
				tail += text
			}
			tail += strings.Repeat(" ", max(0, c.width-statusRuntimeLinksWidth(of)))
		}
		if notes := statusTUISafe(strings.Join(g.notes, " · ")); notes != "" {
			if styled {
				notes = statusTUIStyle(notes, statusToneDim)
			}
			tail += " " + notes
		}
		line := marks + " " + repo + " " + name + " " + stateCell + " " + local
		if tail != "" {
			line += " " + tail
		}
		if styled {
			line = statusTUIFitANSI(line, width)
		} else {
			line = statusTUIFit(line, width)
		}
		if len(position) > 0 {
			position[0](len(lines), g)
		}
		lines = append(lines, line)
	}
	return lines
}

// statusRuntimeItems lists the services, tunnels and resource hooks a repo row owns.
func statusRuntimeItems(snapshot wtc.StatusSnapshot, row wtc.StatusRepo) []wtc.RuntimeItem {
	var items []wtc.RuntimeItem
	for _, item := range snapshot.Runtime {
		if item.Repo == row.Repo && item.Collection == row.Collection {
			items = append(items, item)
		}
	}
	return items
}

// statusRuntimeMark is one letter for a task, toned by its state, and red
// for a live task that doesn't answer now or needs a provider login. A configured
// mark wins. Otherwise a service takes the first letter of its service name
// (w for web), a tunnel the first letter of its own name (t for tunnel), in
// upper case when it is public, and resource hooks are r.
func statusRuntimeMark(item wtc.RuntimeItem) (string, string) {
	first := func(name string) rune {
		for _, r := range strings.TrimPrefix(name[strings.LastIndex(name, "/")+1:], "@") {
			if unicode.IsLetter(r) || unicode.IsDigit(r) {
				return unicode.ToLower(r)
			}
		}
		return '?'
	}
	letter := first(item.Group)
	if item.Kind == "endpoint" {
		letter = first(item.Path)
		if statusRuntimeExposed(item) {
			letter = unicode.ToUpper(letter)
		}
	}
	if item.Mark != "" {
		letter = []rune(item.Mark)[0]
	}
	tone := statusToneFailure
	if item.Kind == "resources" {
		// A resource exists or not; it does not run, so it is never green.
		switch item.State {
		case "provision-hook-completed":
			return string(letter), statusToneLabel
		case "provision-attempted":
			return string(letter), statusToneWarning
		case "checking", "unprovisioned":
			return string(letter), statusToneDim
		}
		return string(letter), statusToneFailure
	}
	if statusRuntimeTrouble(item) {
		return string(letter), statusToneFailure
	}
	switch item.State {
	case "ready", "provision-hook-completed", "done":
		tone = statusToneSuccess
	case "starting", "running", "provision-attempted":
		tone = statusToneWarning
	case "absent", "unconfigured", "stopped", "idle", "checking", "unprovisioned":
		tone = statusToneDim
	}
	return string(letter), tone
}

// statusRuntimeSlot is one column of a LOCAL cell: the mark of items[item],
// or a separator when item is -1.
type statusRuntimeSlot struct {
	sep  string
	item int
}

// statusRuntimeSlots lays out marks per service: a service and its tunnels
// joined by "/", services apart by a space, so "w/t r" reads as web with its
// tunnel, then resources.
func statusRuntimeSlots(items []wtc.RuntimeItem) []statusRuntimeSlot {
	var keys []string
	members := map[string][]int{}
	for i, item := range items {
		key := item.Collection + "\x00" + item.Group
		if _, ok := members[key]; !ok {
			keys = append(keys, key)
		}
		members[key] = append(members[key], i)
	}
	var slots []statusRuntimeSlot
	for g, key := range keys {
		if g > 0 {
			slots = append(slots, statusRuntimeSlot{sep: " ", item: -1})
		}
		for m, i := range members[key] {
			if m > 0 {
				slots = append(slots, statusRuntimeSlot{sep: "/", item: -1})
			}
			slots = append(slots, statusRuntimeSlot{item: i})
		}
	}
	return slots
}

func statusRuntimeCell(items []wtc.RuntimeItem, width int, styled bool) string {
	var b strings.Builder
	slots := statusRuntimeSlots(items)
	for i, slot := range slots {
		if i >= width {
			break
		}
		if slot.item < 0 {
			text := slot.sep
			if styled && text != " " {
				text = statusTUIStyle(text, statusToneDim)
			}
			b.WriteString(text)
			continue
		}
		letter, tone := statusRuntimeMark(items[slot.item])
		if styled {
			// A letter with an address is a terminal link to it, like a PR number.
			if raw := statusTUIURL(items[slot.item].URL); raw != "" && statusRuntimeLive(items[slot.item]) {
				letter = statusTUILinkTone(letter, raw, tone)
			} else {
				letter = statusTUIStyle(letter, tone)
			}
		}
		b.WriteString(letter)
	}
	return b.String() + strings.Repeat(" ", max(0, width-min(len(slots), width)))
}

// statusRuntimeHover is the footer text for the mark under the pointer.
func statusRuntimeHover(item wtc.RuntimeItem) string {
	parts := []string{item.Path, item.Kind, item.State}
	if item.Kind == "endpoint" {
		parts[1] = statusRuntimeExposure(item)
	} else if item.Kind == "resources" {
		parts[1] = strings.TrimSpace(item.Type + " resource")
		if item.State == "provision-hook-completed" {
			parts[2] = "provisioned: created by the repo's provision hook, kept by down, removed by teardown or retire"
		}
	}
	answer := ""
	if item.Answering == "no" {
		answer = "not answering now: " + item.AnswerDetail
	}
	for _, extra := range []string{item.Label, item.URL, item.Reason, item.Error, answer, item.Login, item.Lifecycle} {
		if extra != "" {
			parts = append(parts, extra)
		}
	}
	return statusTUISafe(strings.Join(parts, " · "))
}

// statusRuntimeExposed is true for an endpoint that may be reachable from the
// internet: public, or not known to be protected.
func statusRuntimeExposed(item wtc.RuntimeItem) bool {
	if item.Kind != "endpoint" {
		return false
	}
	switch item.Exposure {
	case wtc.ExposureTailnet, wtc.ExposureAccount, wtc.ExposureProtected:
		return false
	}
	return true
}

// statusRuntimeExposure says who can reach an endpoint and how wtc knows.
func statusRuntimeExposure(item wtc.RuntimeItem) string {
	what := "tunnel"
	if item.Provider != "" {
		what = item.Provider + " tunnel"
	}
	reach := map[string]string{
		wtc.ExposureTailnet:   "tailnet only",
		wtc.ExposureAccount:   "allowed accounts only",
		wtc.ExposureProtected: "protected",
		wtc.ExposurePublic:    "public, reachable from the internet",
	}[item.Exposure]
	if reach == "" {
		return what + ", exposure unknown (treated as public; set x-wtc public: false if it is protected)"
	}
	how := map[string]string{
		wtc.ExposureVerified: "checked with the provider",
		wtc.ExposureDeclared: "declared",
		wtc.ExposureCommand:  "from its command",
		wtc.ExposureDefault:  "provider default",
	}[item.ExposureBasis]
	if how != "" {
		reach += " (" + how + ")"
	}
	return what + ", " + reach
}

// resourceType is the declared type of a resource line, such as postgres.
func (g statusRuntimeGroup) resourceType() string {
	for _, item := range g.members {
		if item.Kind == "resources" {
			return item.Type
		}
	}
	return ""
}

// statusRuntimeLive is true while a task runs, so its address can answer.
func statusRuntimeLive(item wtc.RuntimeItem) bool {
	switch item.State {
	case "ready", "running", "starting":
		return true
	}
	return false
}
