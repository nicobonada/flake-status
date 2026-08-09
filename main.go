// flake-up: survey flake inputs under ~/src, multi-select with fzf, update locks.
//
// Pure fzf UI (no subcommands). Same update for every repo:
//   nix flake update --flake <path>  then commit flake.lock if dirty.
// Does not activate systems (no nh switch).
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 1 && (args[0] == "-h" || args[0] == "--help") {
		fmt.Print(helpText)
		return 0
	}
	if len(args) > 0 {
		fmt.Fprintln(os.Stderr, "flake-up: pure fzf UI — no CLI subcommands.")
		fmt.Fprintln(os.Stderr, "  Run with no arguments. Agents: nix flake update --flake <path>")
		return 2
	}

	srcRoot, err := srcRoot()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

	paths, err := discoverFlakes(srcRoot)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if len(paths) == 0 {
		fmt.Fprintf(os.Stderr, "no flakes under %s (need flake.nix + flake.lock)\n", srcRoot)
		return 1
	}

	labels := make([]string, len(paths))
	for i, p := range paths {
		labels[i] = labelFor(p, srcRoot)
	}
	fmt.Printf("checking %d flake(s) under %s: %s …\n", len(paths), srcRoot, strings.Join(labels, ", "))

	cache := newMetaCache()
	statuses := survey(paths, srcRoot, cache)
	byLabel := make(map[string]flakeStatus, len(statuses))
	for _, st := range statuses {
		byLabel[st.label] = st
	}

	selected, err := fzfMulti(fzfLines(statuses), "update> ",
		"TAB multi-select · ctrl-a toggle all · enter update · esc cancel")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if selected == nil {
		fmt.Println("cancelled")
		return 0
	}
	if len(selected) == 0 {
		fmt.Println("nothing selected")
		return 0
	}

	var chosen []flakeStatus
	for _, lab := range parseSelectedLabels(selected) {
		st, ok := byLabel[lab]
		if !ok {
			fmt.Fprintf(os.Stderr, "unknown selection %q; skip\n", lab)
			continue
		}
		chosen = append(chosen, st)
	}
	if len(chosen) == 0 {
		fmt.Println("nothing to update")
		return 0
	}

	names := make([]string, len(chosen))
	for i, st := range chosen {
		names[i] = st.label
	}
	ok, err := fzfConfirm(fmt.Sprintf("Update locks for %s?", strings.Join(names, ", ")))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if !ok {
		fmt.Println("aborted")
		return 0
	}

	for _, st := range chosen {
		if err := updateOne(st.path, st.label); err != nil {
			fmt.Fprintf(os.Stderr, "update failed for %s: %v\n", st.label, err)
			return 1
		}
	}
	fmt.Println("done")
	return 0
}

const helpText = `flake-up — survey ~/src flakes, fzf multi-select, update locks.

  flake-up          interactive UI
  flake-up --help   this text

Same action for every selected repo:
  nix flake update --flake <path>
  commit flake.lock if changed (jj or git)

Does not run nh / OS switch. Pure fzf — no check/update subcommands.
`

func srcRoot() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "src"), nil
}

func discoverFlakes(root string) ([]string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		dir := filepath.Join(root, e.Name())
		if fileExists(filepath.Join(dir, "flake.nix")) && fileExists(filepath.Join(dir, "flake.lock")) {
			out = append(out, dir)
		}
	}
	sort.Strings(out)
	return out, nil
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

func labelFor(path, root string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return path
	}
	if !strings.Contains(rel, string(filepath.Separator)) {
		return rel
	}
	return rel
}

// --- lock survey ---

type kind string

const (
	kindOK    kind = "ok"
	kindStale kind = "stale"
	kindError kind = "error"
)

type flakeStatus struct {
	path    string
	label   string
	kind    kind
	details []string
}

type metaCache struct {
	mu   sync.Mutex
	data map[string]metaResult
}

type metaResult struct {
	meta map[string]any
	err  error
}

func newMetaCache() *metaCache {
	return &metaCache{data: make(map[string]metaResult)}
}

func (c *metaCache) get(url string) (map[string]any, error) {
	c.mu.Lock()
	if hit, ok := c.data[url]; ok {
		c.mu.Unlock()
		return hit.meta, hit.err
	}
	c.mu.Unlock()

	meta, err := fetchMetadata(url)

	c.mu.Lock()
	c.data[url] = metaResult{meta: meta, err: err}
	c.mu.Unlock()
	return meta, err
}

func survey(paths []string, root string, cache *metaCache) []flakeStatus {
	type item struct {
		path string
		st   flakeStatus
	}
	ch := make(chan item, len(paths))
	var wg sync.WaitGroup
	workers := len(paths)
	if workers > 8 {
		workers = 8
	}
	sem := make(chan struct{}, workers)

	for _, p := range paths {
		wg.Add(1)
		go func(path string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			label := labelFor(path, root)
			stale, details, err := checkFlake(path, cache)
			st := flakeStatus{path: path, label: label}
			if err != nil {
				st.kind = kindError
				st.details = []string{err.Error()}
			} else if len(stale) == 0 {
				st.kind = kindOK
			} else {
				st.kind = kindStale
				st.details = details
			}
			ch <- item{path: path, st: st}
		}(p)
	}
	go func() {
		wg.Wait()
		close(ch)
	}()

	byPath := make(map[string]flakeStatus, len(paths))
	for it := range ch {
		byPath[it.path] = it.st
	}
	out := make([]flakeStatus, 0, len(paths))
	for _, p := range paths {
		out = append(out, byPath[p])
	}
	return out
}

func checkFlake(flake string, cache *metaCache) (stale []string, details []string, err error) {
	lockPath := filepath.Join(flake, "flake.lock")
	raw, err := os.ReadFile(lockPath)
	if err != nil {
		return nil, nil, err
	}
	var lock struct {
		Root  string                     `json:"root"`
		Nodes map[string]json.RawMessage `json:"nodes"`
	}
	if err := json.Unmarshal(raw, &lock); err != nil {
		return nil, nil, err
	}
	rootRaw, ok := lock.Nodes[lock.Root]
	if !ok {
		return nil, nil, fmt.Errorf("root node %q missing", lock.Root)
	}
	var rootNode struct {
		Inputs map[string]json.RawMessage `json:"inputs"`
	}
	if err := json.Unmarshal(rootRaw, &rootNode); err != nil {
		return nil, nil, err
	}

	type job struct {
		name string
		node json.RawMessage
	}
	var jobs []job
	for name, ref := range rootNode.Inputs {
		// follows is a JSON array — skip
		if len(ref) > 0 && ref[0] == '[' {
			continue
		}
		var nodeKey string
		if err := json.Unmarshal(ref, &nodeKey); err != nil {
			continue
		}
		node, ok := lock.Nodes[nodeKey]
		if !ok {
			continue
		}
		jobs = append(jobs, job{name: name, node: node})
	}

	type result struct {
		name   string
		reason string // empty = ok
	}
	resCh := make(chan result, len(jobs))
	var jwg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for _, j := range jobs {
		jwg.Add(1)
		go func(j job) {
			defer jwg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			reason := checkInput(j.name, j.node, cache)
			resCh <- result{name: j.name, reason: reason}
		}(j)
	}
	go func() {
		jwg.Wait()
		close(resCh)
	}()

	for r := range resCh {
		if r.reason == "" {
			continue
		}
		stale = append(stale, r.name)
		details = append(details, fmt.Sprintf("%s: %s", r.name, r.reason))
	}
	sort.Strings(stale)
	sort.Strings(details)
	return stale, details, nil
}

func checkInput(name string, nodeRaw json.RawMessage, cache *metaCache) string {
	var node struct {
		Original map[string]any `json:"original"`
		Locked   map[string]any `json:"locked"`
	}
	if err := json.Unmarshal(nodeRaw, &node); err != nil {
		return "error: " + err.Error()
	}
	if node.Original == nil {
		return "error: missing original"
	}

	// path inputs track the live tree
	if t, _ := node.Original["type"].(string); t == "path" {
		p, _ := node.Original["path"].(string)
		if p == "" {
			return "error: path input missing path"
		}
		if _, err := os.Stat(p); err != nil {
			return "error: path missing: " + p
		}
		return ""
	}

	url, err := flakeRef(node.Original)
	if err != nil {
		return "error: " + err.Error()
	}
	meta, err := cache.get(url)
	if err != nil {
		return "error: " + err.Error()
	}
	haveK, haveV, err := lockedID(node.Locked)
	if err != nil {
		return "error: " + err.Error()
	}
	tipK, tipV, err := tipID(meta)
	if err != nil {
		return "error: " + err.Error()
	}
	if haveK == tipK {
		if haveV == tipV {
			return ""
		}
		return "stale"
	}
	if haveV != tipV {
		return "stale"
	}
	return ""
}

func flakeRef(original map[string]any) (string, error) {
	t, _ := original["type"].(string)
	switch t {
	case "github":
		owner, _ := original["owner"].(string)
		repo, _ := original["repo"].(string)
		ref := fmt.Sprintf("github:%s/%s", owner, repo)
		if r, ok := original["ref"].(string); ok && r != "" {
			ref += "/" + r
		}
		return ref, nil
	case "git":
		url, _ := original["url"].(string)
		if !hasPrefixAny(url, "git+", "github:", "sourcehut:", "hg+") {
			url = "git+" + url
		}
		if r, ok := original["ref"].(string); ok && r != "" {
			if strings.Contains(url, "?") {
				url += "&ref=" + r
			} else {
				url += "?ref=" + r
			}
		}
		return url, nil
	case "tarball":
		url, _ := original["url"].(string)
		return url, nil
	case "path":
		p, _ := original["path"].(string)
		return p, nil
	case "indirect":
		id, _ := original["id"].(string)
		if r, ok := original["ref"].(string); ok && r != "" {
			return "flake:" + id + "/" + r, nil
		}
		return "flake:" + id, nil
	default:
		return "", fmt.Errorf("unsupported input type %q", t)
	}
}

func hasPrefixAny(s string, prefixes ...string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

func fetchMetadata(url string) (map[string]any, error) {
	cmd := exec.Command("nix", "flake", "metadata", url, "--json")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		if msg == "" {
			msg = err.Error()
		}
		lines := strings.Split(msg, "\n")
		return nil, fmt.Errorf("%s", lines[len(lines)-1])
	}
	var meta map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &meta); err != nil {
		return nil, err
	}
	return meta, nil
}

func tipID(meta map[string]any) (key, val string, err error) {
	if rev, ok := meta["revision"].(string); ok && rev != "" {
		return "rev", rev, nil
	}
	if locked, ok := meta["locked"].(map[string]any); ok {
		if rev, ok := locked["rev"].(string); ok && rev != "" {
			return "rev", rev, nil
		}
		if nar, ok := locked["narHash"].(string); ok && nar != "" {
			return "narHash", nar, nil
		}
	}
	return "", "", fmt.Errorf("metadata has neither revision nor narHash")
}

func lockedID(locked map[string]any) (key, val string, err error) {
	if locked == nil {
		return "", "", fmt.Errorf("missing locked")
	}
	if rev, ok := locked["rev"].(string); ok && rev != "" {
		return "rev", rev, nil
	}
	if nar, ok := locked["narHash"].(string); ok && nar != "" {
		return "narHash", nar, nil
	}
	return "", "", fmt.Errorf("lock has neither rev nor narHash")
}

// --- fzf ---

func colorEnabled() bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}

func paint(text, code string) string {
	if !colorEnabled() {
		return text
	}
	return "\033[" + code + "m" + text + "\033[0m"
}

func statusMark(k kind) string {
	switch k {
	case kindOK:
		return paint("✓", "32")
	case kindStale:
		return paint("✗", "31")
	default:
		return paint("!", "33")
	}
}

func fzfLines(statuses []flakeStatus) string {
	var b strings.Builder
	for _, st := range statuses {
		var summary string
		switch st.kind {
		case kindOK:
			summary = "up to date"
		case kindStale:
			names := make([]string, 0, len(st.details))
			for _, d := range st.details {
				if i := strings.Index(d, ":"); i >= 0 {
					names = append(names, d[:i])
				} else {
					names = append(names, d)
				}
			}
			summary = strings.Join(names, ", ")
		default:
			if len(st.details) > 0 {
				summary = st.details[0]
			} else {
				summary = "error"
			}
		}
		fmt.Fprintf(&b, "%s\t%s\t%s\n", statusMark(st.kind), st.label, summary)
	}
	return b.String()
}

func fzfMulti(lines, prompt, header string) ([]string, error) {
	if _, err := exec.LookPath("fzf"); err != nil {
		return nil, fmt.Errorf("flake-up: fzf not found on PATH")
	}
	if !isInteractive() {
		return nil, fmt.Errorf("flake-up: needs an interactive TTY")
	}
	cmd := exec.Command("fzf",
		"--ansi",
		"--multi",
		"--delimiter=\t",
		"--with-nth=1,2,3",
		"--prompt="+prompt,
		"--header="+header,
		"--height=100%",
		"--reverse",
		"--bind=ctrl-a:toggle-all",
	)
	cmd.Stdin = strings.NewReader(lines)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		// esc / non-zero = cancel
		return nil, nil
	}
	var selected []string
	for _, ln := range strings.Split(stdout.String(), "\n") {
		if strings.TrimSpace(ln) != "" {
			selected = append(selected, ln)
		}
	}
	return selected, nil
}

func fzfConfirm(message string) (bool, error) {
	if _, err := exec.LookPath("fzf"); err != nil {
		return false, fmt.Errorf("flake-up: fzf not found on PATH")
	}
	cmd := exec.Command("fzf",
		"--ansi",
		"--prompt=confirm> ",
		"--header="+message,
		"--height=40%",
		"--reverse",
		"--no-multi",
	)
	cmd.Stdin = strings.NewReader("yes\nno\n")
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return false, nil
	}
	return strings.TrimSpace(stdout.String()) == "yes", nil
}

func isInteractive() bool {
	for _, f := range []*os.File{os.Stdin, os.Stdout} {
		fi, err := f.Stat()
		if err != nil {
			return false
		}
		if (fi.Mode() & os.ModeCharDevice) == 0 {
			return false
		}
	}
	return true
}

func parseSelectedLabels(selected []string) []string {
	var labels []string
	for _, line := range selected {
		parts := strings.Split(line, "\t")
		if len(parts) >= 2 {
			labels = append(labels, strings.TrimSpace(parts[1]))
		}
	}
	return labels
}

// --- update ---

func runCmd(dir string, name string, args ...string) error {
	fmt.Println("+", name, strings.Join(args, " "))
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func updateOne(path, label string) error {
	fmt.Printf("── update %s ──\n", label)
	if !fileExists(filepath.Join(path, "flake.nix")) {
		return fmt.Errorf("not a flake root: %s", path)
	}
	if err := runCmd(path, "nix", "flake", "update", "--flake", path); err != nil {
		return err
	}
	return commitLock(path)
}

func commitLock(flake string) error {
	jjDir := filepath.Join(flake, ".jj")
	gitDir := filepath.Join(flake, ".git")
	hasJJ := dirExists(jjDir)
	hasGit := dirExists(gitDir)
	if !hasJJ && !hasGit {
		fmt.Printf("  (no vcs in %s; skip lock commit)\n", flake)
		return nil
	}

	if hasJJ {
		if _, err := exec.LookPath("jj"); err == nil {
			cmd := exec.Command("jj", "diff", "--name-only")
			cmd.Dir = flake
			out, err := cmd.Output()
			if err != nil {
				return fmt.Errorf("jj diff: %w", err)
			}
			names := map[string]bool{}
			for _, line := range strings.Split(string(out), "\n") {
				line = strings.TrimSpace(line)
				if line != "" {
					names[line] = true
				}
			}
			if !names["flake.lock"] {
				fmt.Println("  (flake.lock unchanged; nothing to commit)")
				return nil
			}
			if err := runCmd(flake, "jj", "commit", "-m", "flake: update lock", "flake.lock"); err != nil {
				return err
			}
			fmt.Println("  committed flake.lock")
			return nil
		}
	}

	cmd := exec.Command("git", "status", "--porcelain", "flake.lock")
	cmd.Dir = flake
	out, err := cmd.Output()
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(out)) == "" {
		fmt.Println("  (flake.lock unchanged; nothing to commit)")
		return nil
	}
	if err := runCmd(flake, "git", "add", "flake.lock"); err != nil {
		return err
	}
	if err := runCmd(flake, "git", "commit", "-m", "flake: update lock"); err != nil {
		return err
	}
	fmt.Println("  committed flake.lock")
	return nil
}

func dirExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}
