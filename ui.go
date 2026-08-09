package main

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Two-pane TUI: left = flake list, right = input status for focused flake.
// Space toggles selection for update; enter/u confirms and quits to run updates.

var (
	styleTitle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
	styleHelp   = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	styleOK     = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	styleStale  = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	styleErr    = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	styleMuted  = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	styleBorder = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("8"))
	styleSel    = lipgloss.NewStyle().Foreground(lipgloss.Color("11")) // selected for update
	styleFocus  = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("12"))
)

type flakeItem struct {
	st       flakeStatus
	selected bool // marked for update
}

func (i flakeItem) Title() string {
	mark := statusMark(i.st.kind)
	check := " "
	if i.selected {
		check = styleSel.Render("●")
	} else {
		check = styleMuted.Render("○")
	}
	return fmt.Sprintf("%s %s %s", check, mark, i.st.label)
}

func (i flakeItem) Description() string {
	return statusSummary(i.st)
}

func (i flakeItem) FilterValue() string {
	return i.st.label
}

func statusMark(k kind) string {
	switch k {
	case kindOK:
		return styleOK.Render("✓")
	case kindStale:
		return styleStale.Render("✗")
	default:
		return styleErr.Render("!")
	}
}

func statusSummary(st flakeStatus) string {
	switch st.kind {
	case kindOK:
		return "up to date"
	case kindStale:
		names := make([]string, 0, len(st.details))
		for _, d := range st.details {
			if i := strings.Index(d, ":"); i >= 0 {
				names = append(names, d[:i])
			} else {
				names = append(names, d)
			}
		}
		return strings.Join(names, ", ")
	default:
		if len(st.details) > 0 {
			return st.details[0]
		}
		return "error"
	}
}

type uiModel struct {
	statuses  []flakeStatus
	list      list.Model
	detail    viewport.Model
	width     int
	height    int
	ready     bool
	confirm   bool // confirm dialog open
	cancelled bool
	toUpdate  []flakeStatus // set on successful confirm quit
}

func newUI(statuses []flakeStatus) uiModel {
	items := make([]list.Item, len(statuses))
	for i, st := range statuses {
		// Pre-select stale/error for update.
		sel := st.kind == kindStale || st.kind == kindError
		items[i] = flakeItem{st: st, selected: sel}
	}

	delegate := list.NewDefaultDelegate()
	// Name + marks only; input detail lives in the right pane.
	delegate.ShowDescription = false
	delegate.SetHeight(1)
	delegate.SetSpacing(0)
	delegate.Styles.SelectedTitle = delegate.Styles.SelectedTitle.Foreground(lipgloss.Color("15")).BorderForeground(lipgloss.Color("12"))

	l := list.New(items, delegate, 0, 0)
	l.Title = "Flakes"
	l.SetShowStatusBar(false)
	l.SetFilteringEnabled(true)
	l.SetShowHelp(false)
	l.DisableQuitKeybindings()

	// Extra keys shown in our footer, not bubbles help.
	l.AdditionalShortHelpKeys = func() []key.Binding {
		return []key.Binding{
			key.NewBinding(key.WithKeys(" "), key.WithHelp("space", "toggle")),
			key.NewBinding(key.WithKeys("enter", "u"), key.WithHelp("enter", "update")),
			key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "stale")),
			key.NewBinding(key.WithKeys("q"), key.WithHelp("q", "quit")),
		}
	}

	vp := viewport.New(0, 0)
	vp.SetContent("")

	m := uiModel{
		statuses: statuses,
		list:     l,
		detail:   vp,
	}
	m.refreshDetail()
	return m
}

func (m *uiModel) focusedStatus() *flakeStatus {
	item, ok := m.list.SelectedItem().(flakeItem)
	if !ok {
		return nil
	}
	return &item.st
}

func (m *uiModel) refreshDetail() {
	st := m.focusedStatus()
	if st == nil {
		m.detail.SetContent(styleMuted.Render("No flake selected."))
		return
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s  %s\n", statusMark(st.kind), styleTitle.Render(st.label))
	fmt.Fprintf(&b, "%s\n\n", styleMuted.Render(st.path))

	switch st.kind {
	case kindOK:
		fmt.Fprintf(&b, "%s\n", styleOK.Render("All inputs up to date."))
	case kindError:
		fmt.Fprintf(&b, "%s\n\n", styleErr.Render("Could not check this flake:"))
		for _, d := range st.details {
			fmt.Fprintf(&b, "  %s\n", d)
		}
	case kindStale:
		fmt.Fprintf(&b, "%s\n\n", styleStale.Render("Stale or problem inputs:"))
		for _, d := range st.details {
			// "name: reason"
			name, reason, ok := strings.Cut(d, ": ")
			if !ok {
				fmt.Fprintf(&b, "  • %s\n", d)
				continue
			}
			rStyle := styleStale
			if strings.HasPrefix(reason, "error") {
				rStyle = styleErr
			}
			fmt.Fprintf(&b, "  • %s  %s\n", lipgloss.NewStyle().Bold(true).Render(name), rStyle.Render(reason))
		}
	}

	// Selection state for this flake
	if item, ok := m.list.SelectedItem().(flakeItem); ok {
		fmt.Fprintln(&b)
		if item.selected {
			fmt.Fprintf(&b, "%s\n", styleSel.Render("● marked for update"))
		} else {
			fmt.Fprintf(&b, "%s\n", styleMuted.Render("○ not marked — space to toggle"))
		}
	}

	m.detail.SetContent(b.String())
	m.detail.GotoTop()
}

func (m uiModel) Init() tea.Cmd {
	return nil
}

func (m uiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.layout()
		m.ready = true
		m.refreshDetail()
		return m, nil

	case tea.KeyMsg:
		if m.confirm {
			switch msg.String() {
			case "y", "Y", "enter":
				m.toUpdate = m.selectedStatuses()
				return m, tea.Quit
			case "n", "N", "esc", "q", "ctrl+c":
				m.confirm = false
				return m, nil
			}
			return m, nil
		}

		// When filtering, let list handle most keys.
		if m.list.FilterState() == list.Filtering {
			break
		}

		switch msg.String() {
		case "q", "ctrl+c":
			m.cancelled = true
			return m, tea.Quit
		case " ":
			m.toggleSelected()
			m.refreshDetail()
			return m, nil
		case "a":
			m.toggleAllStale()
			m.refreshDetail()
			return m, nil
		case "enter", "u":
			sel := m.selectedStatuses()
			if len(sel) == 0 {
				return m, nil
			}
			m.confirm = true
			return m, nil
		case "j", "down", "k", "up", "g", "G", "ctrl+d", "ctrl+u", "pgdown", "pgup":
			// fall through to list
		}
	}

	var cmd tea.Cmd
	prevIdx := m.list.Index()
	m.list, cmd = m.list.Update(msg)
	cmds = append(cmds, cmd)
	if m.list.Index() != prevIdx {
		m.refreshDetail()
	}

	// Detail scroll: don't steal j/k from the list — use pgup/pgdn / ctrl+d/u only.
	if !m.confirm {
		if km, ok := msg.(tea.KeyMsg); ok {
			switch km.String() {
			case "pgdown", "pgup", "ctrl+d", "ctrl+u":
				var dcmd tea.Cmd
				m.detail, dcmd = m.detail.Update(msg)
				cmds = append(cmds, dcmd)
			}
		}
	}

	return m, tea.Batch(cmds...)
}

func (m *uiModel) toggleSelected() {
	idx := m.list.Index()
	items := m.list.Items()
	if idx < 0 || idx >= len(items) {
		return
	}
	it, ok := items[idx].(flakeItem)
	if !ok {
		return
	}
	it.selected = !it.selected
	items[idx] = it
	m.list.SetItems(items)
}

func (m *uiModel) toggleAllStale() {
	items := m.list.Items()
	// If any stale/error unselected → select all stale/error; else clear all.
	needSelect := false
	for _, raw := range items {
		it, ok := raw.(flakeItem)
		if !ok {
			continue
		}
		if (it.st.kind == kindStale || it.st.kind == kindError) && !it.selected {
			needSelect = true
			break
		}
	}
	for i, raw := range items {
		it, ok := raw.(flakeItem)
		if !ok {
			continue
		}
		if it.st.kind == kindStale || it.st.kind == kindError {
			it.selected = needSelect
		}
		items[i] = it
	}
	m.list.SetItems(items)
}

func (m uiModel) selectedStatuses() []flakeStatus {
	var out []flakeStatus
	for _, raw := range m.list.Items() {
		it, ok := raw.(flakeItem)
		if ok && it.selected {
			out = append(out, it.st)
		}
	}
	return out
}

func (m *uiModel) layout() {
	// Footer + title rows
	chrome := 3
	innerH := m.height - chrome
	if innerH < 5 {
		innerH = 5
	}
	// Left ~40%, right rest; min widths
	leftW := m.width * 2 / 5
	if leftW < 24 {
		leftW = 24
	}
	if leftW > 48 {
		leftW = 48
	}
	rightW := m.width - leftW - 2
	if rightW < 20 {
		rightW = 20
		leftW = m.width - rightW - 2
	}

	m.list.SetSize(leftW-2, innerH-2) // account for border in view
	m.detail.Width = rightW - 2
	m.detail.Height = innerH - 2
}

func (m uiModel) View() string {
	if !m.ready {
		return "…"
	}

	if m.confirm {
		names := make([]string, 0)
		for _, st := range m.selectedStatuses() {
			names = append(names, st.label)
		}
		box := styleFocus.Padding(1, 2).Render(
			styleTitle.Render("Update locks?") + "\n\n" +
				strings.Join(names, ", ") + "\n\n" +
				styleMuted.Render("nix flake update + commit flake.lock") + "\n\n" +
				styleHelp.Render("y/enter confirm  ·  n/esc cancel"),
		)
		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, box)
	}

	leftTitle := styleTitle.Render(" Flakes ")
	rightTitle := styleTitle.Render(" Inputs ")
	st := m.focusedStatus()
	if st != nil {
		rightTitle = styleTitle.Render(" Inputs · "+st.label+" ")
	}

	left := styleBorder.Width(m.list.Width() + 2).Height(m.list.Height() + 2).Render(
		lipgloss.JoinVertical(lipgloss.Left, leftTitle, m.list.View()),
	)
	// Border wraps detail
	rightInner := lipgloss.JoinVertical(lipgloss.Left, rightTitle, m.detail.View())
	right := styleBorder.Width(m.detail.Width + 2).Height(m.detail.Height + 2).Render(rightInner)

	body := lipgloss.JoinHorizontal(lipgloss.Top, left, right)
	help := styleHelp.Render("space toggle · a stale · enter/u update · / filter · pgup/pgdn detail · q quit")
	nSel := len(m.selectedStatuses())
	status := styleMuted.Render(fmt.Sprintf("%d marked for update", nSel))
	footer := lipgloss.JoinHorizontal(lipgloss.Top,
		help,
		strings.Repeat(" ", max(1, m.width-lipgloss.Width(help)-lipgloss.Width(status)-1)),
		status,
	)

	return lipgloss.JoinVertical(lipgloss.Left, body, footer)
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
