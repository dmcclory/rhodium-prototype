package diff

import (
	"fmt"
	"strings"

	"rhodium/internal/gh"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// mentionPicker is a small modal shown over the noting textarea when the
// user types `@` at a word boundary. It lists the repo's contributors
// (top-contributors first); subsequent keystrokes both extend the note
// text and filter the list against `query`. Pressing enter replaces the
// typed `@query` fragment with `@login ` and closes the modal.
//
// Contributors are fetched lazily on first open and cached on the Model
// keyed by repo. A picker opened before the fetch returns shows
// "loading…" until SetContributors lands.
type mentionPicker struct {
	open    bool
	loading bool
	query   string
	list    list.Model
}

type mentionItem struct {
	login         string
	contributions int
}

func (i mentionItem) Title() string       { return fmt.Sprintf("@%s", i.login) }
func (i mentionItem) Description() string { return fmt.Sprintf("%d contributions", i.contributions) }
func (i mentionItem) FilterValue() string { return i.login }

func newMentionPicker() mentionPicker {
	d := list.NewDefaultDelegate()
	d.ShowDescription = false
	d.SetSpacing(0)
	l := list.New(nil, d, 0, 0)
	l.SetShowHelp(false)
	l.SetShowTitle(false)
	l.SetShowStatusBar(false)
	l.SetFilteringEnabled(false)
	l.Title = ""
	return mentionPicker{list: l}
}

// filterContributors returns items whose login contains query as a
// case-insensitive substring. An empty query matches everything.
func filterContributors(contribs []gh.Contributor, query string) []list.Item {
	q := strings.ToLower(query)
	items := make([]list.Item, 0, len(contribs))
	for _, c := range contribs {
		if q == "" || strings.Contains(strings.ToLower(c.Login), q) {
			items = append(items, mentionItem{login: c.Login, contributions: c.Contributions})
		}
	}
	return items
}

var mentionBoxStyle = lipgloss.NewStyle().
	Border(lipgloss.RoundedBorder()).
	BorderForeground(lipgloss.Color("63")).
	Padding(0, 1)

// Render returns the bordered picker box. Positioning is the caller's
// job — see Model.View, which centers it over the diff body.
func (p *mentionPicker) Render() string {
	if p.loading {
		return mentionBoxStyle.Render("@-mention — loading contributors…")
	}
	return mentionBoxStyle.Render(p.list.View())
}

// openMentionPicker shows the @-picker over the textarea. Contributors
// are fetched lazily per repo; subsequent opens in the same session use
// the cache and are instant. The picker opens with an empty query
// showing the full contributor list.
func (m *Model) openMentionPicker() tea.Cmd {
	if m.pr == nil {
		return nil
	}
	repo := m.pr.Repo
	m.mention.open = true
	m.mention.query = ""
	if cached, ok := m.contributors[repo]; ok {
		m.mention.loading = false
		m.mention.list.SetItems(filterContributors(cached, ""))
		m.mention.list.ResetSelected()
		m.sizeMentionPicker()
		return nil
	}
	m.mention.loading = true
	m.mention.list.SetItems(nil)
	m.sizeMentionPicker()
	return func() tea.Msg { return LoadContributorsMsg{Repo: repo} }
}

func (m *Model) sizeMentionPicker() {
	w := m.width / 2
	if w < 30 {
		w = 30
	}
	h := 12
	if m.height < h+4 {
		h = m.height - 4
		if h < 5 {
			h = 5
		}
	}
	m.mention.list.SetSize(w, h)
}

// updateMentionKeys routes keys while the mention picker is open. The
// picker sits over the noting textarea and runs in a dual-input model:
// printable keys are forwarded to the textarea (so the user sees their
// `@query` building up) and also drive a live filter over the
// contributor list. Enter replaces the typed `@query` fragment with
// `@<login> `; esc leaves the text untouched. Cursor-motion and other
// non-login keys close the picker and fall through to the textarea.
func (m *Model) updateMentionKeys(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "esc":
		m.mention.open = false
		return nil
	case "enter":
		return m.acceptMention()
	case "up", "down", "tab", "shift+tab":
		var cmd tea.Cmd
		m.mention.list, cmd = m.mention.list.Update(msg)
		return cmd
	case "backspace", "ctrl+h":
		return m.mentionBackspace(msg)
	}

	if len(msg.Runes) == 1 && isMentionChar(msg.Runes[0]) {
		return m.mentionTypeRune(msg)
	}

	// Any other key (space, punctuation, cursor motion, ctrl+u, etc.)
	// closes the picker and flows through to the textarea so the user's
	// intent — typing, navigating, deleting a word — still happens.
	m.mention.open = false
	var cmd tea.Cmd
	m.noteInput, cmd = m.noteInput.Update(msg)
	return cmd
}

// mentionTypeRune appends a char to the query and refreshes the filtered
// list.
func (m *Model) mentionTypeRune(msg tea.KeyMsg) tea.Cmd {
	var cmd tea.Cmd
	m.noteInput, cmd = m.noteInput.Update(msg)
	m.mention.query += string(msg.Runes[0])
	m.refilterMentions()
	return cmd
}

// mentionBackspace pops a char off the query. If the query was already
// empty the backspace deletes the leading `@` and we close the picker.
func (m *Model) mentionBackspace(msg tea.KeyMsg) tea.Cmd {
	if m.mention.query == "" {
		m.mention.open = false
		var cmd tea.Cmd
		m.noteInput, cmd = m.noteInput.Update(msg)
		return cmd
	}
	runes := []rune(m.mention.query)
	m.mention.query = string(runes[:len(runes)-1])
	var cmd tea.Cmd
	m.noteInput, cmd = m.noteInput.Update(msg)
	m.refilterMentions()
	return cmd
}

func (m *Model) refilterMentions() {
	if m.pr == nil {
		return
	}
	cached, ok := m.contributors[m.pr.Repo]
	if !ok {
		return
	}
	m.mention.list.SetItems(filterContributors(cached, m.mention.query))
	m.mention.list.ResetSelected()
}

// acceptMention replaces the typed `@<query>` fragment in the textarea
// with `@<login> ` by rewinding len(query)+1 backspaces through the
// textarea and then inserting the chosen login.
func (m *Model) acceptMention() tea.Cmd {
	it, ok := m.mention.list.SelectedItem().(mentionItem)
	if !ok {
		m.mention.open = false
		return nil
	}
	bs := tea.KeyMsg{Type: tea.KeyBackspace}
	for i := 0; i < len([]rune(m.mention.query))+1; i++ {
		m.noteInput, _ = m.noteInput.Update(bs)
	}
	m.noteInput.InsertString("@" + it.login + " ")
	m.mention.open = false
	return nil
}

// isMentionChar is true for characters allowed in a GitHub login —
// typing one extends the filter query; anything else closes the picker.
func isMentionChar(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z':
		return true
	case r >= 'A' && r <= 'Z':
		return true
	case r >= '0' && r <= '9':
		return true
	case r == '-' || r == '_':
		return true
	}
	return false
}
