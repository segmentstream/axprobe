package externaldriver

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseFinalFromPlainJSON(t *testing.T) {
	got, ok := parseFinal(`{"goal_reached":true,"summary":"done","observations":[{"category":"friction","note":"slow"}]}`)
	if !ok {
		t.Fatal("expected parse")
	}
	if !got.GoalReached || got.Summary != "done" || len(got.Observations) != 1 {
		t.Fatalf("unexpected final result: %+v", got)
	}
}

func TestParseFinalFromClaudeEnvelope(t *testing.T) {
	got, ok := parseFinal(`{"type":"result","result":"{\"goal_reached\":false,\"summary\":\"stuck\",\"observations\":[]}"}`)
	if !ok {
		t.Fatal("expected parse")
	}
	if got.GoalReached || got.Summary != "stuck" {
		t.Fatalf("unexpected final result: %+v", got)
	}
}

func TestParseFinalFromRepeatedClaudeEnvelope(t *testing.T) {
	envelope := `{"type":"result","result":"prose\n{\"goal_reached\":true,\"summary\":\"done\",\"observations\":[]}"}`
	got, ok := parseFinal(envelope + "\n" + envelope)
	if !ok {
		t.Fatal("expected parse")
	}
	if !got.GoalReached || got.Summary != "done" {
		t.Fatalf("unexpected final result: %+v", got)
	}
}

func TestParseBridgeLog(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "transcript.log")
	body := `
<<<AXPROBE-CMD>>>
tool --help
<<<AXPROBE-EXIT:0>>>
usage
<<<AXPROBE-END>>>

<<<AXPROBE-CMD>>>
tool run
<<<AXPROBE-EXIT:1>>>
failed
<<<AXPROBE-END>>>
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	steps := parseBridgeLog(path)
	if len(steps) != 2 {
		t.Fatalf("expected 2 steps, got %d", len(steps))
	}
	if steps[0].Command != "tool --help" || steps[0].ExitCode != 0 || steps[0].Result != "usage" {
		t.Fatalf("unexpected first step: %+v", steps[0])
	}
	if steps[1].Command != "tool run" || steps[1].ExitCode != 1 || steps[1].Result != "failed" {
		t.Fatalf("unexpected second step: %+v", steps[1])
	}
}
