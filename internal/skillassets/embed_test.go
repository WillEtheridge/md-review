package skillassets

import (
	"bytes"
	"strings"
	"testing"
)

func TestCanonicalSkillIsEmbeddedAtChildPath(t *testing.T) {
	const wantPath = "mdreview/SKILL.md"
	if skillPath != wantPath {
		t.Fatalf("skillPath = %q, want child path %q", skillPath, wantPath)
	}

	skill, err := ReadSkill()
	if err != nil {
		t.Fatalf("read embedded canonical skill %q: %v", skillPath, err)
	}
	if len(skill) == 0 {
		t.Fatalf("embedded canonical skill %q is empty", skillPath)
	}
	if bytes.Contains(skill, []byte("MDREVIEW_PLACEHOLDER:SKILL")) {
		t.Fatal("embedded canonical skill still contains the release placeholder")
	}
}

func TestCanonicalSkillHasPortableFrontmatter(t *testing.T) {
	frontmatter, body := splitSkill(t)
	if len(frontmatter) != 2 {
		t.Fatalf("frontmatter has %d fields, want only name and description: %q", len(frontmatter), frontmatter)
	}
	if frontmatter[0] != "name: mdreview" {
		t.Fatalf("frontmatter name = %q, want %q", frontmatter[0], "name: mdreview")
	}
	const descriptionPrefix = "description: "
	if !strings.HasPrefix(frontmatter[1], descriptionPrefix) {
		t.Fatalf("second frontmatter field = %q, want description", frontmatter[1])
	}
	description := strings.TrimPrefix(frontmatter[1], descriptionPrefix)
	for _, trigger := range []string{"*.md.review.json", "Markdown", "viewer"} {
		if !strings.Contains(description, trigger) {
			t.Errorf("frontmatter description is missing trigger %q", trigger)
		}
	}
	if len(bytes.TrimSpace(body)) == 0 {
		t.Fatal("canonical skill instruction body is empty")
	}
}

func TestCanonicalSkillContainsPortableWorkflow(t *testing.T) {
	skill, err := ReadSkill()
	if err != nil {
		t.Fatalf("read embedded canonical skill %q: %v", skillPath, err)
	}
	normalizedSkill := strings.Join(strings.Fields(string(skill)), " ")

	requiredInstructions := []string{
		"*.md.review.json",
		"removing only the trailing `.review.json`",
		"`schemaVersion`",
		"exactly `1`",
		"duplicate thread IDs",
		"duplicate message IDs anywhere in the sidecar",
		"keys at any depth",
		"`status` is `open`",
		"Edit the Markdown before changing review state",
		"leave the thread `open`",
		"author.type` set to `agent`",
		"author.name` set to your agent name",
		"UTC time in RFC 3339 form",
		"`status` to `handled` only after",
		"Never set a thread to `resolved`",
		"never change any anchor",
		"every unknown schema-version-1 field",
		"arbitrary-precision numbers",
		"Immediately before replacing a sidecar, reread it",
		"atomically replace the sidecar",
		"residual race with uncoordinated direct writers",
		"files directly",
		"`mdreview [DIRECTORY]`",
		"their own foreground terminal",
		"does not open a browser automatically",
		"Do not use `--managed-session`, `nohup`",
		"Ctrl+C",
	}
	for _, instruction := range requiredInstructions {
		if !strings.Contains(normalizedSkill, instruction) {
			t.Errorf("canonical skill is missing instruction %q", instruction)
		}
	}

	for _, forbiddenClaim := range []string{
		"MDREVIEW_PLACEHOLDER:SKILL",
	} {
		if bytes.Contains(skill, []byte(forbiddenClaim)) {
			t.Errorf("canonical skill contains forbidden host or placeholder claim %q", forbiddenClaim)
		}
	}
	if len(skill) > 8*1024 {
		t.Errorf("canonical skill is %d bytes, want concise instructions no larger than 8 KiB", len(skill))
	}
}

func splitSkill(t *testing.T) ([]string, []byte) {
	t.Helper()
	skill, err := ReadSkill()
	if err != nil {
		t.Fatalf("read embedded canonical skill %q: %v", skillPath, err)
	}
	const opening = "---\n"
	if !bytes.HasPrefix(skill, []byte(opening)) {
		t.Fatal("canonical skill does not begin with YAML frontmatter")
	}
	remainder := skill[len(opening):]
	const closing = "\n---\n"
	end := bytes.Index(remainder, []byte(closing))
	if end < 0 {
		t.Fatal("canonical skill frontmatter has no closing delimiter")
	}
	frontmatter := strings.Split(string(remainder[:end]), "\n")
	body := remainder[end+len(closing):]
	return frontmatter, body
}
