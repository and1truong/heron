// Package tui renders the shared supervisor and observation snapshots. It never
// launches or signals processes directly.
package tui

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/and1truong/heron/internal/observe"
	"github.com/and1truong/heron/internal/process"
	"github.com/and1truong/heron/internal/supervisor"
)

type sample struct {
	At  time.Time
	CPU float64
	RSS uint64
}
type app struct {
	supervisor.Snapshot
	Processes []process.Stats
	CPU       float64
	RSS       uint64
}
type update struct {
	apps []app
	err  error
}
type result struct {
	id, action string
	err        error
}
type model struct {
	apps                       []app
	selected                   string
	filter, logFilter, input   string
	search                     bool
	focus, maximize            int
	events, expanded, help     bool
	offset                     int
	logEnd                     uint64
	processIndex               int
	processID                  process.Identity
	confirm, confirmID, status string
	busy                       bool
	history                    map[string][]sample
}

func collect(ctx context.Context, sup *supervisor.Supervisor, tracker process.Tracker, sampler *process.Sampler) update {
	before := sup.Snapshots()
	ctx, cancel := context.WithTimeout(ctx, 750*time.Millisecond)
	defer cancel()
	stats, err := tracker.Snapshot(ctx)
	stats = sampler.Sample(time.Now(), stats)
	u := update{err: err}
	after := sup.Snapshots()
	for i, s := range after {
		a := app{Snapshot: s}
		if i < len(before) && before[i].PID == s.PID && before[i].StartedAt == s.StartedAt && err == nil {
			a.Processes = process.Owned(s.PID, stats)
			for _, p := range a.Processes {
				a.CPU += p.CPU
				a.RSS += p.RSS
			}
		}
		u.apps = append(u.apps, a)
	}
	return u
}

func Run(ctx context.Context, sup *supervisor.Supervisor, store *observe.Store) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	keys, restore, err := openTerminal(ctx)
	if err != nil {
		return err
	}
	defer restore()
	updates := make(chan update, 1)
	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		tracker := process.OSTracker{}
		sampler := &process.Sampler{}
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		for {
			u := collect(ctx, sup, tracker, sampler)
			select {
			case updates <- u:
			case <-ctx.Done():
				return
			}
			select {
			case <-tick.C:
			case <-ctx.Done():
				return
			}
		}
	}()
	defer func() { cancel(); <-workerDone }()
	m := model{history: make(map[string][]sample)}
	results := make(chan result, 1)
	redraw := time.NewTicker(200 * time.Millisecond)
	defer redraw.Stop()
	last := ""
	render := func() {
		w, h := terminalSize()
		frame := m.view(w, h, store)
		if frame != last {
			fmt.Fprint(os.Stdout, "\x1b[H"+frame)
			last = frame
		}
	}
	render()
	for {
		select {
		case <-ctx.Done():
			return nil
		case u := <-updates:
			m.apps = u.apps
			if u.err != nil {
				m.status = u.err.Error()
			}
			for _, a := range m.apps {
				if len(a.Processes) == 0 {
					continue
				}
				h := m.history[a.ID]
				if len(h) == 120 {
					h = h[1:]
				}
				m.history[a.ID] = append(h, sample{time.Now(), a.CPU, a.RSS})
			}
			m.reselect()
		case r := <-results:
			m.busy = false
			m.status = r.id + ": " + r.action + " complete"
			if r.err != nil {
				m.status = r.id + ": " + r.err.Error()
			}
		case chunk, ok := <-keys:
			if !ok {
				return nil
			}
			m.input += chunk
			for {
				key, rest := nextKey(m.input)
				m.input = rest
				if key == "" {
					break
				}
				quit, action, id := m.key(key)
				if quit {
					return nil
				}
				if action != "" {
					m.busy = true
					m.status = id + ": " + action + "…"
					go func(action, id string) { err := sup.Action(ctx, id, action); results <- result{id, action, err} }(action, id)
				}
			}
		case <-redraw.C:
		}
		render()
	}
}

// Parse escape sequences across read boundaries; never treat pasted bytes as
// part of a terminal control sequence in the renderer.
func nextKey(s string) (string, string) {
	if s == "" {
		return "", ""
	}
	if s[0] == 27 {
		if len(s) == 1 {
			return "", s
		}
		if s[1] == '[' || s[1] == 'O' {
			for i := 2; i < len(s); i++ {
				if s[i] >= 0x40 && s[i] <= 0x7e {
					return s[:i+1], s[i+1:]
				}
			}
			if len(s) > 32 {
				return "escape", ""
			}
			return "", s
		}
		return "escape", s[1:]
	}
	return s[:1], s[1:]
}

func (m *model) visible() []app {
	var apps []app
	for _, a := range m.apps {
		if strings.Contains(strings.ToLower(a.ID), strings.ToLower(m.filter)) {
			apps = append(apps, a)
		}
	}
	return apps
}
func (m *model) reselect() {
	apps := m.visible()
	for _, a := range apps {
		if a.ID == m.selected {
			return
		}
	}
	m.selected = ""
	if len(apps) > 0 {
		m.selected = apps[0].ID
	}
	m.offset = 0
	m.processIndex = 0
}
func (m *model) current() app {
	for _, a := range m.apps {
		if a.ID == m.selected {
			return a
		}
	}
	return app{}
}
func (m *model) key(k string) (bool, string, string) {
	if k == "\x03" {
		return true, "", ""
	}
	if m.confirm != "" {
		action, id := m.confirm, m.confirmID
		m.confirm = ""
		if k == "y" {
			if action == "quit" {
				return true, "", ""
			}
			if !m.busy {
				return false, action, id
			}
		}
		return false, "", ""
	}
	if m.search {
		target := &m.filter
		if m.focus == 3 {
			target = &m.logFilter
		}
		switch k {
		case "\r", "\n":
			m.search = false
		case "escape", "\x1b":
			m.search = false
			*target = ""
		case "\x7f", "\b":
			if len(*target) > 0 {
				*target = (*target)[:len(*target)-1]
			}
		default:
			if len(k) == 1 && k[0] >= 32 && k[0] < 127 && len(*target) < 100 {
				*target += k
			}
		}
		m.reselect()
		m.offset = 0
		return false, "", ""
	}
	switch k {
	case "q":
		m.confirm = "quit"
		m.confirmID = ""
	case "?":
		m.help = !m.help
	case "1", "2", "3":
		p := int(k[0] - '0')
		if m.maximize == p {
			m.maximize = 0
		} else {
			m.maximize = p
		}
		m.focus = p
	case "\t":
		m.focus = m.focus%3 + 1
	case "l":
		m.focus = 3
		m.events = false
		m.offset = 0
	case "e":
		m.focus = 3
		m.events = true
		m.offset = 0
	case "p":
		m.focus = 2
	case "/":
		m.search = true
		if m.focus != 3 {
			m.focus = 1
		}
	case "\r", "\n":
		if m.focus == 2 {
			m.expanded = !m.expanded
		}
	case "\x1b[A", "\x1b[B", "j":
		delta := 1
		if k == "\x1b[A" {
			delta = -1
		}
		m.move(delta)
	case "\x1b[5~":
		m.offset += 10
		m.logEnd = 0
	case "\x1b[6~":
		m.offset = max(0, m.offset-10)
		m.logEnd = 0
	case "g":
		m.offset = 0
	case "S", "x", "r", "k":
		if m.busy || m.selected == "" {
			break
		}
		action := map[string]string{"S": "start", "x": "stop", "r": "restart", "k": "kill"}[k]
		if action == "kill" {
			m.confirm = action
			m.confirmID = m.selected
			break
		}
		return false, action, m.selected
	}
	return false, "", ""
}

func (m *model) move(delta int) {
	if m.focus == 3 {
		m.offset = max(0, m.offset-delta)
		m.logEnd = 0
		return
	}
	if m.focus == 2 {
		ps := treeOrder(m.current().Processes)
		if len(ps) > 0 {
			m.processIndex = max(0, min(len(ps)-1, m.processIndex+delta))
			m.processID = ps[m.processIndex].Identity
		}
		return
	}
	apps := m.visible()
	for i, a := range apps {
		if a.ID == m.selected {
			m.selected = apps[max(0, min(len(apps)-1, i+delta))].ID
			m.offset = 0
			m.processIndex = 0
			m.processID = process.Identity{}
			return
		}
	}
}

func clean(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == 0x2028 || r == 0x2029 {
			return ' '
		}
		return r
	}, s)
}
func fit(s string, w int) string {
	if w <= 0 {
		return ""
	}
	s = clean(s)
	var b strings.Builder
	used := 0
	for _, r := range s {
		width := 1
		if r > 127 {
			width = 2
		}
		if used+width > w {
			break
		}
		b.WriteRune(r)
		used += width
	}
	return b.String() + strings.Repeat(" ", w-used)
}
func memory(n uint64) string { return fmt.Sprintf("%.1fM", float64(n)/(1024*1024)) }

func (m *model) view(w, h int, store *observe.Store) string {
	w = max(1, w)
	h = max(1, h)
	focus := map[int]string{0: "apps", 1: "apps", 2: "processes", 3: "logs/events"}[m.focus]
	lines := []string{fmt.Sprintf(" HERON  %d apps | focus: %s | q quit | ? help", len(m.apps), focus)}
	if w < 60 || h < 12 {
		lines = append(lines, " Terminal too small; resize to at least 60 x 12.")
	} else {
		bodyH := h - 3
		left := m.appLines(bodyH)
		detail := m.detailLines(bodyH / 2)
		logs := m.logLines(store, bodyH-len(detail))
		switch m.maximize {
		case 1:
			lines = append(lines, pad(left, bodyH)...)
		case 2:
			lines = append(lines, pad(m.detailLines(bodyH), bodyH)...)
		case 3:
			lines = append(lines, pad(m.logLines(store, bodyH), bodyH)...)
		default:
			lw := max(24, min(40, w/3))
			left = pad(left, bodyH)
			right := pad(append(detail, logs...), bodyH)
			for i := 0; i < bodyH; i++ {
				lines = append(lines, fit(left[i], lw)+" | "+fit(right[i], w-lw-3))
			}
		}
	}
	status := m.status
	if m.confirm != "" {
		status = fmt.Sprintf("Confirm %s %s? [y/N] (quit stops all apps)", m.confirm, m.confirmID)
	}
	if m.search {
		target := m.filter
		if m.focus == 3 {
			target = m.logFilter
		}
		status = "Filter: /" + target + "_  Enter apply; Ctrl-C quit"
	}
	lines = append(lines, status, " Arrows select/scroll | S start x stop r restart k kill | 1/2/3 maximize")
	if m.help {
		lines = []string{" HERON HELP (? close)", " Arrows: select app/process or scroll logs; Tab: focus pane", " 1/2/3: maximize apps/processes/logs; same key restores split", " p: process tree; Enter: expand/collapse all; arrows: PID details", " l: logs; e: lifecycle events; /: filter focused apps or logs", " PgUp/PgDn: scroll logs; g: resume tail", " S: start; x: stop (blocks lazy-start); r: restart; k: confirmed kill", " q: confirm quit and stop all apps; Ctrl-C: stop all and quit", " CPU: 100% = one core; RSS sum may double-count shared pages", " Detached/external processes outside launch group: metrics unavailable", " Start uses configured idle timeout; TUI does not pin apps awake"}
	}
	lines = pad(lines, h)
	for i := range lines {
		lines[i] = fit(lines[i], w) + "\x1b[K"
	}
	return strings.Join(lines, "\r\n")
}
func pad(lines []string, n int) []string {
	if len(lines) > n {
		return lines[:n]
	}
	for len(lines) < n {
		lines = append(lines, "")
	}
	return lines
}

func (m *model) appLines(height int) []string {
	lines := []string{" APPS  /" + m.filter}
	apps := m.visible()
	selected := 0
	for i, a := range apps {
		if a.ID == m.selected {
			selected = i
		}
	}
	start := max(0, selected-max(1, (height-1)/2)+1)
	for _, a := range apps[start:] {
		prefix := "  "
		if a.ID == m.selected {
			prefix = "> "
		}
		stats := "-"
		if len(a.Processes) > 0 {
			stats = fmt.Sprintf("%.0f%% %s", a.CPU, memory(a.RSS))
		}
		lines = append(lines, fmt.Sprintf("%s%s [%s]", prefix, a.ID, a.State), "   "+stats)
	}
	return lines
}
func treeOrder(ps []process.Stats) []process.Stats {
	ps = append([]process.Stats(nil), ps...)
	sort.Slice(ps, func(i, j int) bool { return ps[i].PID < ps[j].PID })
	pids := make(map[int]bool)
	for _, p := range ps {
		pids[p.PID] = true
	}
	var result []process.Stats
	seen := make(map[int]bool)
	var visit func(process.Stats)
	visit = func(p process.Stats) {
		if seen[p.PID] {
			return
		}
		seen[p.PID] = true
		result = append(result, p)
		for _, child := range ps {
			if child.PPID == p.PID {
				visit(child)
			}
		}
	}
	for _, p := range ps {
		if !pids[p.PPID] {
			visit(p)
		}
	}
	for _, p := range ps {
		visit(p)
	}
	return result
}
func (m *model) detailLines(height int) []string {
	a := m.current()
	if a.ID == "" {
		return pad([]string{" No matching app"}, height)
	}
	uptime := "-"
	if a.State == supervisor.StateRunning {
		uptime = time.Since(a.StartedAt).Round(time.Second).String()
	}
	lines := []string{fmt.Sprintf(" %s  %s  uptime %s  restarts %d", a.ID, a.State, uptime, a.Restarts)}
	if len(a.DependsOn) > 0 {
		lines = append(lines, fmt.Sprintf(" Dependencies: %s", strings.Join(a.DependsOn, ", ")))
	}
	if len(a.ActiveDependents) > 0 {
		lines = append(lines, fmt.Sprintf(" Kept alive by: %s", strings.Join(a.ActiveDependents, ", ")))
	}
	if len(a.Processes) == 0 {
		return pad(append(lines, " CPU/RSS: unavailable (no live owned process group)"), height)
	}
	lines = append(lines, fmt.Sprintf(" CPU %.1f%% (%.2f cores) RSS %s | history %d/120", a.CPU, a.CPU/100, memory(a.RSS), len(m.history[a.ID])), " PROCESS TREE (p focus; Enter expand/collapse)")
	ps := treeOrder(a.Processes)
	for i, p := range ps {
		if p.Identity == m.processID {
			m.processIndex = i
		}
	}
	m.processIndex = max(0, min(m.processIndex, len(ps)-1))
	m.processID = ps[m.processIndex].Identity
	selected := ps[m.processIndex]
	lines = append(lines, fmt.Sprintf(" PID %d PPID %d VMS %s", selected.PID, selected.PPID, memory(selected.VMS)))
	if !m.expanded {
		return pad(append(lines, fmt.Sprintf(" + %s (%d processes)", ps[0].Command, len(ps))), height)
	}
	depths := make(map[int]int)
	rows := make([]string, 0, len(ps))
	for i, p := range ps {
		depth := 0
		if d, ok := depths[p.PPID]; ok {
			depth = d + 1
		}
		depths[p.PID] = depth
		prefix := "  "
		if i == m.processIndex {
			prefix = "> "
		}
		rows = append(rows, fmt.Sprintf("%s%s%s  %.1f%% %s", prefix, strings.Repeat("  ", min(depth, 5)), p.Command, p.CPU, memory(p.RSS)))
	}
	start := max(0, m.processIndex-max(1, height-len(lines))+1)
	return pad(append(lines, rows[start:]...), height)
}
func (m *model) logLines(store *observe.Store, height int) []string {
	title := " LOGS"
	if m.events {
		title = " EVENTS"
	}
	if m.offset == 0 {
		title += " [tail]"
	} else {
		title += " [paused]"
	}
	entries := store.Entries(m.selected, m.events)
	var rows []string
	var sequences []uint64
	for _, e := range entries {
		text := fmt.Sprintf(" %s %s %s", e.At.Format("15:04:05"), e.Stream, e.Text)
		if strings.Contains(strings.ToLower(text), strings.ToLower(m.logFilter)) {
			rows = append(rows, text)
			sequences = append(sequences, e.Seq)
		}
	}
	if m.offset == 0 {
		m.logEnd = 0
	}
	m.offset = min(m.offset, max(0, len(rows)-1))
	end := len(rows) - m.offset
	if m.offset > 0 && m.logEnd != 0 {
		end = 0
		for i, seq := range sequences {
			if seq <= m.logEnd {
				end = i + 1
			}
		}
		if end == 0 && len(rows) > 0 {
			end = 1
		}
		m.offset = len(rows) - end
	}
	if m.offset > 0 && end > 0 {
		m.logEnd = sequences[end-1]
	}
	start := max(0, end-max(0, height-1))
	return pad(append([]string{title + " /" + m.logFilter}, rows[start:end]...), height)
}
