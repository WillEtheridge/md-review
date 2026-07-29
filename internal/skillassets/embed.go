// Package skillassets owns the canonical Agent Skill bytes compiled into
// mdReview.
package skillassets

import "embed"

const skillPath = "mdreview/SKILL.md"

//go:embed mdreview/SKILL.md
var files embed.FS

// ReadSkill returns the canonical Agent Skill compiled into mdReview.
func ReadSkill() ([]byte, error) {
	return files.ReadFile(skillPath)
}
