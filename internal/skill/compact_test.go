package skill

import (
	"strings"
	"testing"
)

func TestCompactInstructionDropsUnmatchedRows(t *testing.T) {
	instruction := "You are a helpful assistant.\n\n# Skill Catalog\nLoad skills first.\n\n## Directory\n\n| Name | Use when |\n| --- | --- |\n| `playing-maze` | Use when the user wants a 迷宫 of 墙 路 起 终 |\n| `strict-arithmetic` | Use when the user asks for a numeric calculation |\n"
	got, changed := CompactInstruction(instruction, "玄枢台账周五能不能发生产？工号 EG-7741。")
	if !changed {
		t.Fatal("expected catalog to shrink")
	}
	for _, leftover := range []string{"playing-maze", "strict-arithmetic", "# Skill Catalog"} {
		if strings.Contains(got, leftover) {
			t.Fatalf("unused catalog still present (%s): %s", leftover, got)
		}
	}
	if !strings.Contains(got, "You are a helpful assistant.") {
		t.Fatalf("base instruction lost: %s", got)
	}
}

func TestCompactInstructionKeepsMatchingMazeRow(t *testing.T) {
	instruction := "You are a helpful assistant.\n\n## Directory\n\n| Name | Use when |\n| --- | --- |\n| `playing-maze` | Use when the user wants a 迷宫 of 墙 路 起 终 |\n| `strict-arithmetic` | Use when the user asks for a numeric calculation |\n"
	got, changed := CompactInstruction(instruction, "请做一个爱心形状的迷宫，终点在内部。")
	if !changed {
		t.Fatal("expected unmatched arithmetic row to drop")
	}
	if !strings.Contains(got, "playing-maze") || !strings.Contains(got, "迷宫") {
		t.Fatalf("maze row dropped: %s", got)
	}
	if strings.Contains(got, "strict-arithmetic") {
		t.Fatalf("arithmetic row kept: %s", got)
	}
}
