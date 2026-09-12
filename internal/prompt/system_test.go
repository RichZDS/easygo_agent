package prompt

import "testing"

func TestWithSkillCatalogKeepsBaseAndAppendsHeuristicOnly(t *testing.T) {
	got := WithSkillCatalog("You are a helpful assistant.", "# Skill Catalog\nCall load_skill.")
	if got != "You are a helpful assistant.\n\n# Skill Catalog\nCall load_skill." {
		t.Fatalf("got %q", got)
	}
}
