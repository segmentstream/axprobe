// Package explore is the manifest compiler: it drives a plain-language intent
// once, discovering credentials interactively as the agent hits gates, and
// synthesizes a reusable scenario manifest. The human describes intent and
// answers gates; they never hand-author the structured manifest.
//
// MVP scope: discovers kind:file credentials (the dominant case — a key file).
// value/oauth discovery come later.
package explore

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/segmentstream/axprobe/internal/box"
	"github.com/segmentstream/axprobe/internal/llm"
	"github.com/segmentstream/axprobe/internal/manifest"
	"github.com/segmentstream/axprobe/internal/secrets"
)

// DiscoveryBroker satisfies driver.Gatekeeper. On a gate it interactively learns
// a file credential, injects it, records the declaration for the manifest, and
// resumes the run.
type DiscoveryBroker struct {
	box        box.Box
	store      *secrets.Store
	llm        *llm.Client
	in         *bufio.Reader
	out        io.Writer
	counter    int
	Discovered []manifest.Credential
}

// NewDiscoveryBroker builds a discovery broker for one explore run. The llm
// client (optional) is used to propose a meaningful credential name.
func NewDiscoveryBroker(b box.Box, store *secrets.Store, client *llm.Client, in io.Reader, out io.Writer) *DiscoveryBroker {
	return &DiscoveryBroker{box: b, store: store, llm: client, in: bufio.NewReader(in), out: out}
}

// proposeName asks the model for a short snake_case credential name from the
// gate text. Falls back to credential_N on any error.
func (d *DiscoveryBroker) proposeName(needs string) string {
	fallback := fmt.Sprintf("credential_%d", d.counter)
	if d.llm == nil {
		return fallback
	}
	const sys = `Propose a short snake_case name for the credential a CLI is asking for. ` +
		`Return ONLY a JSON object like {"name":"bigquery_sa_key"}. No prose.`
	msg, _, err := d.llm.Chat(context.Background(),
		[]llm.Message{{Role: "system", Content: sys}, {Role: "user", Content: needs}}, nil)
	if err != nil {
		return fallback
	}
	var p struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal([]byte(extractJSON(msg.Content)), &p); err != nil || p.Name == "" {
		return fallback
	}
	return p.Name
}

// extractJSON returns the first {...} span of s, or s unchanged.
func extractJSON(s string) string {
	i := strings.IndexByte(s, '{')
	j := strings.LastIndexByte(s, '}')
	if i >= 0 && j > i {
		return s[i : j+1]
	}
	return s
}

// Resolve interactively discovers a file credential for the gate.
func (d *DiscoveryBroker) Resolve(needs string) (string, bool) {
	d.counter++
	fmt.Fprintf(d.out, "\n🔍 explore: the agent is blocked and needs a credential:\n   %s\n", needs)
	fmt.Fprintln(d.out, "   (MVP discovers file credentials. Press Enter at the path to stop here instead.)")

	name := d.ask("   credential name", d.proposeName(needs))
	path := expandHome(d.ask("   path to the file on your host", ""))
	if path == "" {
		fmt.Fprintln(d.out, "   no file given — stopping the run here.")
		return "", false
	}

	content, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(d.out, "   error reading %q: %v — stopping.\n", path, err)
		return "", false
	}

	boxPath := "/root/.axprobe/" + name
	if err := d.box.CopyIn(content, boxPath); err != nil {
		fmt.Fprintf(d.out, "   inject error: %v — stopping.\n", err)
		return "", false
	}

	cred := manifest.Credential{
		Name:   name,
		Kind:   "file",
		Prompt: needs,
		Inject: manifest.InjectSpec{BoxPath: boxPath},
	}
	d.Discovered = append(d.Discovered, cred)
	if err := d.store.Set(name, content); err != nil {
		fmt.Fprintf(d.out, "   warning: could not store credential: %v\n", err)
	}

	fmt.Fprintf(d.out, "   ✓ recorded credential %q (kind:file → %s)\n", name, boxPath)
	return fmt.Sprintf("Credential %q is now available in the sandbox at %s. Continue toward the goal.", name, boxPath), true
}

func (d *DiscoveryBroker) ask(label, def string) string {
	if def != "" {
		fmt.Fprintf(d.out, "%s [%s]> ", label, def)
	} else {
		fmt.Fprintf(d.out, "%s> ", label)
	}
	line, _ := d.in.ReadString('\n')
	v := strings.TrimSpace(line)
	if v == "" {
		return def
	}
	return v
}

// scenarioOut is the synthesized scenario, written as a clean .axprobe/<name>.yaml.
type scenarioOut struct {
	SchemaVersion string                `yaml:"schema_version"`
	Name          string                `yaml:"name"`
	Intent        string                `yaml:"intent"`
	Goal          string                `yaml:"goal"`
	Credentials   []manifest.Credential `yaml:"credentials,omitempty"`
}

// Synthesize writes the discovered scenario to .axprobe/<name>.yaml and returns
// the path. The box/install stays in .axprobe/config.yaml (inherited).
func Synthesize(name, intent string, creds []manifest.Credential) (string, error) {
	out := scenarioOut{
		SchemaVersion: manifest.SupportedSchemaVersion,
		Name:          name,
		Intent:        intent,
		Goal:          intent,
		Credentials:   creds,
	}
	data, err := yaml.Marshal(out)
	if err != nil {
		return "", err
	}

	if err := os.MkdirAll(".axprobe", 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(".axprobe", name+".yaml")
	header := "# Synthesized by `axprobe explore`. Review and edit before committing.\n" +
		"# The box/install is inherited from .axprobe/config.yaml.\n"
	if err := os.WriteFile(path, append([]byte(header), data...), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(p, "~"))
		}
	}
	return p
}
