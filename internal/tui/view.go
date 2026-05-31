package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// View renders the three regions stacked: scrollback transcript →
// prompt input → status footer.
//
// The footer keys: provider/model | persona | message count | tokens.
// Tokens are cumulative across the session.
func (m Model) View() string {
	if !m.ready {
		return "loading friday…\n"
	}

	transcript := m.view.View()

	inputLine := m.input.View()
	if m.busy {
		inputLine = styleNotice.Render("… working …") + "  " + inputLine
	}

	// While the user is typing a "/..." command, the footer turns into a live
	// skill picker; otherwise it shows the usual status line. Keeping it in the
	// single footer slot avoids disturbing the viewport height math.
	footer := m.footer()
	if strings.HasPrefix(strings.TrimSpace(m.input.Value()), "/") {
		footer = m.skillFooter()
	}

	return strings.Join([]string{transcript, inputLine, footer}, "\n")
}

// skillFooter renders the live "/" command picker: the skills whose name
// matches what's typed so far, on the footer line.
func (m Model) skillFooter() string {
	typed := strings.TrimPrefix(strings.TrimSpace(m.input.Value()), "/")
	matches := matchSkills(m.skills, typed)
	if len(m.skills) == 0 {
		return footerStyle.Render("no skills found under .friday/skills/")
	}
	if len(matches) == 0 {
		return footerStyle.Render("no skill matches /" + typed + " — /help to list all")
	}
	names := make([]string, len(matches))
	for i, s := range matches {
		names[i] = "/" + s.Name
	}
	return footerStyle.Render("skills: " + strings.Join(names, "  ") + "  · ⏎ to run")
}

var (
	footerStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("245")).
			Faint(true)
	footerDivider = footerStyle.Render(" │ ")
)

func (m Model) footer() string {
	model := m.model
	if model == "" {
		model = "deepseek/?"
	}
	tokens := fmt.Sprintf("%s in / %s out toks",
		commafy(m.inputTokens), commafy(m.outputTokens))
	parts := []string{
		model,
		m.persona,
		fmt.Sprintf("%d msgs", m.messages),
		tokens,
	}
	if n := len(m.pendingPrompts); n > 0 {
		parts = append(parts, fmt.Sprintf("%d queued", n))
	}
	return footerStyle.Render(strings.Join(parts, footerDivider))
}

// commafy renders n with thousands separators. Bytes-only, no
// locale-aware shenanigans.
func commafy(n int) string {
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	var b strings.Builder
	pre := len(s) % 3
	if pre > 0 {
		b.WriteString(s[:pre])
		if len(s) > pre {
			b.WriteString(",")
		}
	}
	for i := pre; i < len(s); i += 3 {
		b.WriteString(s[i : i+3])
		if i+3 < len(s) {
			b.WriteString(",")
		}
	}
	return b.String()
}
