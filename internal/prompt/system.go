// Package prompt 管理 Agent 系统提示词。
package prompt

import "strings"

const SystemPrompt = "You are a helpful assistant."

// WithSkillCatalog appends the heuristic catalog. Specialized skill bodies are
// not included; the agent loads those through load_skill.
func WithSkillCatalog(base, catalog string) string {
	base = strings.TrimSpace(base)
	catalog = strings.TrimSpace(catalog)
	if catalog == "" {
		return base
	}
	if base == "" {
		return catalog
	}
	return base + "\n\n" + catalog
}
