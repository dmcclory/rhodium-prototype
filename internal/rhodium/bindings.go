package rhodium

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"

	"rhodium/internal/tui/keys"
)

// globalBindings are always available. `ctrl+c` fires regardless of filter
// mode (multi-char keys always do); `q` stays filter-gated by the single-
// char rule so typing q in a filter doesn't exit the app; `?` is explicitly
// Unfiltered so help opens even from a filter.
func globalBindings(a *app) []keys.Binding {
	return []keys.Binding{
		{
			Name: "quit", Keys: []string{"q", "ctrl+c"}, Desc: "quit", Group: "Global",
			Action: func() tea.Cmd { return tea.Quit },
		},
		{
			Name: "help", Keys: []string{"?"}, Desc: "help", Group: "Global",
			Unfiltered: true,
			Action: func() tea.Cmd {
				a.help.Open = true
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
func agentBindings(a *app) []keys.Binding {
	actions := a.cfg.ActionsResolved()
	out := make([]keys.Binding, 0, len(actions))
	for _, act := range actions {
		act := act // capture
		out = append(out, keys.Binding{
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
