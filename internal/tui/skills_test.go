package tui

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseSkillFrontmatter(t *testing.T) {
	sk := parseSkillFrontmatter("---\nname: start\ndescription: kick off trading\nprompt: 開始交易。立即分析。\n---\n# start\nbody…")
	if sk.Name != "start" {
		t.Errorf("name = %q; want start", sk.Name)
	}
	if sk.Description != "kick off trading" {
		t.Errorf("description = %q", sk.Description)
	}
	if sk.Prompt != "開始交易。立即分析。" {
		t.Errorf("prompt = %q", sk.Prompt)
	}
}

func TestParseSkillFrontmatter_NoFrontmatter(t *testing.T) {
	sk := parseSkillFrontmatter("# just a heading\nno frontmatter here")
	if sk.Name != "" || sk.Prompt != "" {
		t.Errorf("expected empty skill for a file without frontmatter, got %+v", sk)
	}
}

func TestLoadSkills(t *testing.T) {
	root := t.TempDir()
	// A runnable skill with frontmatter.
	writeSkill(t, root, "start", "---\nname: start\ndescription: begin\nprompt: go now\n---\n# start\n")
	// A docs-only skill (no prompt) whose name falls back to the directory.
	writeSkill(t, root, "notes", "# notes\njust docs, no frontmatter\n")

	skills := loadSkills(root)
	if len(skills) != 2 {
		t.Fatalf("loaded %d skills; want 2", len(skills))
	}
	// Sorted by name: notes, start.
	if skills[0].Name != "notes" || skills[1].Name != "start" {
		t.Fatalf("unexpected order/names: %+v", skills)
	}
	if skills[1].Prompt != "go now" {
		t.Errorf("start prompt = %q; want 'go now'", skills[1].Prompt)
	}
	if skills[0].Prompt != "" {
		t.Errorf("docs-only skill should have no prompt, got %q", skills[0].Prompt)
	}

	if sk, ok := findSkill(skills, "START"); !ok || sk.Name != "start" {
		t.Errorf("findSkill is case-insensitive; got %+v ok=%v", sk, ok)
	}
	if _, ok := findSkill(skills, "missing"); ok {
		t.Error("findSkill should miss an unknown name")
	}
	if m := matchSkills(skills, "st"); len(m) != 1 || m[0].Name != "start" {
		t.Errorf("matchSkills(\"st\") = %+v; want [start]", m)
	}
	if m := matchSkills(skills, ""); len(m) != 2 {
		t.Errorf("empty prefix should match all, got %d", len(m))
	}
}

func TestLoadSkills_MissingDir(t *testing.T) {
	if s := loadSkills(t.TempDir()); s != nil {
		t.Errorf("no .friday/skills dir should yield nil, got %+v", s)
	}
}

func writeSkill(t *testing.T, root, name, content string) {
	t.Helper()
	dir := filepath.Join(root, ".friday", "skills", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
