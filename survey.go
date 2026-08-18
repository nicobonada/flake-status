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
// Priority: red (stale/error) > amber (VCS drift) > green.
type kind string

const (
	kindPending kind = "pending" // survey still running
	kindOK      kind = "ok"      // green — no red or amber findings
	kindStale   kind = "stale"   // red — normal input behind or input error
	kindPin     kind = "pin"     // amber — VCS drift only
	kindError   kind = "error"   // red — flake-level failure (unreadable lock, etc.)
)

// inputState is the status of a single root flake input.
type inputState string

const (
	inputOK    inputState = "ok"
	inputStale inputState = "stale"
	inputError inputState = "error"
)

// inputStatus is one direct input of a flake, after comparing lock vs tip.
type inputStatus struct {
	Name   string
	State  inputState
	Pin    bool   // original flake ref is frozen to a SHA or "=" version
	Detail string // error text, or plain behind-tip line when Have/Tip are unset
	// Behind-tip labels for the detail pane (lock → tip). Dates are YYYY-MM-DD.
	// On a pin that matches the lock, Have/HaveDay are the freeze; Tip is empty.
	Have    string
	Tip     string
	HaveDay string
	TipDay  string
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

// isPinnedOriginal reports whether the flake *ref* is frozen (a git SHA or an
// exact "=" version). Locked.rev does not count — every lock has one.
func isPinnedOriginal(original map[string]any) bool {
	if original == nil {
		return false
	}
	if r, ok := original["rev"].(string); ok && isGitSHA(r) {
		return true
	}
	if r, ok := original["ref"].(string); ok && isGitSHA(r) {
		return true
	}
	if ref, err := flakeRef(original); err == nil && refLooksPinned(ref) {
		return true
	}
	if u, ok := original["url"].(string); ok && refLooksPinned(u) {
		return true
	}
	return false
}

func refLooksPinned(ref string) bool {
	return hasExactVersionOperator(ref) || shaFromRef(ref) != ""
}

func isGitSHA(s string) bool {
	n := len(s)
	if n < 7 || n > 40 {
		return false
	}
	for i := 0; i < n; i++ {
		c := s[i]
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f', c >= 'A' && c <= 'F':
		default:
			return false
		}
	}
	return true
}

// shaFromRef returns a git SHA from ?rev= / &rev= or a path segment.
func shaFromRef(ref string) string {
	if ref == "" {
		return ""
	}
	if _, query, ok := strings.Cut(ref, "?"); ok {
		query, _, _ = strings.Cut(query, "#")
		for _, kv := range strings.Split(query, "&") {
			k, v, found := strings.Cut(kv, "=")
			if found && k == "rev" && isGitSHA(v) {
				return v
			}
		}
	}
	path := ref
	if i := strings.IndexAny(path, "?#"); i >= 0 {
		path = path[:i]
	}
	for _, seg := range strings.Split(path, "/") {
		if isGitSHA(seg) {
			return seg
		}
	}
	return ""
}

// pinLabel is the short freeze shown next to "pin": version, or short SHA.
func pinLabel(original, locked map[string]any) string {
	if original != nil {
		if u, ok := original["url"].(string); ok && u != "" {
			if v := versionFromURL(u); v != "" {
				return v
			}
			if sha := shaFromRef(u); sha != "" {
				return shortHash(sha)
			}
		}
		if r, ok := original["rev"].(string); ok && isGitSHA(r) {
			return shortHash(r)
		}
		if r, ok := original["ref"].(string); ok && isGitSHA(r) {
			return shortHash(r)
		}
		if ref, err := flakeRef(original); err == nil {
			if v := versionFromURL(ref); v != "" {
				return v
			}
			if sha := shaFromRef(ref); sha != "" {
				return shortHash(sha)
			}
		}
	}
	if locked != nil {
		if r, ok := locked["rev"].(string); ok && r != "" {
			return shortHash(r)
		}
	}
	return ""
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
// any red → stale/error family; else VCS drift → amber; else ok.
func rollupKind(inputs []inputStatus, vcs vcsStatus) kind {
	hasRed, hasAmber := false, false
	for _, in := range inputs {
		switch in.State {
		case inputStale, inputError:
			hasRed = true
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
			pin:  isPinnedOriginal(peek.Original),
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
	// Pins resolve to themselves — do not rewrite to a floating tip.
	meta, err := cache.get(url)
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
		if pin {
			applyPinDisplay(&base, node.Original, node.Locked)
		}
		return base
	}

	behind := parseBehindDetail(node.Locked, meta, haveV, tipV)
	base.State = inputStale
	base.Have = behind.Have
	base.Tip = behind.Tip
	base.HaveDay = behind.HaveDay
	base.TipDay = behind.TipDay
	base.Detail = behind.String()
	return base
}

func applyPinDisplay(st *inputStatus, original, locked map[string]any) {
	st.Have = pinLabel(original, locked)
	if sec, ok := lastModifiedUnix(locked); ok {
		st.HaveDay = formatDay(sec)
	}
}

// behindDetail is lock vs tip for one input: rev/version plus optional dates.
type behindDetail struct {
	Have, Tip       string
	HaveDay, TipDay string
	Fallback        string
}

func parseBehindDetail(locked map[string]any, meta map[string]any, haveV, tipV string) behindDetail {
	haveLabel := shortRefLabel(locked, haveV)
	tipLabel := shortRefLabel(metaLocked(meta), tipV)
	d := behindDetail{}
	switch {
	case haveLabel != "" && tipLabel != "" && haveLabel != tipLabel:
		d.Have, d.Tip = haveLabel, tipLabel
	case haveV != tipV:
		d.Have, d.Tip = shortHash(haveV), shortHash(tipV)
	default:
		d.Fallback = "stale"
	}
	if sec, ok := lastModifiedUnix(locked); ok {
		d.HaveDay = formatDay(sec)
	}
	if sec, ok := lastModifiedUnix(meta); ok {
		d.TipDay = formatDay(sec)
	} else if sec, ok := lastModifiedUnix(metaLocked(meta)); ok {
		d.TipDay = formatDay(sec)
	}
	return d
}

func formatRevDate(rev, day string) string {
	if rev == "" {
		return ""
	}
	if day == "" {
		return rev
	}
	return rev + " (" + day + ")"
}

func (d behindDetail) String() string {
	if d.Have == "" && d.Tip == "" {
		if d.Fallback != "" {
			return d.Fallback
		}
		return "stale"
	}
	left := formatRevDate(d.Have, d.HaveDay)
	right := formatRevDate(d.Tip, d.TipDay)
	switch {
	case left != "" && right != "":
		return left + " -> " + right
	case left != "":
		return left
	default:
		return right
	}
}

func lastModifiedUnix(m map[string]any) (int64, bool) {
	if m == nil {
		return 0, false
	}
	v, ok := m["lastModified"]
	if !ok || v == nil {
		return 0, false
	}
	var n int64
	switch t := v.(type) {
	case float64:
		n = int64(t)
	case int64:
		n = t
	case int:
		n = int64(t)
	case json.Number:
		i, err := t.Int64()
		if err != nil {
			return 0, false
		}
		n = i
	default:
		return 0, false
	}
	if n <= 0 {
		return 0, false
	}
	return n, true
}

func formatDay(unix int64) string {
	return time.Unix(unix, 0).UTC().Format("2006-01-02")
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
