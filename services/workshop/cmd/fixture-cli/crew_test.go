package main

import (
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestCrewFixtureHelper(t *testing.T) {
	if os.Getenv("EASYGO_FIXTURE_HELPER") != "1" {
		return
	}
	main()
}
func TestCrewFixtureResumeInput(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"\n\nUnread messages from the foreman:\n- [reply-id] continue", "\n" + strings.Repeat("界", 30000)} {
		input := `{"mode":"crew","script":[{"op":"echo_input"}]}` + suffix
		cmd := exec.Command(binary, "-test.run=^TestCrewFixtureHelper$")
		cmd.Env = append(os.Environ(), "EASYGO_FIXTURE_HELPER=1", "EASYGO_CREW_TOKEN=fake-nonsecret-token")
		cmd.Stdin = strings.NewReader("workflow\n\nUser input:\n" + input)
		raw, err := cmd.Output()
		if err != nil {
			t.Fatal("trailing resume text rejected", err)
		}
		decoder := json.NewDecoder(strings.NewReader(string(raw)))
		result := ""
		success := false
		for {
			var event struct {
				Type string
				Item struct{ Text string }
			}
			err := decoder.Decode(&event)
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			if event.Type == "item.completed" {
				result += event.Item.Text
			}
			if event.Type == "turn.completed" {
				success = true
			}
		}
		if !success || len(result) > 64<<10 || !strings.HasPrefix(input, result) || len(result) == 0 {
			t.Fatal("echo output mismatch")
		}
		if len(input) <= 64<<10 && result != input {
			t.Fatal("full resume input not echoed")
		}
	}
}
