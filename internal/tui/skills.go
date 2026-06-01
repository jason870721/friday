package tui

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Skill is a user-invocable command discovered under .friday/skills/<name>/SKILL.md.
// Typing "/<name>" in the TUI runs its Prompt through the runner (the
// orchestrator), so e.g. "/start" injects the trading kickoff prompt.
//
// The runnable prompt and metadata come from a small YAML-ish frontmatter block
// at the top of SKILL.md:
//
//	---
//	name: start
//	description: 啟動 F.R.I.D.A.Y. 自主合約交易
//	prompt: 開始交易。立即分析所有已設定的市場…
//	---
//
// A skill with no `prompt:` is listed but not runnable (it's a docs-only skill).
type Skill struct {
	Name        string
	Description string
	Prompt      string
	Path        string
}

// loadSkills scans <root>/.friday/skills/*/SKILL.md and returns the parsed
// skills, sorted by name. Missing directory or unreadable files are not errors —
// they just yield fewer (or no) skills, so the slash-command UI degrades to
// "no skills found".
func loadSkills(root string) []Skill {
	dir := filepath.Join(root, ".friday", "skills")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}

	out := make([]Skill, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		path := filepath.Join(dir, e.Name(), "SKILL.md")
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		sk := parseSkillFrontmatter(string(data))
		if sk.Name == "" {
			sk.Name = e.Name() // fall back to the directory name
		}
		sk.Path = path
		out = append(out, sk)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// parseSkillFrontmatter reads the leading `---`-delimited block and pulls out
// the name / description / prompt keys. Values are single-line. Anything that
// isn't a well-formed frontmatter block yields a zero Skill (the caller fills
// the name from the directory).
func parseSkillFrontmatter(content string) Skill {
	var sk Skill
	if !strings.HasPrefix(content, "---") {
		return sk
	}
	rest := strings.TrimPrefix(content[3:], "\n")
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return sk
	}
	for _, line := range strings.Split(rest[:end], "\n") {
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		val = strings.TrimSpace(val)
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "name":
			sk.Name = val
		case "description":
			sk.Description = val
		case "prompt":
			sk.Prompt = val
		}
	}
	return sk
}

// findSkill returns the skill whose name matches (case-insensitively) the typed
// name, and whether one was found.
func findSkill(skills []Skill, name string) (Skill, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, s := range skills {
		if strings.ToLower(s.Name) == name {
			return s, true
		}
	}
	return Skill{}, false
}

// matchSkills returns the skills whose name starts with the typed prefix
// (case-insensitive). An empty prefix matches all — used for the live "/"
// suggestion footer.
func matchSkills(skills []Skill, prefix string) []Skill {
	prefix = strings.ToLower(strings.TrimSpace(prefix))
	out := make([]Skill, 0, len(skills))
	for _, s := range skills {
		if prefix == "" || strings.HasPrefix(strings.ToLower(s.Name), prefix) {
			out = append(out, s)
		}
	}
	return out
}

// skillListLines renders the full skill list for the transcript (shown on "/"
// or "/help").
func skillListLines(skills []Skill) []string {
	if len(skills) == 0 {
		return []string{styleNotice.Render("no skills found under .friday/skills/")}
	}
	lines := []string{styleNotice.Render("available skills — type /<name> then Enter:")}
	for _, s := range skills {
		line := stylePrompt.Render("  /" + s.Name)
		if s.Description != "" {
			line += styleNotice.Render(" — " + s.Description)
		}
		if strings.TrimSpace(s.Prompt) == "" {
			line += styleNotice.Render(" (docs only — no runnable prompt)")
		}
		lines = append(lines, line)
	}
	return lines
}
