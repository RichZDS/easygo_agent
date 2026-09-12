package maze

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sampleGrid() [][]string {
	return [][]string{
		{"墙", "墙", "墙", "墙"},
		{"墙", "起", "路", "墙"},
		{"墙", "路", "终", "墙"},
		{"墙", "墙", "墙", "墙"},
	}
}

func TestMakeAndRunReachesTheEnd(t *testing.T) {
	store := NewStore(t.TempDir())
	record, err := store.Make("小径", "两步到终点", sampleGrid())
	if err != nil {
		t.Fatal(err)
	}
	if record.ID == "" || record.Name != "小径" || record.Describe != "两步到终点" {
		t.Fatalf("record=%+v", record)
	}
	result, err := store.Run(record.ID, []int{2, 4})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Reached || result.Stopped != "终" {
		t.Fatalf("result=%+v", result)
	}
	if !strings.Contains(result.Grid, "人") {
		t.Fatalf("rendered grid missing person: %s", result.Grid)
	}
}

func TestRunStopsOnWall(t *testing.T) {
	store := NewStore(t.TempDir())
	record, err := store.Make("死路", "撞墙", sampleGrid())
	if err != nil {
		t.Fatal(err)
	}
	result, err := store.Run(record.ID, []int{4})
	if err != nil {
		t.Fatal(err)
	}
	if result.Reached || result.Reason == "" {
		t.Fatalf("wall should stop the run: %+v", result)
	}
}

func TestListAndDetailRoundTripOnDisk(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	first, err := store.Make("小径", "两步到终点", sampleGrid())
	if err != nil {
		t.Fatal(err)
	}
	listed, err := store.List()
	if err != nil || len(listed) != 1 || listed[0].ID != first.ID {
		t.Fatalf("list=%+v err=%v", listed, err)
	}
	got, err := store.Detail(first.ID)
	if err != nil || got.Name != "小径" || len(got.Grid) != 4 {
		t.Fatalf("detail=%+v err=%v", got, err)
	}
	if _, err = os.Stat(filepath.Join(dir, first.ID+".json")); err != nil {
		t.Fatal(err)
	}
}

func TestMakeRejectsGridWithoutStartAndEnd(t *testing.T) {
	store := NewStore(t.TempDir())
	if _, err := store.Make("坏", "", [][]string{{"墙", "路"}, {"路", "墙"}}); err == nil {
		t.Fatal("invalid grid accepted")
	}
}
