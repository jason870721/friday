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

	footer := m.footer()

	return strings.Join([]string{transcript, inputLine, footer}, "\n")
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
	return footerStyle.Render(
		fmt.Sprintf("%s%s%s%s%d msgs%s%s",
			model,
			footerDivider,
			m.persona,
			footerDivider,
			m.messages,
			footerDivider,
			tokens))
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
