package rhodium

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
)

// Binding is a single key → action record. One shape drives dispatch, help
// overlay rendering, and (eventually) user-level key remapping. Keep it
// data-oriented: adding a feature should mean appending a Binding, not
// threading a new switch case through multiple files.
type Binding struct {
	Name       string        // stable id; future config can rebind by name
	Keys       []string      // all keys that trigger this binding (aliases)
	Desc       string        // shown in help overlay
	Group      string        // "Navigate" | "Mark" | "Notes" | "View" | "Agent" | "Global"
	Action     func() tea.Cmd // invoked on match; closures capture *app from their enclosing bindings() method
	Unfiltered bool          // if true, still fires while a bubbles list is in filter mode
}

// Group names — also the rendering order in the help overlay.
var groupOrder = []string{"Navigate", "Mark", "Notes", "Mention", "View", "Agent", "Global"}

// dispatch walks the given binding tables in order and fires the first
// match. Returns (cmd, true) when a key matched so callers know whether to
// fall through to bubbles' own default handling.
//
// Filter gating is per-key, not per-binding: during list filter mode,
// single-character keys (letters, digits, punctuation) are skipped so they
// type into the filter instead of triggering actions. Multi-char keys
// ("esc", "enter", "tab", "ctrl+c", arrows) always fire — they can't be
// typed into a filter anyway. `Unfiltered: true` is the explicit escape
// hatch for single-char keys that must still work during filter (e.g. `?`).
func dispatch(key string, filtering bool, tables ...[]Binding) (tea.Cmd, bool) {
	for _, tbl := range tables {
		for _, b := range tbl {
			for _, k := range b.Keys {
				if k != key {
					continue
				}
				if filtering && !b.Unfiltered && len(k) == 1 {
					continue
				}
				return b.Action(), true
			}
		}
	}
	return nil, false
}

// globalBindings are always available. `ctrl+c` fires regardless of filter
// mode (multi-char keys always do); `q` stays filter-gated by the single-
// char rule so typing q in a filter doesn't exit the app; `?` is explicitly
// Unfiltered so help opens even from a filter.
func globalBindings(a *app) []Binding {
	return []Binding{
		{
			Name: "quit", Keys: []string{"q", "ctrl+c"}, Desc: "quit", Group: "Global",
			Action: func() tea.Cmd { return tea.Quit },
		},
		{
			Name: "help", Keys: []string{"?"}, Desc: "help", Group: "Global",
			Unfiltered: true,
			Action: func() tea.Cmd {
				a.help.open = true
				return nil
			},
		},
		{
			// Bubble Tea swallows ctrl+z by default — we opt into shell
			// suspension by returning tea.Suspend, which restores the
			// terminal, sends SIGTSTP, and rewires alt-screen on resume.
			Name: "suspend", Keys: []string{"ctrl+z"}, Desc: "suspend (resume with `fg`)", Group: "Global",
			Action: func() tea.Cmd { return tea.Suspend },
		},
	}
}

// agentBindings turns each configured action into a Binding with Group
// "Agent". User-added harnesses in config show up in dispatch + help with
// no further wiring.
func agentBindings(a *app) []Binding {
	actions := a.cfg.ActionsResolved()
	out := make([]Binding, 0, len(actions))
	for _, act := range actions {
		act := act // capture
		out = append(out, Binding{
			Name:  "agent-" + act.Name,
			Keys:  []string{act.Key},
			Desc:  act.Name,
			Group: "Agent",
			Action: func() tea.Cmd {
				cmd, err := runAction(a, act)
				if err != nil {
					a.status.msg = fmt.Sprintf("%s: %s", act.Name, err.Error())
					return nil
				}
				return cmd
			},
		})
	}
	return out
}
