package main

import (
	"strings"
	"testing"

	"easygo-agent/internal/agent/deepagent"
)

func TestUltraLongEvalCorpusIsThemedNotRepeatedFiller(t *testing.T) {
	turns := deepagent.ThemedUserTurns()
	if err := deepagent.RejectRepeatedFiller(turns); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(turns, "\n")
	if !strings.Contains(joined, "玄枢台账") || !strings.Contains(joined, "EG-7741") {
		t.Fatal("themed corpus missing early facts")
	}
	if strings.Contains(joined, "这是一段与内部工具无关的填充说明") {
		t.Fatal("old 一段话*n padding leaked into the themed corpus")
	}
}

func TestPartitionFoundIgnoresCaseAndSpaces(t *testing.T) {
	found, missing := partitionFound("工号是 EG-7741，内部工具叫玄枢台账。", []string{"EG-7741", "玄枢台账", "周五"})
	if len(found) != 2 || len(missing) != 1 || missing[0] != "周五" {
		t.Fatalf("found=%v missing=%v", found, missing)
	}
	if !containsAll("截止日期 3 月 18 日，对接人王敏", []string{"3月18", "王敏"}) {
		t.Fatal("short-term needles should match collapsed spaces")
	}
}
