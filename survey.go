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

func dirExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

func labelFor(path, root string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return path
	}
	return rel
}

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
	details []string // e.g. "nixpkgs: stale"
	// notes: always shown (e.g. inputs flake-up will not bulk-update)
	notes []string
}

// Inputs that must not ride along with `nix flake update` of the whole flake.
// Determinate is versioned for non-prerelease minors via nix-config's
// scripts/update-determinate; FlakeHub ranges can resolve to GH prereleases.
func isBulkUpdateSkipped(name string, original map[string]any) bool {
	if name == "determinate" {
		return true
	}
	if original == nil {
		return false
	}
	t, _ := original["type"].(string)
	if t != "tarball" {
		return false
	}
	u, _ := original["url"].(string)
	// Match FlakeHub + pinned archive URLs for this project.
	return strings.Contains(u, "DeterminateSystems/determinate")
}

const skipBulkNote = "not bulk-updated with other inputs (use scripts/update-determinate in nix-config for non-prerelease minors)"

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
			stale, details, notes, err := checkFlake(path, cache)
			st := flakeStatus{path: path, label: label, notes: notes}
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

func checkFlake(flake string, cache *metaCache) (stale []string, details []string, notes []string, err error) {
	lockPath := filepath.Join(flake, "flake.lock")
	raw, err := os.ReadFile(lockPath)
	if err != nil {
		return nil, nil, nil, err
	}
	var lock struct {
		Root  string                     `json:"root"`
		Nodes map[string]json.RawMessage `json:"nodes"`
	}
	if err := json.Unmarshal(raw, &lock); err != nil {
		return nil, nil, nil, err
	}
	rootRaw, ok := lock.Nodes[lock.Root]
	if !ok {
		return nil, nil, nil, fmt.Errorf("root node %q missing", lock.Root)
	}
	var rootNode struct {
		Inputs map[string]json.RawMessage `json:"inputs"`
	}
	if err := json.Unmarshal(rootRaw, &rootNode); err != nil {
		return nil, nil, nil, err
	}

	type job struct {
		name    string
		node    json.RawMessage
		skipped bool
	}
	var jobs []job
	for name, ref := range rootNode.Inputs {
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
		var peek struct {
			Original map[string]any `json:"original"`
		}
		_ = json.Unmarshal(node, &peek)
		jobs = append(jobs, job{
			name:    name,
			node:    node,
			skipped: isBulkUpdateSkipped(name, peek.Original),
		})
	}

	type result struct {
		name    string
		reason  string
		skipped bool
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
			if j.skipped {
				resCh <- result{name: j.name, skipped: true}
				return
			}
			reason := checkInput(j.node, cache)
			resCh <- result{name: j.name, reason: reason}
		}(j)
	}
	go func() {
		jwg.Wait()
		close(resCh)
	}()

	for r := range resCh {
		if r.skipped {
			notes = append(notes, fmt.Sprintf("%s: %s", r.name, skipBulkNote))
			continue
		}
		if r.reason == "" {
			continue
		}
		stale = append(stale, r.name)
		details = append(details, fmt.Sprintf("%s: %s", r.name, r.reason))
	}
	sort.Strings(stale)
	sort.Strings(details)
	sort.Strings(notes)
	return stale, details, notes, nil
}

// rootUpdateInputs lists direct flake inputs that may be passed to
// `nix flake update <names…>` (excludes bulk-skip list).
func rootUpdateInputs(flake string) (update []string, skipped []string, err error) {
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
	for name, ref := range rootNode.Inputs {
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
		var peek struct {
			Original map[string]any `json:"original"`
		}
		_ = json.Unmarshal(node, &peek)
		if isBulkUpdateSkipped(name, peek.Original) {
			skipped = append(skipped, name)
			continue
		}
		update = append(update, name)
	}
	sort.Strings(update)
	sort.Strings(skipped)
	return update, skipped, nil
}

func checkInput(nodeRaw json.RawMessage, cache *metaCache) string {
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
