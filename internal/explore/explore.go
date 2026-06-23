// Package explore is the manifest compiler: it drives a plain-language intent
// once, discovering credentials interactively as the agent hits gates, and
// synthesizes a reusable scenario manifest. The human describes intent and
// answers gates; they never hand-author the structured manifest.
//
// Discovery covers two credential kinds:
//   - file:  a key/credential file the user supplies a path to.
//   - oauth: a browser login. explore proposes the login command, runs it in the
//     box (loopback flow over a reserved callback port), and discovers where the
//     token lands by diffing the box filesystem around the login.
package explore

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/segmentstream/axprobe/internal/box"
	"github.com/segmentstream/axprobe/internal/browser"
	"github.com/segmentstream/axprobe/internal/llm"
	"github.com/segmentstream/axprobe/internal/manifest"
	"github.com/segmentstream/axprobe/internal/secrets"
)

// DefaultOAuthPort is the loopback callback port explore reserves at box-up. A
// browser login discovered mid-run binds the tool's redirect server to it, and
// the published port forwards the host's 127.0.0.1:<port> redirect into the box.
const DefaultOAuthPort = 8085

// DiscoveryBroker satisfies driver.Gatekeeper. On a gate it interactively learns
// a credential (file or oauth), provisions it in the box, records the declaration
// for the synthesized manifest, and resumes the run.
type DiscoveryBroker struct {
	box        box.Box
	store      *secrets.Store
	llm        *llm.Client
	in         *bufio.Reader
	out        io.Writer
	oauthPort  int
	counter    int
	Discovered []manifest.Credential
}

// NewDiscoveryBroker builds a discovery broker for one explore run. The llm
// client (optional) is used to classify the gate and propose a credential spec.
func NewDiscoveryBroker(b box.Box, store *secrets.Store, client *llm.Client, in io.Reader, out io.Writer) *DiscoveryBroker {
	return &DiscoveryBroker{box: b, store: store, llm: client, in: bufio.NewReader(in), out: out, oauthPort: DefaultOAuthPort}
}

// InterceptsBareLogins opts into the driver routing a login command the agent
// runs directly (rather than via the gate tool) through Resolve. In explore no
// credential is declared up front, so an un-intercepted login would block forever.
func (d *DiscoveryBroker) InterceptsBareLogins() bool { return true }

// credProposal is the model's read of a gate: what kind of credential is needed
// and, for oauth, how to log in.
type credProposal struct {
	Kind         string `json:"kind"` // "file" | "oauth"
	Name         string `json:"name"`
	Mode         string `json:"mode,omitempty"`          // oauth: "loopback" | "device"
	LoginCommand string `json:"login_command,omitempty"` // oauth: command that starts the login
}

// proposeCredential asks the model to classify the gate and propose a spec.
// Falls back to a file credential named credential_N on any error.
func (d *DiscoveryBroker) proposeCredential(needs string) credProposal {
	fallback := credProposal{Kind: "file", Name: fmt.Sprintf("credential_%d", d.counter)}
	if d.llm == nil {
		return fallback
	}
	sys := fmt.Sprintf(`A CLI agent is blocked at a credential or login gate. Classify it and propose a spec.
Return ONLY a JSON object, no prose:
{"kind":"file|oauth","name":"snake_case_name","mode":"loopback|device","login_command":"..."}
Rules:
- kind "file": a key/credential FILE must be supplied (e.g. a service-account JSON). Omit mode and login_command.
- kind "oauth": a BROWSER login is required. Set:
    mode = "loopback" for a localhost-redirect flow (the tool opens a URL and waits for a 127.0.0.1 callback), or "device" for a device-code flow.
    login_command = the exact shell command that starts the login (e.g. "segmentstream warehouse auth login").
  For a loopback flow the sandbox has reserved callback port %d — if the tool accepts a port flag, include it set to %d (e.g. append " --port %d").
- If the gate text quotes the exact command the agent ran, base login_command on THAT command, but for a loopback flow ensure the callback port flag is --port %d (add or replace it) and drop automation flags like --json.
- name: a short snake_case identifier for the credential.`, d.oauthPort, d.oauthPort, d.oauthPort, d.oauthPort)

	msg, _, err := d.llm.Chat(context.Background(),
		[]llm.Message{{Role: "system", Content: sys}, {Role: "user", Content: needs}}, nil)
	if err != nil {
		return fallback
	}
	var p credProposal
	if err := json.Unmarshal([]byte(extractJSON(msg.Content)), &p); err != nil || p.Name == "" {
		return fallback
	}
	if p.Kind != "oauth" {
		p.Kind = "file"
	}
	return p
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

// Resolve interactively discovers the credential the gate needs and resumes.
func (d *DiscoveryBroker) Resolve(needs string) (string, bool) {
	d.counter++
	p := d.proposeCredential(needs)
	if p.Kind == "oauth" {
		return d.resolveOAuth(needs, p)
	}
	return d.resolveFile(needs, p)
}

// resolveFile discovers a kind:file credential: the user supplies a host path,
// the file is injected into the box, and the declaration is recorded.
func (d *DiscoveryBroker) resolveFile(needs string, p credProposal) (string, bool) {
	fmt.Fprintf(d.out, "\n🔍 explore: the agent is blocked and needs a credential file:\n   %s\n", needs)
	fmt.Fprintln(d.out, "   (Press Enter at the path to stop the run here instead.)")

	name := d.ask("   credential name", p.Name)
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

	d.Discovered = append(d.Discovered, manifest.Credential{
		Name:   name,
		Kind:   "file",
		Prompt: needs,
		Inject: manifest.InjectSpec{BoxPath: boxPath},
	})
	if err := d.store.Set(name, content); err != nil {
		fmt.Fprintf(d.out, "   warning: could not store credential: %v\n", err)
	}

	fmt.Fprintf(d.out, "   ✓ recorded credential %q (kind:file → %s)\n", name, boxPath)
	return fmt.Sprintf("Credential %q is now available in the sandbox at %s. Continue toward the goal.", name, boxPath), true
}

// resolveOAuth discovers a kind:oauth credential. It proposes a login command
// (human-approved), runs it in the box so the user completes the browser step,
// then discovers where the token landed by diffing the box's home directory.
func (d *DiscoveryBroker) resolveOAuth(needs string, p credProposal) (string, bool) {
	mode := p.Mode
	if mode != "device" && mode != "loopback" {
		mode = "loopback"
	}
	fmt.Fprintf(d.out, "\n🔓 explore: the agent needs a browser login (oauth/%s):\n   %s\n", mode, needs)
	fmt.Fprintln(d.out, "   (Press Enter at the command to stop the run here instead.)")

	name := d.ask("   credential name", p.Name)
	login := d.ask("   login command to run in the sandbox", p.LoginCommand)
	if login == "" {
		fmt.Fprintln(d.out, "   no login command — stopping the run here.")
		return "", false
	}

	// Snapshot the box home before login so we can discover where the token lands.
	before := d.fileSnapshot()

	fmt.Fprintln(d.out, "   complete the browser login when the URL appears below:")
	fmt.Fprintf(d.out, "   $ %s\n", login)
	fmt.Fprintln(d.out, "   ──────── login output ────────")
	res, err := d.box.ExecStream(login, browser.TeeOpen(d.out))
	fmt.Fprintln(d.out, "   ──────────────────────────────")
	if err != nil {
		fmt.Fprintf(d.out, "   login error: %v — stopping.\n", err)
		return "", false
	}
	if res.ExitCode != 0 {
		fmt.Fprintf(d.out, "   login exited %d — stopping.\n", res.ExitCode)
		return "", false
	}

	tokenPaths := discoverTokenPaths(before, d.fileSnapshot())
	if len(tokenPaths) == 0 {
		fmt.Fprintln(d.out, "   note: no new files detected after login — token_paths left empty (re-runs will need the browser).")
	} else {
		fmt.Fprintf(d.out, "   discovered token_paths: %s\n", strings.Join(tokenPaths, ", "))
	}

	cred := manifest.Credential{
		Name:         name,
		Kind:         "oauth",
		Prompt:       needs,
		Mode:         mode,
		LoginCommand: login,
		TokenPaths:   tokenPaths,
	}
	if mode == "loopback" {
		cred.CallbackPort = d.oauthPort
	}
	d.Discovered = append(d.Discovered, cred)

	// Cache the token so the first `run` of the synthesized scenario needs no
	// browser. The store namespace + key match what the broker restores from.
	if len(tokenPaths) > 0 {
		if data, err := d.box.ArchiveOut(tokenPaths); err != nil {
			fmt.Fprintf(d.out, "   warning: could not capture token to cache: %v\n", err)
		} else if err := d.store.SetKeychainOnly(name+".token", data); err != nil {
			fmt.Fprintf(d.out, "   warning: could not cache token: %v\n", err)
		} else {
			fmt.Fprintf(d.out, "   ✓ cached login (next run needs no browser)\n")
		}
	}

	fmt.Fprintf(d.out, "   ✓ recorded credential %q (kind:oauth/%s)\n", name, mode)
	return fmt.Sprintf("Browser login %q completed in the sandbox; the tool is now authenticated. Continue toward the goal.", name), true
}

// fileSnapshot returns the set of regular files under the box home, used to
// detect what a login wrote.
func (d *DiscoveryBroker) fileSnapshot() map[string]bool {
	res, err := d.box.Exec(`find /root -type f 2>/dev/null`)
	if err != nil {
		return nil
	}
	set := map[string]bool{}
	for _, line := range strings.Split(res.Stdout, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			set[line] = true
		}
	}
	return set
}

// discoverTokenPaths returns the exact credential files that appeared between two
// home snapshots. Caching the precise new files (not a parent directory) is what
// keeps a multi-megabyte installed binary out of the cache: the binary was
// written during setup, so it is not "new" and never selected.
func discoverTokenPaths(before, after map[string]bool) []string {
	var files []string
	for f := range after {
		if before[f] || !isCacheableNewFile(f) {
			continue
		}
		files = append(files, f)
	}
	sort.Strings(files)
	return files
}

// isCacheableNewFile reports whether a newly-appeared file under the box home is
// worth caching as login state — i.e. not shell/cache noise.
func isCacheableNewFile(file string) bool {
	const root = "/root/"
	if !strings.HasPrefix(file, root) {
		return false
	}
	top := strings.SplitN(strings.TrimPrefix(file, root), "/", 2)[0]
	switch top {
	case ".bash_history", ".bashrc", ".profile", ".bash_logout", ".cache",
		".wget-hsts", ".lesshst", ".viminfo", ".sudo_as_admin_successful":
		return false
	}
	return true
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
	Expect        expectOut             `yaml:"expect"`
}

// expectOut is the starter AX bar written into every synthesized scenario, so the
// author edits a real definition-of-done instead of an absent one.
type expectOut struct {
	GoalReached           bool `yaml:"goal_reached"`
	MaxHumanInterventions int  `yaml:"max_human_interventions"`
	MaxFalseErrors        int  `yaml:"max_false_errors"`
}

// DistillGoal rewrites a raw intent into a user-level goal — the user's desired
// outcome with no tool/command/flag names — so the synthesized scenario passes
// its own goal lint instead of echoing the intent verbatim. Falls back to the
// intent if the model is absent or the call fails.
func DistillGoal(client *llm.Client, intent string) string {
	if client == nil {
		return intent
	}
	sys := "You refine AX-test scenarios. Rewrite the user's INTENT as a GOAL: one or two plain sentences stating the user's real-world desired OUTCOME and how they would know it worked. Never name any tool, CLI, product, command, subcommand, or flag (including any testing tool). Say WHAT the user wants to achieve, never HOW to do it. Reply with only the goal text."
	msg, _, err := client.Chat(context.Background(),
		[]llm.Message{{Role: "system", Content: sys}, {Role: "user", Content: "INTENT: " + intent}}, nil)
	if err != nil {
		return intent
	}
	if g := strings.TrimSpace(msg.Content); g != "" {
		return g
	}
	return intent
}

// Synthesize writes the discovered scenario to .axprobe/<name>.yaml and returns
// the path. The box/install stays in .axprobe/config.yaml (inherited). goal is the
// distilled user-level goal (see DistillGoal); intent is preserved verbatim.
func Synthesize(name, intent, goal string, creds []manifest.Credential) (string, error) {
	out := scenarioOut{
		SchemaVersion: manifest.SupportedSchemaVersion,
		Name:          name,
		Intent:        intent,
		Goal:          goal,
		Credentials:   creds,
		Expect:        expectOut{GoalReached: true, MaxHumanInterventions: len(creds), MaxFalseErrors: 0},
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
