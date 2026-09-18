package task

import (
	"fmt"
	"strings"
)

// Markdown is a portable report; checkpoints always remain in the database.
func Markdown(t Task) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Task %s\n\nVersion: %d · Status: %s · Role: %s\n\n%s\n\n", t.ID, t.Version, t.Status, t.Brief.Role, t.Brief.Goal)
	for _, section := range []struct {
		name  string
		items []string
	}{{"Constraints", t.Brief.Constraints}, {"Acceptance", t.Brief.Acceptance}, {"Evidence", t.Result.Evidence}, {"Unmet", t.Result.Unmet}} {
		if len(section.items) == 0 {
			continue
		}
		fmt.Fprintf(&b, "## %s\n\n", section.name)
		for _, item := range section.items {
			fmt.Fprintf(&b, "- %s\n", item)
		}
		b.WriteByte('\n')
	}
	fmt.Fprintf(&b, "## Result\n\n%s\n\n%s\n\n## Events\n\n", t.Result.Summary, t.Reason)
	for _, event := range t.Events {
		fmt.Fprintf(&b, "- %d · v%d · %s: %s\n", event.Seq, event.Version, event.Kind, event.Detail)
	}
	return b.String()
}
