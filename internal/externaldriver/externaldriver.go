package externaldriver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/segmentstream/axprobe/internal/box"
	"github.com/segmentstream/axprobe/internal/driver"
	"github.com/segmentstream/axprobe/internal/manifest"
)

type bridgeBox interface {
	NewCommandBridge() (*box.CommandBridge, error)
}

type finalResult struct {
	GoalReached  bool                 `json:"goal_reached"`
	Summary      string               `json:"summary"`
	Observations []driver.Observation `json:"observations"`
}

// Run drives a prepared box with an external local coding agent. The external
// agent runs on the host, but every product command goes through axprobe-bash,
// which executes inside the disposable box and logs transcript evidence.
func Run(ctx context.Context, b box.Box, m *manifest.Manifest, name, model string) (*driver.Result, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	if name != "codex" && name != "claude" {
		return nil, fmt.Errorf("unsupported driver %q (supported: axprobe, codex, claude)", name)
	}
	bridgeProvider, ok := b.(bridgeBox)
	if !ok {
		return nil, fmt.Errorf("driver %q requires a box that can expose a command bridge", name)
	}
	bridge, err := bridgeProvider.NewCommandBridge()
	if err != nil {
		return nil, err
	}
	defer bridge.Cleanup()

	version := versionString(name)
	prompt := promptFor(m, "./axprobe-bash")
	start := time.Now()
	stdout, stderr, exitCode, finalText, err := runAgent(ctx, name, model, bridge.Dir, prompt)
	if err != nil {
		return nil, err
	}

	transcript := parseBridgeLog(bridge.LogPath)
	parsed, parseOK := parseFinal(finalText + "\n" + stdout)
	res := &driver.Result{
		Driver:        name,
		DriverModel:   model,
		DriverVersion: version,
		Steps:         len(transcript),
		CommandsRun:   len(transcript),
		Transcript:    transcript,
		Commands:      commandsFromTranscript(transcript),
		DurationSec:   time.Since(start).Seconds(),
	}
	if parseOK {
		res.Reached = parsed.GoalReached
		res.Summary = strings.TrimSpace(parsed.Summary)
		res.Observations = parsed.Observations
	} else {
		res.Summary = "External driver did not return the required final JSON result."
		res.Observations = append(res.Observations, driver.Observation{
			Category: "missing_guidance",
			Note:     "The external driver finished without a parseable AXprobe final JSON result.",
		})
	}
	if strings.TrimSpace(res.Summary) == "" {
		if res.Reached {
			res.Summary = "External driver reported the goal reached."
		} else {
			res.Summary = "External driver reported the goal was not reached."
		}
	}
	if exitCode != 0 {
		res.Observations = append(res.Observations, driver.Observation{
			Category: "friction",
			Note:     fmt.Sprintf("External driver process exited non-zero (%d).", exitCode),
		})
		if !res.Reached {
			res.Summary = strings.TrimSpace(res.Summary + " External driver exit: " + firstNonEmpty(strings.TrimSpace(stderr), strings.TrimSpace(stdout)))
		}
	}
	if len(transcript) == 0 {
		res.Observations = append(res.Observations, driver.Observation{
			Category: "missing_guidance",
			Note:     "The external driver did not run any commands through axprobe-bash, so AXprobe could not capture in-box transcript evidence.",
		})
	}
	res.PostMortem = strings.TrimSpace(finalText)
	res.FinalizeForExternal()
	return res, nil
}

func promptFor(m *manifest.Manifest, bridgePath string) string {
	return fmt.Sprintf(`You are driving a CLI inside an AXprobe disposable box.

Goal:
%s

Rules:
- Run product commands ONLY through this bridge command:
  %s '<command>'
- Your current working directory contains that bridge script.
- Do not run the product CLI directly on the host.
- Do not inspect or modify host files except this temporary working directory.
- If you are stuck, say exactly where and why.
- Finish by printing ONLY this JSON object, with no markdown:
  {"goal_reached":true|false,"summary":"one concise paragraph","observations":[{"category":"missing_guidance|confusion|extra_steps|friction|unclear_interface","note":"...","suggestion":"optional"}]}

The tool under test is already installed in the box. Begin.
`, m.Goal, bridgePath)
}

func runAgent(ctx context.Context, name, model, dir, prompt string) (stdout, stderr string, exitCode int, finalText string, err error) {
	switch name {
	case "codex":
		return runCodex(ctx, model, dir, prompt)
	case "claude":
		return runClaude(ctx, model, dir, prompt)
	default:
		return "", "", 0, "", fmt.Errorf("unsupported driver %q", name)
	}
}

func runCodex(ctx context.Context, model, dir, prompt string) (string, string, int, string, error) {
	if _, err := exec.LookPath("codex"); err != nil {
		return "", "", 0, "", fmt.Errorf("driver codex requested but `codex` was not found on PATH")
	}
	finalPath := filepath.Join(dir, "final.txt")
	args := []string{
		"exec",
		"--skip-git-repo-check",
		"--cd", dir,
		"--dangerously-bypass-approvals-and-sandbox",
		"--output-last-message", finalPath,
		"-",
	}
	if strings.TrimSpace(model) != "" {
		args = append([]string{"exec", "-m", model}, args[1:]...)
	}
	return runProcess(ctx, dir, prompt, "codex", args, finalPath)
}

func runClaude(ctx context.Context, model, dir, prompt string) (string, string, int, string, error) {
	if _, err := exec.LookPath("claude"); err != nil {
		return "", "", 0, "", fmt.Errorf("driver claude requested but `claude` was not found on PATH")
	}
	args := []string{
		"-p",
		"--output-format", "json",
		"--permission-mode", "bypassPermissions",
		"--no-session-persistence",
		prompt,
	}
	if strings.TrimSpace(model) != "" {
		args = append([]string{"--model", model}, args...)
	}
	stdout, stderr, code, _, err := runProcess(ctx, dir, "", "claude", args, "")
	return stdout, stderr, code, stdout, err
}

func runProcess(ctx context.Context, dir, stdin, name string, args []string, finalPath string) (string, string, int, string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	runErr := cmd.Run()
	exitCode := 0
	if runErr != nil {
		if exitErr, ok := runErr.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			return out.String(), errBuf.String(), 0, "", fmt.Errorf("%s: %w", name, runErr)
		}
	}
	finalText := ""
	if finalPath != "" {
		if data, err := os.ReadFile(finalPath); err == nil {
			finalText = string(data)
		}
	}
	return out.String(), errBuf.String(), exitCode, finalText, nil
}

func versionString(name string) string {
	out, err := exec.Command(name, "--version").CombinedOutput()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func parseFinal(s string) (finalResult, bool) {
	if fr, ok := parseFinalObject(s); ok {
		return fr, true
	}
	for _, candidate := range jsonObjects(s) {
		var envelope struct {
			Result string `json:"result"`
		}
		if err := json.Unmarshal([]byte(candidate), &envelope); err == nil && envelope.Result != "" {
			if fr, ok := parseFinalObject(envelope.Result); ok {
				return fr, true
			}
		}
	}
	return finalResult{}, false
}

func parseFinalObject(s string) (finalResult, bool) {
	for _, candidate := range jsonObjects(s) {
		var raw map[string]json.RawMessage
		if err := json.Unmarshal([]byte(candidate), &raw); err != nil {
			continue
		}
		if _, ok := raw["goal_reached"]; !ok {
			continue
		}
		var fr finalResult
		if err := json.Unmarshal([]byte(candidate), &fr); err == nil {
			return fr, true
		}
	}
	return finalResult{}, false
}

func jsonObjects(s string) []string {
	var out []string
	start := -1
	depth := 0
	inString := false
	escaped := false
	for i, r := range s {
		if inString {
			if escaped {
				escaped = false
				continue
			}
			if r == '\\' {
				escaped = true
				continue
			}
			if r == '"' {
				inString = false
			}
			continue
		}
		switch r {
		case '"':
			inString = true
		case '{':
			if depth == 0 {
				start = i
			}
			depth++
		case '}':
			if depth > 0 {
				depth--
				if depth == 0 && start >= 0 {
					out = append(out, s[start:i+1])
				}
			}
		}
	}
	return out
}

var bridgeBlockRE = regexp.MustCompile(`(?s)<<<AXPROBE-CMD>>>\n(.*?)\n<<<AXPROBE-EXIT:(\d+)>>>\n(.*?)\n<<<AXPROBE-END>>>`)

func parseBridgeLog(path string) []driver.Step {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var steps []driver.Step
	for _, m := range bridgeBlockRE.FindAllStringSubmatch(string(data), -1) {
		steps = append(steps, driver.Step{
			Command:  strings.TrimSpace(m[1]),
			ExitCode: atoi(m[2]),
			Result:   summarize(m[3]),
		})
	}
	return steps
}

func summarize(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	lines := strings.Split(s, "\n")
	if len(lines) > 8 {
		lines = append(lines[:6], "...", lines[len(lines)-1])
	}
	out := strings.Join(lines, "\n")
	if len(out) > 2000 {
		out = strings.TrimSpace(out[:2000]) + " ..."
	}
	return out
}

func commandsFromTranscript(steps []driver.Step) []string {
	out := make([]string, 0, len(steps))
	for _, s := range steps {
		out = append(out, s.Command)
	}
	return out
}

func atoi(s string) int {
	var n int
	_, _ = fmt.Sscanf(s, "%d", &n)
	return n
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
