package main

import "testing"

func TestPartitionFoundIgnoresCaseAndSpaces(t *testing.T) {
	found, missing := partitionFound("工号是 EG-7741，内部工具叫玄枢台账。", []string{"EG-7741", "玄枢台账", "周五"})
	if len(found) != 2 || len(missing) != 1 || missing[0] != "周五" {
		t.Fatalf("found=%v missing=%v", found, missing)
	}
	if !containsAll("截止日期 3 月 18 日，对接人王敏", []string{"3月18", "王敏"}) {
		t.Fatal("short-term needles should match collapsed spaces")
	}
}
