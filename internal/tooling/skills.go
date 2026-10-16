package tooling

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

type Skill struct {
	ID            string      `json:"id"`
	Title         string      `json:"title"`
	IncidentTypes []string    `json:"incident_types"`
	Steps         []SkillStep `json:"steps"`
	Instructions  string      `json:"instructions"`
}
type SkillStep struct {
	ID        string         `json:"id"`
	Title     string         `json:"title"`
	Agent     string         `json:"agent"`
	Tool      string         `json:"tool"`
	Arguments map[string]any `json:"arguments"`
}

// LoadSkills loads readable instructions together with validated executable plans.
func LoadSkills(projectRoot string) ([]Skill, error) {
	paths, err := filepath.Glob(filepath.Join(projectRoot, "skills", "*", "plan.json"))
	if err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("no executable skills in %s", filepath.Join(projectRoot, "skills"))
	}
	skills := make([]Skill, 0, len(paths))
	seen := map[string]bool{}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		var skill Skill
		if err = json.Unmarshal(data, &skill); err != nil {
			return nil, fmt.Errorf("skill %s: %w", path, err)
		}
		if skill.ID == "" || skill.Title == "" || len(skill.Steps) == 0 || len(skill.IncidentTypes) == 0 || seen[skill.ID] {
			return nil, fmt.Errorf("invalid or duplicate skill %s", path)
		}
		seen[skill.ID] = true
		if skill.ID != filepath.Base(filepath.Dir(path)) {
			return nil, fmt.Errorf("skill id does not match directory: %s", path)
		}
		steps := map[string]bool{}
		for _, step := range skill.Steps {
			virtual := step.Tool == "search_knowledge" || step.Tool == "search_memory" || step.Tool == "plan_recovery" || step.Tool == "write_postmortem"
			if step.ID == "" || steps[step.ID] || step.Agent == "" || (!readNames[step.Tool] && !virtual) {
				return nil, fmt.Errorf("invalid or unsafe step in %s: %s", skill.ID, step.ID)
			}
			steps[step.ID] = true
		}
		instructions, err := os.ReadFile(filepath.Join(filepath.Dir(path), "SKILL.md"))
		if err != nil {
			return nil, err
		}
		skill.Instructions = string(instructions)
		skills = append(skills, skill)
	}
	sort.Slice(skills, func(i, j int) bool { return skills[i].ID < skills[j].ID })
	return skills, nil
}
