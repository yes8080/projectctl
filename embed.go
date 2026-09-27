// Package projectctl contains the skill embedded in every release binary.
package projectctl

import "embed"

// SkillAssets ships portable instructions; runtime functionality lives in Go.
//
//go:embed skills/github-project-control/SKILL.md skills/github-project-control/agents skills/github-project-control/references
var SkillAssets embed.FS
