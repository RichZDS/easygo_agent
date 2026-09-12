package skill

import (
	"fmt"
	"strings"
	"unicode"
)

const compactStub = "Skill catalog compacted for this turn: no specialized skill matched the current request. Call load_skill only if a later request matches a catalog entry."

// CompactInstruction drops unused catalog Directory rows from a system prompt.
// Matching is from the row's own name/use-when terms into the current situation,
// so unrelated long skill text can be removed before dialogue is summarized.
func CompactInstruction(instruction, situation string) (string, bool) {
	instruction = strings.TrimSpace(instruction)
	if instruction == "" {
		return instruction, false
	}
	dirIdx := strings.Index(instruction, "## Directory")
	if dirIdx < 0 && !strings.Contains(instruction, "Skill Catalog") {
		return instruction, false
	}
	head := instruction
	dir := ""
	if dirIdx >= 0 {
		head = strings.TrimSpace(instruction[:dirIdx])
		dir = instruction[dirIdx:]
	}
	kept := matchingDirectoryRows(dir, situation)
	if len(kept) == 0 {
		base := stripCatalogHead(head)
		out := strings.TrimSpace(base + "\n\n" + compactStub)
		return out, out != instruction
	}
	var b strings.Builder
	b.WriteString(head)
	b.WriteString("\n\n## Directory\n\n| Name | Use when |\n| --- | --- |\n")
	for _, row := range kept {
		fmt.Fprintf(&b, "| `%s` | %s |\n", row.Name, row.Description)
	}
	out := strings.TrimSpace(b.String())
	return out, out != instruction
}

func stripCatalogHead(head string) string {
	for _, marker := range []string{"# Skill Catalog", "This is the only skill registered", "Specialized procedures stay on disk"} {
		if idx := strings.Index(head, marker); idx > 0 {
			return strings.TrimSpace(head[:idx])
		}
	}
	return strings.TrimSpace(head)
}

func matchingDirectoryRows(directory, situation string) []Entry {
	var rows []Entry
	for _, line := range strings.Split(directory, "\n") {
		name, desc, ok := parseDirectoryRow(line)
		if !ok {
			continue
		}
		if catalogRowMatches(name, desc, situation) {
			rows = append(rows, Entry{Name: name, Description: desc})
		}
	}
	return rows
}

func parseDirectoryRow(line string) (name, desc string, ok bool) {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "|") {
		return "", "", false
	}
	parts := strings.Split(line, "|")
	if len(parts) < 4 {
		return "", "", false
	}
	name = strings.Trim(strings.TrimSpace(parts[1]), "`")
	desc = strings.TrimSpace(parts[2])
	if name == "" || name == "Name" || name == "---" || desc == "" || desc == "Use when" {
		return "", "", false
	}
	return name, desc, true
}

func catalogRowMatches(name, desc, situation string) bool {
	sit := foldText(situation)
	if sit == "" {
		return false
	}
	if strings.Contains(sit, foldText(name)) {
		return true
	}
	for _, term := range catalogTerms(name + " " + desc) {
		if strings.Contains(sit, foldText(term)) {
			return true
		}
	}
	return false
}

func catalogTerms(text string) []string {
	var terms []string
	var cjk []rune
	var ascii []rune
	flushCJK := func() {
		if len(cjk) >= 2 {
			terms = append(terms, string(cjk))
		}
		cjk = cjk[:0]
	}
	flushASCII := func() {
		if len(ascii) >= 4 {
			word := strings.ToLower(string(ascii))
			if !catalogStop[word] {
				terms = append(terms, word)
			}
		}
		ascii = ascii[:0]
	}
	for _, r := range text {
		switch {
		case unicode.Is(unicode.Han, r):
			flushASCII()
			cjk = append(cjk, r)
		case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-':
			flushCJK()
			ascii = append(ascii, r)
		default:
			flushCJK()
			flushASCII()
		}
	}
	flushCJK()
	flushASCII()
	return terms
}

func foldText(text string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return unicode.ToLower(r)
	}, text)
}

var catalogStop = map[string]bool{
	"when": true, "user": true, "wants": true, "with": true, "from": true,
	"this": true, "that": true, "into": true, "call": true, "load": true,
	"skill": true, "before": true, "after": true, "using": true, "asks": true,
}
