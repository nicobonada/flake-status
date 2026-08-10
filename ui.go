package main

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Two-pane survey TUI: left = flake list, right = VCS + inputs for the focus.
// Read-only — updates are done outside this tool.

var (
	styleTitle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
	styleHelp   = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	styleOK     = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	styleStale  = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	stylePin    = lipgloss.NewStyle().Foreground(lipgloss.Color("3")) // amber
	styleErr    = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	styleMuted  = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	styleBorder = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("8"))
)

// Messages from the background survey.
type surveyResultMsg struct{ st flakeStatus }
type surveyDoneMsg struct{}

type flakeItem struct {
	st flakeStatus
}

func (i flakeItem) Title() string {
	return fmt.Sprintf("%s %s", statusMark(i.st.kind), i.st.label)
}

func (i flakeItem) Description() string {
	return statusSummary(i.st)
}

func (i flakeItem) FilterValue() string {
	return i.st.label
}

func statusMark(k kind) string {
	switch k {
	case kindPending:
		return styleMuted.Render("…")
	case kindOK:
		return styleOK.Render("✓")
	case kindStale:
		return styleStale.Render("✗")
	case kindPin:
		return stylePin.Render("!")
	case kindError:
		return styleErr.Render("!")
	default:
		return styleMuted.Render("?")
	}
}

func statusSummary(st flakeStatus) string {
	switch st.kind {
	case kindPending:
		return "checking…"
	case kindOK:
		if !st.vcs.Aligned && st.vcs.Summary != "" && st.vcs.Summary != "(no vcs)" {
			return "inputs ok · " + st.vcs.Summary
		}
		return "up to date"
	case kindPin:
		return pinSummary(st)
	case kindStale:
		return problemSummary(st)
	default:
		if st.flakeErr != "" {
			return st.flakeErr
		}
		return "error"
	}
}

func pinSummary(st flakeStatus) string {
	var names []string
	for _, in := range st.inputs {
		if in.State == inputPin {
			names = append(names, in.Name)
		}
	}
	if len(names) == 0 {
		return "pin lag"
	}
	return strings.Join(names, ", ")
}

func problemSummary(st flakeStatus) string {
	var names []string
	for _, in := range st.inputs {
		switch in.State {
		case inputStale, inputError, inputPin:
			names = append(names, in.Name)
		}
	}
	if len(names) == 0 {
		return "stale"
	}
	return strings.Join(names, ", ")
}

type uiModel struct {
	statuses []flakeStatus
	list     list.Model
	detail   viewport.Model
	spinner  spinner.Model
	surveyCh <-chan flakeStatus
	checking bool
	checked  int
	total    int
	width    int
	height   int
	ready    bool
}

func newUI(statuses []flakeStatus, surveyCh <-chan flakeStatus) uiModel {
	items := make([]list.Item, len(statuses))
	for i, st := range statuses {
		items[i] = flakeItem{st: st}
	}

	delegate := list.NewDefaultDelegate()
	delegate.ShowDescription = false
	delegate.SetHeight(1)
	delegate.SetSpacing(0)
	delegate.Styles.SelectedTitle = delegate.Styles.SelectedTitle.
		Foreground(lipgloss.Color("15")).
		BorderForeground(lipgloss.Color("12"))

	l := list.New(items, delegate, 0, 0)
	l.Title = ""
	l.SetShowTitle(false)
	l.SetShowStatusBar(false)
	l.SetFilteringEnabled(true)
	l.SetShowHelp(false)
	l.DisableQuitKeybindings()
	l.AdditionalShortHelpKeys = func() []key.Binding {
		return []key.Binding{
			key.NewBinding(key.WithKeys("q"), key.WithHelp("q", "quit")),
		}
	}

	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("12"))

	vp := viewport.New(0, 0)
	vp.SetContent("")

	m := uiModel{
		statuses: statuses,
		list:     l,
		detail:   vp,
		spinner:  sp,
		surveyCh: surveyCh,
		checking: surveyCh != nil,
		total:    len(statuses),
	}
	m.refreshDetail()
	return m
}

func waitSurvey(ch <-chan flakeStatus) tea.Cmd {
	return func() tea.Msg {
		st, ok := <-ch
		if !ok {
			return surveyDoneMsg{}
		}
		return surveyResultMsg{st: st}
	}
}

func (m *uiModel) focusedStatus() *flakeStatus {
	item, ok := m.list.SelectedItem().(flakeItem)
	if !ok {
		return nil
	}
	return &item.st
}

func (m *uiModel) applySurveyResult(st flakeStatus) {
	items := m.list.Items()
	for i, raw := range items {
		it, ok := raw.(flakeItem)
		if !ok || it.st.path != st.path {
			continue
		}
		it.st = st
		items[i] = it
		break
	}
	m.list.SetItems(items)
	m.checked++
	for i := range m.statuses {
		if m.statuses[i].path == st.path {
			m.statuses[i] = st
			break
		}
	}
	m.refreshDetail()
}

func (m *uiModel) attentionCount() int {
	n := 0
	for _, raw := range m.list.Items() {
		it, ok := raw.(flakeItem)
		if ok && it.st.needsAttention() {
			n++
		}
	}
	return n
}

func (m *uiModel) refreshDetail() {
	st := m.focusedStatus()
	if st == nil {
		m.detail.SetContent(styleMuted.Render("No flake focused."))
		return
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n", styleMuted.Render(st.path))

	// VCS block
	fmt.Fprintf(&b, "%s  ", styleTitle.Render("VCS"))
	switch {
	case st.kind == kindPending && st.vcs.Pending:
		fmt.Fprintf(&b, "%s %s\n", m.spinner.View(), styleMuted.Render("fetching…"))
	case st.vcs.Err != "" && st.vcs.Summary == "":
		fmt.Fprintf(&b, "%s\n", styleErr.Render(st.vcs.Err))
	default:
		sumStyle := styleMuted
		if st.vcs.Aligned {
			sumStyle = styleOK
		} else if st.vcs.Summary != "(no vcs)" {
			sumStyle = stylePin
		}
		fmt.Fprintf(&b, "%s\n", sumStyle.Render(st.vcs.Summary))
		for _, line := range st.vcs.Lines {
			fmt.Fprintf(&b, "     %s\n", styleMuted.Render(line))
		}
	}

	fmt.Fprintln(&b)
	fmt.Fprintf(&b, "%s\n", styleTitle.Render("Inputs"))

	switch {
	case st.kind == kindPending:
		fmt.Fprintf(&b, "  %s %s\n", m.spinner.View(), styleMuted.Render("Checking inputs…"))
	case st.flakeErr != "":
		fmt.Fprintf(&b, "  %s\n", styleErr.Render(st.flakeErr))
	case len(st.inputs) == 0:
		fmt.Fprintf(&b, "  %s\n", styleMuted.Render("(no direct inputs)"))
	default:
		// Align names for a clean column.
		nameWidth := 0
		for _, in := range st.inputs {
			if len(in.Name) > nameWidth {
				nameWidth = len(in.Name)
			}
		}
		if nameWidth > 28 {
			nameWidth = 28
		}
		for _, in := range st.inputs {
			glyph, gStyle := inputGlyph(in)
			name := in.Name
			if len(name) > nameWidth {
				name = name[:nameWidth-1] + "…"
			}
			pad := strings.Repeat(" ", nameWidth-len(name))
			line := fmt.Sprintf("  %s  %s%s", gStyle.Render(glyph), name, pad)
			if in.Detail != "" {
				dStyle := styleMuted
				switch in.State {
				case inputStale:
					dStyle = styleStale
				case inputPin:
					dStyle = stylePin
				case inputError:
					dStyle = styleErr
				}
				line += "  " + dStyle.Render(in.Detail)
			}
			fmt.Fprintln(&b, line)
		}
	}

	m.detail.SetContent(b.String())
	m.detail.GotoTop()
}

func inputGlyph(in inputStatus) (string, lipgloss.Style) {
	switch in.State {
	case inputOK:
		return "✓", styleOK
	case inputStale:
		return "✗", styleStale
	case inputPin:
		return "!", stylePin
	case inputError:
		return "!", styleErr
	default:
		return "?", styleMuted
	}
}

func (m uiModel) Init() tea.Cmd {
	if m.checking && m.surveyCh != nil {
		return tea.Batch(m.spinner.Tick, waitSurvey(m.surveyCh))
	}
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

	case spinner.TickMsg:
		if m.checking {
			var cmd tea.Cmd
			m.spinner, cmd = m.spinner.Update(msg)
			if st := m.focusedStatus(); st != nil && st.kind == kindPending {
				m.refreshDetail()
			}
			return m, cmd
		}
		return m, nil

	case surveyResultMsg:
		m.applySurveyResult(msg.st)
		if m.checking && m.surveyCh != nil {
			return m, waitSurvey(m.surveyCh)
		}
		return m, nil

	case surveyDoneMsg:
		m.checking = false
		m.refreshDetail()
		return m, nil

	case tea.KeyMsg:
		if m.list.FilterState() == list.Filtering {
			break
		}
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "j", "down", "k", "up", "g", "G", "ctrl+d", "ctrl+u", "pgdown", "pgup":
			// list / detail
		}
	}

	var cmd tea.Cmd
	prevIdx := m.list.Index()
	m.list, cmd = m.list.Update(msg)
	cmds = append(cmds, cmd)
	if m.list.Index() != prevIdx {
		m.refreshDetail()
	}

	if km, ok := msg.(tea.KeyMsg); ok {
		switch km.String() {
		case "pgdown", "pgup", "ctrl+d", "ctrl+u":
			var dcmd tea.Cmd
			m.detail, dcmd = m.detail.Update(msg)
			cmds = append(cmds, dcmd)
		}
	}

	return m, tea.Batch(cmds...)
}

func (m *uiModel) layout() {
	chrome := 3
	innerH := m.height - chrome
	if innerH < 5 {
		innerH = 5
	}
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

	m.list.SetSize(leftW-2, innerH-2)
	m.detail.Width = rightW - 2
	m.detail.Height = innerH - 2
}

func (m uiModel) View() string {
	if !m.ready {
		return "…"
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
	rightInner := lipgloss.JoinVertical(lipgloss.Left, rightTitle, m.detail.View())
	right := styleBorder.Width(m.detail.Width + 2).Height(m.detail.Height + 2).Render(rightInner)

	body := lipgloss.JoinHorizontal(lipgloss.Top, left, right)
	help := styleHelp.Render("j/k move · / filter · pgup/pgdn detail · q quit")

	var status string
	if m.checking {
		status = styleMuted.Render(fmt.Sprintf("%s checking %d/%d", m.spinner.View(), m.checked, m.total))
	} else {
		n := m.attentionCount()
		if n == 0 {
			status = styleOK.Render("all clear")
		} else if n == 1 {
			status = styleMuted.Render("1 needs attention")
		} else {
			status = styleMuted.Render(fmt.Sprintf("%d need attention", n))
		}
	}
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
