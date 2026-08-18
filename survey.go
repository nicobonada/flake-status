package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Timeouts so a stalled nix/git call cannot leave the TUI on spinners forever.
const (
	metadataTimeout = 45 * time.Second
	vcsFetchTimeout = 30 * time.Second
	vcsCmdTimeout   = 15 * time.Second
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

// kind is the rollup severity of a whole flake (left list mark).
// Priority: red (stale/error) > amber (pin lag or VCS drift) > green.
type kind string

const (
	kindPending kind = "pending" // survey still running
	kindOK      kind = "ok"      // green — no red or amber findings
	kindStale   kind = "stale"   // red — normal input behind or input error
	kindPin     kind = "pin"     // amber — pin lag and/or VCS drift only
	kindError   kind = "error"   // red — flake-level failure (unreadable lock, etc.)
)

// inputState is the status of a single root flake input.
type inputState string

const (
	inputOK    inputState = "ok"
	inputStale inputState = "stale"
	inputPin   inputState = "pin" // pin-style and behind tip
	inputError inputState = "error"
)

// inputStatus is one direct input of a flake, after comparing lock vs tip.
type inputStatus struct {
	Name   string
	State  inputState
	Pin    bool   // exact "=" version pin in the flake ref (see isExactVersionPin)
	Detail string // short reason: "stale", "3.21.9 → 3.22.1", error text
}

// vcsLine is one bookmark row under the VCS summary.
type vcsLine struct {
	Name string // topic name, @, main, origin
	ID   string // short commit id
	Desc string // first line of description
}

// vcsStatus summarizes unpublished work vs main / origin for the repo.
type vcsStatus struct {
	// Summary is one line, e.g. "main = origin" or "niri-binds ≠ main = origin".
	Summary string
	// Lines are optional detail rows (colored in the UI).
	Lines []vcsLine
	// Aligned is true when main matches origin and there is no diverging
	// topic bookmark or dirty working copy (empty parked work on main is fine).
	Aligned bool
	// Pending is true while fetch/check has not finished (unused when set on final result).
	Pending bool
	// Err is a short error if VCS could not be inspected.
	Err string
}

type flakeStatus struct {
	path     string
	label    string
	kind     kind
	inputs   []inputStatus
	vcs      vcsStatus
	flakeErr string // set when the lock/inputs survey failed

	// Message flags: which fields this update carries (surveyStream sends
	// VCS and inputs separately so the UI can paint the fast side first).
	hasVCS    bool
	hasInputs bool
	// Accumulated: both true means the left-list mark can be finalized.
	vcsDone    bool
	inputsDone bool
}

// needsAttention is true when the flake should count toward the footer total.
func (st flakeStatus) needsAttention() bool {
	switch st.kind {
	case kindStale, kindPin, kindError:
		return true
	default:
		return false
	}
}

// isExactVersionPin reports whether the input uses an exact version pin via the
// "=" operator in the flake ref (any host — commonly in versioned flake URLs).
// Metadata of that ref always resolves to the pin itself, so tip checks must
// query a floating rewrite (floatingTipRef) to see if a newer release exists.
func isExactVersionPin(original map[string]any) bool {
	if original == nil {
		return false
	}
	if ref, err := flakeRef(original); err == nil && hasExactVersionOperator(ref) {
		return true
	}
	// flakeRef may fail for odd types; still inspect raw URL fields.
	if u, ok := original["url"].(string); ok && hasExactVersionOperator(u) {
		return true
	}
	return false
}

// hasExactVersionOperator detects "=" / "%3D" exact-version segments in a ref.
func hasExactVersionOperator(ref string) bool {
	if ref == "" {
		return false
	}
	if strings.Contains(strings.ToLower(ref), "%3d") {
		return true
	}
	for _, seg := range strings.Split(ref, "/") {
		seg, _, _ = strings.Cut(seg, "?")
		if strings.HasPrefix(seg, "=") {
			return true
		}
	}
	return false
}

// floatingTipRef rewrites an exact "=" pin ref so metadata resolves to the
// latest matching release (version segment → "*").
func floatingTipRef(ref string) string {
	parts := strings.Split(ref, "/")
	for i := len(parts) - 1; i >= 0; i-- {
		seg := parts[i]
		base, query, hasQuery := strings.Cut(seg, "?")
		lower := strings.ToLower(base)
		if strings.HasPrefix(base, "=") || strings.HasPrefix(lower, "%3d") {
			if hasQuery {
				parts[i] = "*" + "?" + query
			} else {
				parts[i] = "*"
			}
			return strings.Join(parts, "/")
		}
	}
	return ref
}

type metaCache struct {
	mu       sync.Mutex
	data     map[string]metaResult
	inflight map[string]*metaCall
	// fetch is nix flake metadata; injectable in tests.
	fetch func(string) (map[string]any, error)
}

type metaResult struct {
	meta map[string]any
	err  error
}

// metaCall is one in-flight fetch that waiters block on (singleflight).
type metaCall struct {
	done chan struct{}
	res  metaResult
}

func newMetaCache() *metaCache {
	return &metaCache{
		data:     make(map[string]metaResult),
		inflight: make(map[string]*metaCall),
		fetch:    fetchMetadata,
	}
}

// get returns metadata for url. Concurrent callers for the same url share one
// in-flight fetch (singleflight): the first starts nix, the rest wait for that
// result instead of launching a duplicate. Hits after that are a map lookup.
func (c *metaCache) get(url string) (map[string]any, error) {
	c.mu.Lock()
	if hit, ok := c.data[url]; ok {
		c.mu.Unlock()
		return hit.meta, hit.err
	}
	if call, ok := c.inflight[url]; ok {
		c.mu.Unlock()
		<-call.done
		return call.res.meta, call.res.err
	}
	call := &metaCall{done: make(chan struct{})}
	c.inflight[url] = call
	fetch := c.fetch
	if fetch == nil {
		fetch = fetchMetadata
	}
	c.mu.Unlock()

	meta, err := fetch(url)
	call.res = metaResult{meta: meta, err: err}

	c.mu.Lock()
	c.data[url] = call.res
	delete(c.inflight, url)
	c.mu.Unlock()
	close(call.done)
	return meta, err
}

// mergeSurvey folds a partial update into the accumulated status. Left-list
// severity stays pending until both VCS and inputs have arrived.
func mergeSurvey(dst, src flakeStatus) flakeStatus {
	out := dst
	if src.hasVCS {
		out.vcs = src.vcs
		out.vcsDone = true
	}
	if src.hasInputs {
		out.inputs = src.inputs
		out.flakeErr = src.flakeErr
		out.inputsDone = true
	}
	if out.inputsDone && out.vcsDone {
		if out.flakeErr != "" {
			out.kind = kindError
		} else {
			out.kind = rollupKind(out.inputs, out.vcs)
		}
	} else {
		out.kind = kindPending
	}
	return out
}

// Global limit for jj/git fetch so we do not open dozens of network sessions at once.
var vcsFetchSem = make(chan struct{}, 3)

// pendingStatuses builds the initial list shown before checks finish.
func pendingStatuses(paths []string, root string) []flakeStatus {
	out := make([]flakeStatus, 0, len(paths))
	for _, p := range paths {
		out = append(out, flakeStatus{
			path:  p,
			label: labelFor(p, root),
			kind:  kindPending,
			vcs:   vcsStatus{Pending: true, Summary: "…"},
		})
	}
	return out
}

// surveyStream checks flakes concurrently. VCS and inputs are sent as separate
// updates so the UI can show fetch results while metadata is still running.
// The channel is closed when all work is done.
func surveyStream(paths []string, root string, cache *metaCache) <-chan flakeStatus {
	ch := make(chan flakeStatus, len(paths)*2)
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
			var inner sync.WaitGroup
			inner.Add(2)
			go func() {
				defer inner.Done()
				ch <- flakeStatus{
					path:   path,
					label:  label,
					kind:   kindPending,
					vcs:    checkVCS(path),
					hasVCS: true,
				}
			}()
			go func() {
				defer inner.Done()
				inputs, inErr := checkFlakeInputs(path, cache)
				st := flakeStatus{
					path:      path,
					label:     label,
					kind:      kindPending,
					inputs:    inputs,
					hasInputs: true,
				}
				if inErr != nil {
					st.flakeErr = inErr.Error()
				}
				ch <- st
			}()
			inner.Wait()
		}(p)
	}
	go func() {
		wg.Wait()
		close(ch)
	}()
	return ch
}

// rollupKind maps right-pane findings to a left-list severity:
// any red → stale/error family; else any amber → pin; else ok.
func rollupKind(inputs []inputStatus, vcs vcsStatus) kind {
	hasRed, hasAmber := false, false
	for _, in := range inputs {
		switch in.State {
		case inputStale, inputError:
			hasRed = true
		case inputPin:
			hasAmber = true
		}
	}
	// VCS: hard error is red; misalignment is amber.
	if vcs.Err != "" {
		hasRed = true
	} else if vcsDriftAmber(vcs) {
		hasAmber = true
	}
	if hasRed {
		return kindStale
	}
	if hasAmber {
		return kindPin
	}
	return kindOK
}

func vcsDriftAmber(vcs vcsStatus) bool {
	if vcs.Pending || vcs.Aligned {
		return false
	}
	switch vcs.Summary {
	case "", "(no vcs)":
		return false
	default:
		return true
	}
}

func checkFlakeInputs(flake string, cache *metaCache) ([]inputStatus, error) {
	lockPath := filepath.Join(flake, "flake.lock")
	raw, err := os.ReadFile(lockPath)
	if err != nil {
		return nil, err
	}
	var lock struct {
		Root  string                     `json:"root"`
		Nodes map[string]json.RawMessage `json:"nodes"`
	}
	if err := json.Unmarshal(raw, &lock); err != nil {
		return nil, err
	}
	rootRaw, ok := lock.Nodes[lock.Root]
	if !ok {
		return nil, fmt.Errorf("root node %q missing", lock.Root)
	}
	var rootNode struct {
		Inputs map[string]json.RawMessage `json:"inputs"`
	}
	if err := json.Unmarshal(rootRaw, &rootNode); err != nil {
		return nil, err
	}

	type job struct {
		name string
		node json.RawMessage
		pin  bool
	}
	var jobs []job
	for name, ref := range rootNode.Inputs {
		if len(ref) > 0 && ref[0] == '[' {
			continue // follows another input
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
			name: name,
			node: node,
			pin:  isExactVersionPin(peek.Original),
		})
	}

	resCh := make(chan inputStatus, len(jobs))
	var jwg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for _, j := range jobs {
		jwg.Add(1)
		go func(j job) {
			defer jwg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			resCh <- checkInputStatus(j.name, j.node, j.pin, cache)
		}(j)
	}
	go func() {
		jwg.Wait()
		close(resCh)
	}()

	out := make([]inputStatus, 0, len(jobs))
	for r := range resCh {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func checkInputStatus(name string, nodeRaw json.RawMessage, pin bool, cache *metaCache) inputStatus {
	base := inputStatus{Name: name, Pin: pin}

	var node struct {
		Original map[string]any `json:"original"`
		Locked   map[string]any `json:"locked"`
	}
	if err := json.Unmarshal(nodeRaw, &node); err != nil {
		base.State = inputError
		base.Detail = err.Error()
		return base
	}
	if node.Original == nil {
		base.State = inputError
		base.Detail = "missing original"
		return base
	}

	if t, _ := node.Original["type"].(string); t == "path" {
		p, _ := node.Original["path"].(string)
		if p == "" {
			base.State = inputError
			base.Detail = "path input missing path"
			return base
		}
		if _, err := os.Stat(p); err != nil {
			base.State = inputError
			base.Detail = "path missing: " + p
			return base
		}
		base.State = inputOK
		return base
	}

	url, err := flakeRef(node.Original)
	if err != nil {
		base.State = inputError
		base.Detail = err.Error()
		return base
	}
	// Exact "=" pins always resolve to themselves; ask floating tip for "is there an update?".
	metaURL := url
	if pin {
		metaURL = floatingTipRef(url)
	}
	meta, err := cache.get(metaURL)
	if err != nil {
		base.State = inputError
		base.Detail = err.Error()
		return base
	}
	haveK, haveV, err := lockedID(node.Locked)
	if err != nil {
		base.State = inputError
		base.Detail = err.Error()
		return base
	}
	tipK, tipV, err := tipID(meta)
	if err != nil {
		base.State = inputError
		base.Detail = err.Error()
		return base
	}

	atTip := haveK == tipK && haveV == tipV
	if !atTip && haveK != tipK {
		// Different id keys (rev vs narHash) — compare values if both non-empty.
		atTip = haveV == tipV
	}
	if atTip {
		base.State = inputOK
		return base
	}

	// Behind tip: exact pins → amber "!"; floating inputs → red "✗".
	detail := formatBehindDetail(node.Locked, meta, haveV, tipV)
	if pin {
		base.State = inputPin
		base.Detail = detail
		return base
	}
	base.State = inputStale
	base.Detail = detail
	return base
}

// formatBehindDetail prefers short version-like labels when present; else short hashes.
func formatBehindDetail(locked map[string]any, meta map[string]any, haveV, tipV string) string {
	haveLabel := shortRefLabel(locked, haveV)
	tipLabel := shortRefLabel(metaLocked(meta), tipV)
	if haveLabel != "" && tipLabel != "" && haveLabel != tipLabel {
		return haveLabel + " → " + tipLabel
	}
	if haveV != tipV {
		return shortHash(haveV) + " → " + shortHash(tipV)
	}
	return "stale"
}

func metaLocked(meta map[string]any) map[string]any {
	if locked, ok := meta["locked"].(map[string]any); ok {
		return locked
	}
	return meta
}

func shortRefLabel(m map[string]any, fallback string) string {
	if m == nil {
		return shortHash(fallback)
	}
	// Tarball / release-ish URLs often embed a version.
	if u, ok := m["url"].(string); ok && u != "" {
		if v := versionFromURL(u); v != "" {
			return v
		}
	}
	if ref, ok := m["ref"].(string); ok && ref != "" {
		return ref
	}
	return shortHash(fallback)
}

func versionFromURL(u string) string {
	// Prefer path segments: =3.21.9, %3D3.21.9, or …/3.21.9/… in pinned archives.
	for _, seg := range strings.Split(u, "/") {
		seg, _, _ = strings.Cut(seg, "?")
		seg, _, _ = strings.Cut(seg, "#")
		seg = strings.TrimSuffix(seg, ".tar.gz")
		seg = strings.TrimSuffix(seg, ".tgz")
		seg = strings.TrimSuffix(seg, ".tar.zst")
		lower := strings.ToLower(seg)
		switch {
		case strings.HasPrefix(seg, "="):
			seg = strings.TrimPrefix(seg, "=")
		case strings.HasPrefix(lower, "%3d"):
			seg = seg[len("%3D"):] // length same for %3d
			if len(seg) > 0 && (seg[0] == 'v' || seg[0] == 'V') {
				// keep optional v below
			}
		}
		seg = strings.TrimPrefix(seg, "v")
		seg = strings.TrimPrefix(seg, "V")
		if len(seg) > 0 && seg[0] >= '0' && seg[0] <= '9' && strings.Contains(seg, ".") {
			return seg
		}
	}
	return ""
}

func shortHash(s string) string {
	if s == "" {
		return "?"
	}
	if len(s) > 8 {
		return s[:8]
	}
	return s
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
	ctx, cancel := context.WithTimeout(context.Background(), metadataTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "nix", "flake", "metadata", url, "--json")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("timeout after %s", metadataTimeout)
		}
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
