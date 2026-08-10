package main

import (
	"fmt"
	"strings"
	"unicode/utf8"

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
	// Palette tuned for dark terminals: body text stays readable; accents stay loud.
	// Avoid ANSI 8 (often equal to the background in dark themes).
	colAccent  = lipgloss.Color("12")  // cyan/blue — focus, section labels
	colOK      = lipgloss.Color("10")  // bright green
	colStale   = lipgloss.Color("9")   // bright red
	colAmber   = lipgloss.Color("214") // orange/amber
	colHash    = lipgloss.Color("13")  // magenta — short ids
	colName    = lipgloss.Color("14")  // cyan — bookmark / field names
	colMuted   = lipgloss.Color("245") // secondary body (path, VCS desc, help)
	colBorder  = lipgloss.Color("240") // unfocused border (dimmer than body text)
	colBorderF = lipgloss.Color("12")  // focused pane border
	colText    = lipgloss.Color("15")  // selected row
	colBody    = lipgloss.Color("252") // unselected list labels
	colTitleOff = lipgloss.Color("248")

	styleHelp     = lipgloss.NewStyle().Foreground(colMuted)
	styleOK       = lipgloss.NewStyle().Foreground(colOK)
	styleStale    = lipgloss.NewStyle().Foreground(colStale)
	styleAmber    = lipgloss.NewStyle().Foreground(colAmber)
	styleErr      = lipgloss.NewStyle().Foreground(colStale)
	styleMuted    = lipgloss.NewStyle().Foreground(colMuted)
	styleSection  = lipgloss.NewStyle().Bold(true).Foreground(colAccent)
	stylePath     = lipgloss.NewStyle().Foreground(colMuted)
	styleBook     = lipgloss.NewStyle().Foreground(colName)
	styleHash     = lipgloss.NewStyle().Foreground(colHash)
	styleTitleOn  = lipgloss.NewStyle().Bold(true).Foreground(colAccent)
	styleTitleOff = lipgloss.NewStyle().Bold(true).Foreground(colTitleOff)
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
	case kindStale, kindError:
		return styleStale.Render("✗")
	case kindPin:
		return styleAmber.Render("!")
	default:
		return styleMuted.Render("?")
	}
}

func statusSummary(st flakeStatus) string {
	switch st.kind {
	case kindPending:
		return "checking…"
	case kindOK:
		return "up to date"
	case kindPin:
		return amberSummary(st)
	case kindStale:
		return problemSummary(st)
	default:
		if st.flakeErr != "" {
			return st.flakeErr
		}
		return "error"
	}
}

func amberSummary(st flakeStatus) string {
	var parts []string
	for _, in := range st.inputs {
		if in.State == inputPin {
			parts = append(parts, in.Name)
		}
	}
	if vcsDriftAmber(st.vcs) {
		parts = append(parts, "vcs")
	}
	if len(parts) == 0 {
		return "attention"
	}
	return strings.Join(parts, ", ")
}

func problemSummary(st flakeStatus) string {
	var names []string
	for _, in := range st.inputs {
		switch in.State {
		case inputStale, inputError, inputPin:
			names = append(names, in.Name)
		}
	}
	if vcsDriftAmber(st.vcs) {
		names = append(names, "vcs")
	}
	if len(names) == 0 {
		if st.flakeErr != "" {
			return st.flakeErr
		}
		return "stale"
	}
	return strings.Join(names, ", ")
}

type uiModel struct {
	statuses  []flakeStatus
	list      list.Model
	detail    viewport.Model
	spinner   spinner.Model
	surveyCh  <-chan flakeStatus
	checking  bool
	checked   int
	total     int
	width     int
	height    int
	ready     bool
	leftBoxW  int
	rightBoxW int
	boxH      int
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
	// Selected row: bright text + cyan left bar (lazyjj-ish focus).
	delegate.Styles.SelectedTitle = lipgloss.NewStyle().
		Foreground(colText).
		Bold(true).
		Border(lipgloss.NormalBorder(), false, false, false, true).
		BorderForeground(colAccent).
		PaddingLeft(1)
	delegate.Styles.NormalTitle = lipgloss.NewStyle().
		Foreground(colBody).
		PaddingLeft(1)
	delegate.Styles.DimmedTitle = lipgloss.NewStyle().
		Foreground(colMuted).
		PaddingLeft(1)

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
	sp.Style = lipgloss.NewStyle().Foreground(colAccent)

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
	fmt.Fprintf(&b, "%s\n\n", stylePath.Render(st.path))

	// VCS block
	fmt.Fprintf(&b, "%s  ", styleSection.Render("VCS"))
	switch {
	case st.kind == kindPending && st.vcs.Pending:
		fmt.Fprintf(&b, "%s %s\n", m.spinner.View(), styleMuted.Render("fetching…"))
	case st.vcs.Err != "" && st.vcs.Summary == "":
		fmt.Fprintf(&b, "%s\n", styleErr.Render(st.vcs.Err))
	default:
		sumStyle := styleMuted
		if st.vcs.Aligned {
			sumStyle = styleOK
		} else if vcsDriftAmber(st.vcs) {
			sumStyle = styleAmber
		} else if st.vcs.Err != "" {
			sumStyle = styleErr
		}
		fmt.Fprintf(&b, "%s\n", sumStyle.Render(st.vcs.Summary))
		for _, line := range st.vcs.Lines {
			fmt.Fprintf(&b, "     %s\n", renderVCSLine(line))
		}
	}

	fmt.Fprintln(&b)
	fmt.Fprintf(&b, "%s\n", styleSection.Render("Inputs"))

	switch {
	case st.kind == kindPending:
		fmt.Fprintf(&b, "  %s %s\n", m.spinner.View(), styleMuted.Render("Checking inputs…"))
	case st.flakeErr != "":
		fmt.Fprintf(&b, "  %s\n", styleErr.Render(st.flakeErr))
	case len(st.inputs) == 0:
		fmt.Fprintf(&b, "  %s\n", styleMuted.Render("(no direct inputs)"))
	default:
		nameWidth := 0
		for _, in := range st.inputs {
			if n := utf8.RuneCountInString(in.Name); n > nameWidth {
				nameWidth = n
			}
		}
		if nameWidth > 28 {
			nameWidth = 28
		}
		for _, in := range st.inputs {
			glyph, gStyle := inputGlyph(in)
			name := in.Name
			runes := []rune(name)
			if len(runes) > nameWidth {
				name = string(runes[:nameWidth-1]) + "…"
			}
			pad := strings.Repeat(" ", nameWidth-utf8.RuneCountInString(name))
			line := fmt.Sprintf("  %s  %s%s", gStyle.Render(glyph), name, pad)
			if in.Detail != "" {
				dStyle := styleMuted
				switch in.State {
				case inputStale:
					dStyle = styleStale
				case inputPin:
					dStyle = styleAmber
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

func renderVCSLine(line vcsLine) string {
	name := fmt.Sprintf("%-6s", line.Name)
	parts := []string{styleBook.Render(name), styleHash.Render(line.ID)}
	if line.Desc != "" {
		parts = append(parts, styleMuted.Render(line.Desc))
	}
	return strings.Join(parts, "  ")
}

func inputGlyph(in inputStatus) (string, lipgloss.Style) {
	switch in.State {
	case inputOK:
		return "✓", styleOK
	case inputStale:
		return "✗", styleStale
	case inputPin:
		return "!", styleAmber
	case inputError:
		return "✗", styleErr
	default:
		return "?", styleMuted
	}
}

// titledBox draws a rounded box with the title embedded in the top border:
//
//	╭─ Title ─────────╮
//	│ content         │
//	╰─────────────────╯
func titledBox(title, content string, width, height int, focused bool) string {
	if width < 4 {
		width = 4
	}
	if height < 2 {
		height = 2
	}

	borderCol := colBorder
	titleStyle := styleTitleOff
	if focused {
		borderCol = colBorderF
		titleStyle = styleTitleOn
	}
	bStyle := lipgloss.NewStyle().Foreground(borderCol)

	// Top border with title: ╭─ Title ──…─╮
	const (
		tl = "╭"
		tr = "╮"
		bl = "╰"
		br = "╯"
		h  = "─"
		v  = "│"
	)
	innerW := width - 2 // between vertical borders
	title = strings.TrimSpace(title)
	// "─ Title ─" fragment; pad with ─ to fill innerW
	titleFrag := ""
	if title != "" {
		titleFrag = " " + title + " "
	}
	titleVis := utf8.RuneCountInString(titleFrag)
	// left "─" after ╭, then title, then fill, before ╮
	// layout: ╭ + ─ + titleFrag + ─* + ╮  but title is colored separately
	leftDashes := 1
	remain := innerW - leftDashes - titleVis
	if remain < 1 {
		// Truncate title to fit.
		maxTitle := innerW - leftDashes - 1
		if maxTitle < 1 {
			maxTitle = 1
		}
		r := []rune(title)
		if len(r) > maxTitle {
			title = string(r[:maxTitle])
		}
		titleFrag = " " + title + " "
		titleVis = utf8.RuneCountInString(titleFrag)
		remain = innerW - leftDashes - titleVis
		if remain < 0 {
			remain = 0
		}
	}
	top := bStyle.Render(tl+strings.Repeat(h, leftDashes)) +
		titleStyle.Render(titleFrag) +
		bStyle.Render(strings.Repeat(h, remain)+tr)

	// Content area: height-2 rows of │ content │
	bodyH := height - 2
	if bodyH < 1 {
		bodyH = 1
	}
	// Pad/truncate content lines to bodyH and innerW.
	rawLines := strings.Split(content, "\n")
	lines := make([]string, bodyH)
	for i := 0; i < bodyH; i++ {
		var line string
		if i < len(rawLines) {
			line = rawLines[i]
		}
		lines[i] = padVisual(line, innerW)
	}
	var body strings.Builder
	for _, line := range lines {
		body.WriteString(bStyle.Render(v))
		body.WriteString(line)
		body.WriteString(bStyle.Render(v))
		body.WriteByte('\n')
	}

	bottom := bStyle.Render(bl + strings.Repeat(h, innerW) + br)
	return top + "\n" + body.String() + bottom
}

// padVisual pads or truncates s to width display cells (ANSI-aware via lipgloss).
func padVisual(s string, width int) string {
	w := lipgloss.Width(s)
	if w == width {
		return s
	}
	if w < width {
		return s + strings.Repeat(" ", width-w)
	}
	// Truncate carefully: walk runes with lipgloss width.
	// Simple path: use lipgloss.NewStyle().Width(width).MaxWidth(width).Render
	return lipgloss.NewStyle().Width(width).MaxHeight(1).Render(s)
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
	chrome := 2 // footer only; titles live in borders
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
	rightW := m.width - leftW
	if rightW < 24 {
		rightW = 24
		leftW = m.width - rightW
	}

	// Inner content size accounts for border (1 col/row each side).
	m.list.SetSize(leftW-2, innerH-2)
	m.detail.Width = rightW - 2
	m.detail.Height = innerH - 2
	m.leftBoxW = leftW
	m.rightBoxW = rightW
	m.boxH = innerH
}

func (m uiModel) View() string {
	if !m.ready {
		return "…"
	}

	// Right border title is the focused flake name (not "Inputs").
	rightTitle := "·"
	if st := m.focusedStatus(); st != nil {
		rightTitle = st.label
	}

	// List is focused for navigation; give it the accent border.
	left := titledBox("Flakes", m.list.View(), m.leftBoxW, m.boxH, true)
	right := titledBox(rightTitle, m.detail.View(), m.rightBoxW, m.boxH, false)

	body := lipgloss.JoinHorizontal(lipgloss.Top, left, right)
	help := styleHelp.Render("j/k move · / filter · pgup/pgdn detail · q quit")

	var status string
	if m.checking {
		status = styleMuted.Render(fmt.Sprintf("%s checking %d/%d", m.spinner.View(), m.checked, m.total))
	} else {
		n := m.attentionCount()
		switch {
		case n == 0:
			status = styleOK.Render("all clear")
		case n == 1:
			status = styleAmber.Render("1 needs attention")
		default:
			status = styleAmber.Render(fmt.Sprintf("%d need attention", n))
		}
	}
	gap := max(1, m.width-lipgloss.Width(help)-lipgloss.Width(status)-1)
	footer := lipgloss.JoinHorizontal(lipgloss.Top, help, strings.Repeat(" ", gap), status)

	return lipgloss.JoinVertical(lipgloss.Left, body, footer)
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
