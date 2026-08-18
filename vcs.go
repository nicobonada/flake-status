package main

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// checkVCS fetches remotes (rate-limited) and compares unpublished work
// against main / origin. Prefer jj when .jj exists; otherwise git.
// No VCS → empty summary.
func checkVCS(repo string) vcsStatus {
	switch {
	case dirExists(filepath.Join(repo, ".jj")):
		return checkVCSJJ(repo)
	case dirExists(filepath.Join(repo, ".git")):
		return checkVCSGit(repo)
	default:
		return vcsStatus{Summary: "(no vcs)", Aligned: true}
	}
}

func checkVCSJJ(repo string) vcsStatus {
	// Fetch first so main@origin reflects the remote (bounded concurrency).
	vcsFetchSem <- struct{}{}
	_ = runQuiet(repo, vcsFetchTimeout, "jj", "git", "fetch")
	<-vcsFetchSem

	mainID, mainDesc, errMain := jjBookmark(repo, "main")
	originID, originDesc, errOrigin := jjBookmark(repo, "main@origin")
	topics, errTopics := jjLocalBookmarks(repo)
	wc, errWC := jjWorkingCopy(repo)

	if errMain != nil && errOrigin != nil && errTopics != nil && errWC != nil {
		return vcsStatus{Summary: "vcs unavailable", Err: errMain.Error()}
	}

	var main, origin *jjRef
	if errMain == nil && mainID != "" {
		main = &jjRef{name: "main", id: mainID, desc: mainDesc}
	}
	if errOrigin == nil && originID != "" {
		origin = &jjRef{name: "origin", id: originID, desc: originDesc}
	}
	if errWC != nil {
		wc = jjRef{}
	}
	return summarizeJJ(main, origin, topics, wc)
}

// jjRef is one bookmark or working-copy commit used in alignment.
type jjRef struct {
	name   string
	id     string
	desc   string
	empty  bool
	parent string
}

func isTrunkBookmark(name string) bool {
	return name == "main" || name == "master"
}

// parkedEmptyOn is an empty change whose only parent is main — leftover
// parked working copy or a just-created topic bookmark with no commits yet.
func parkedEmptyOn(r jjRef, mainID string) bool {
	return mainID != "" && r.empty && r.parent == mainID
}

// summarizeJJ compares unpublished work (named topic bookmarks and a dirty
// working copy) against main and origin. Empty parked work on main is aligned.
func summarizeJJ(main, origin *jjRef, topics []jjRef, wc jjRef) vcsStatus {
	mainID := ""
	if main != nil {
		mainID = main.id
	}

	var diverging []jjRef
	seen := map[string]bool{}
	for _, t := range topics {
		if t.name == "" || t.id == "" || isTrunkBookmark(t.name) {
			continue
		}
		if t.id == mainID || parkedEmptyOn(t, mainID) {
			continue
		}
		if seen[t.name] {
			continue
		}
		seen[t.name] = true
		diverging = append(diverging, t)
	}
	sort.Slice(diverging, func(i, j int) bool { return diverging[i].name < diverging[j].name })

	// Dirty @ with no topic bookmark on it is unpublished work of its own.
	// Empty @ is the parked working copy — topic bookmarks already cover
	// recorded units underneath.
	if wc.id != "" && !wc.empty {
		covered := wc.id == mainID
		for _, t := range topics {
			if t.id == wc.id {
				covered = true
				break
			}
		}
		if !covered {
			diverging = append([]jjRef{{name: "@", id: wc.id, desc: wc.desc}}, diverging...)
		}
	}

	hasMain := main != nil && main.id != ""
	hasOrigin := origin != nil && origin.id != ""
	mainEqOrigin := hasMain && hasOrigin && main.id == origin.id

	var summary string
	switch {
	case len(diverging) > 0 && hasMain && hasOrigin:
		names := make([]string, len(diverging))
		for i, t := range diverging {
			names[i] = t.name
		}
		work := strings.Join(names, ", ")
		if mainEqOrigin {
			summary = work + " ≠ main = origin"
		} else {
			summary = work + " ≠ main ≠ origin"
		}
	case hasMain && hasOrigin:
		if mainEqOrigin {
			summary = "main = origin"
		} else {
			summary = "main ≠ origin"
		}
	case len(diverging) > 0 && hasMain:
		names := make([]string, len(diverging))
		for i, t := range diverging {
			names[i] = t.name
		}
		summary = strings.Join(names, ", ") + " ≠ main (no origin)"
	case hasMain:
		summary = "main (no origin)"
	default:
		summary = "vcs (incomplete bookmarks)"
	}

	aligned := hasMain && hasOrigin && mainEqOrigin && len(diverging) == 0

	var lines []vcsLine
	if !aligned {
		for _, t := range diverging {
			lines = append(lines, makeVCSLine(t.name, t.id, t.desc))
		}
		if hasMain {
			lines = append(lines, makeVCSLine("main", main.id, main.desc))
		}
		if hasOrigin && !mainEqOrigin {
			lines = append(lines, makeVCSLine("origin", origin.id, origin.desc))
		}
	} else if hasMain {
		// One quiet line when fully aligned.
		lines = append(lines, makeVCSLine("main", main.id, main.desc))
	}

	return vcsStatus{
		Summary: summary,
		Lines:   lines,
		Aligned: aligned,
	}
}

func makeVCSLine(name, id, desc string) vcsLine {
	desc = strings.TrimSpace(desc)
	desc = strings.ReplaceAll(desc, "\n", " ")
	return vcsLine{
		Name: name,
		ID:   shortHash(id),
		Desc: desc,
	}
}

func jjBookmark(repo, name string) (id, desc string, err error) {
	// commit_id + tab + first line of description (tab is a real \t in the template).
	tmpl := "commit_id ++ \"\t\" ++ description.first_line() ++ \"\n\""
	out, err := runOut(repo, vcsCmdTimeout, "jj", "log", "-r", name, "--no-graph", "-n", "1", "-T", tmpl)
	if err != nil {
		return "", "", err
	}
	line := strings.TrimSpace(out)
	if line == "" {
		return "", "", fmt.Errorf("empty log for %s", name)
	}
	id, desc, _ = strings.Cut(line, "\t")
	id = strings.TrimSpace(id)
	desc = strings.TrimSpace(desc)
	if id == "" {
		return "", "", fmt.Errorf("no commit for %s", name)
	}
	return id, desc, nil
}

// jjLocalBookmarks lists local bookmarks (not remotes). Trunk names are
// included; summarizeJJ drops them.
func jjLocalBookmarks(repo string) ([]jjRef, error) {
	// name, remote, commit, empty, parents, description — tabs, one local bookmark per line.
	tmpl := `name ++ "\t" ++ coalesce(remote, "") ++ "\t" ++ ` +
		`if(normal_target, normal_target.commit_id(), "") ++ "\t" ++ ` +
		`if(normal_target, if(normal_target.empty(), "1", "0"), "") ++ "\t" ++ ` +
		`if(normal_target, normal_target.parents().map(|c| c.commit_id()).join(" "), "") ++ "\t" ++ ` +
		`if(normal_target, normal_target.description().first_line(), "") ++ "\n"`
	out, err := runOut(repo, vcsCmdTimeout, "jj", "bookmark", "list", "--sort", "name", "-T", tmpl)
	if err != nil {
		return nil, err
	}
	var refs []jjRef
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.SplitN(line, "\t", 6)
		if len(fields) < 3 {
			continue
		}
		name := strings.TrimSpace(fields[0])
		remote := ""
		if len(fields) > 1 {
			remote = strings.TrimSpace(fields[1])
		}
		if name == "" || remote != "" {
			continue
		}
		id := strings.TrimSpace(fields[2])
		if id == "" {
			continue
		}
		empty := len(fields) > 3 && fields[3] == "1"
		parent := ""
		if len(fields) > 4 {
			if ps := strings.Fields(fields[4]); len(ps) > 0 {
				parent = ps[0]
			}
		}
		desc := ""
		if len(fields) > 5 {
			desc = strings.TrimSpace(fields[5])
		}
		refs = append(refs, jjRef{name: name, id: id, desc: desc, empty: empty, parent: parent})
	}
	return refs, nil
}

func jjWorkingCopy(repo string) (jjRef, error) {
	tmpl := "commit_id ++ \"\t\" ++ if(empty, \"1\", \"0\") ++ \"\t\" ++ parents.map(|c| c.commit_id()).join(\" \") ++ \"\t\" ++ description.first_line() ++ \"\n\""
	out, err := runOut(repo, vcsCmdTimeout, "jj", "log", "-r", "@", "--no-graph", "-n", "1", "-T", tmpl)
	if err != nil {
		return jjRef{}, err
	}
	line := strings.TrimSpace(out)
	if line == "" {
		return jjRef{}, fmt.Errorf("empty log for @")
	}
	fields := strings.SplitN(line, "\t", 4)
	if len(fields) < 1 || strings.TrimSpace(fields[0]) == "" {
		return jjRef{}, fmt.Errorf("no commit for @")
	}
	r := jjRef{name: "@", id: strings.TrimSpace(fields[0])}
	if len(fields) > 1 {
		r.empty = fields[1] == "1"
	}
	if len(fields) > 2 {
		if ps := strings.Fields(fields[2]); len(ps) > 0 {
			r.parent = ps[0]
		}
	}
	if len(fields) > 3 {
		r.desc = strings.TrimSpace(fields[3])
	}
	return r, nil
}

func checkVCSGit(repo string) vcsStatus {
	vcsFetchSem <- struct{}{}
	_ = runQuiet(repo, vcsFetchTimeout, "git", "fetch", "--quiet")
	<-vcsFetchSem

	mainID, errMain := gitRev(repo, "main")
	if errMain != nil {
		mainID, errMain = gitRev(repo, "master")
	}
	originID, errOrigin := gitRev(repo, "origin/main")
	if errOrigin != nil {
		originID, errOrigin = gitRev(repo, "origin/master")
	}

	if errMain != nil {
		return vcsStatus{Summary: "git (no main)", Err: errMain.Error()}
	}
	mainDesc := gitSubject(repo, mainID)

	if errOrigin != nil {
		return vcsStatus{
			Summary: "main (no origin)",
			Lines:   []vcsLine{makeVCSLine("main", mainID, mainDesc)},
			Aligned: false,
		}
	}
	originDesc := gitSubject(repo, originID)
	if mainID == originID {
		return vcsStatus{
			Summary: "main = origin",
			Lines:   []vcsLine{makeVCSLine("main", mainID, mainDesc)},
			Aligned: true,
		}
	}
	return vcsStatus{
		Summary: "main ≠ origin",
		Lines: []vcsLine{
			makeVCSLine("main", mainID, mainDesc),
			makeVCSLine("origin", originID, originDesc),
		},
		Aligned: false,
	}
}

func gitRev(repo, ref string) (string, error) {
	out, err := runOut(repo, vcsCmdTimeout, "git", "rev-parse", "--verify", ref)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func gitSubject(repo, rev string) string {
	out, err := runOut(repo, vcsCmdTimeout, "git", "log", "-1", "--format=%s", rev)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

func runQuiet(dir string, timeout time.Duration, name string, args ...string) error {
	_, err := runOut(dir, timeout, name, args...)
	return err
}

func runOut(dir string, timeout time.Duration, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("timeout after %s", timeout)
		}
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("%s", msg)
	}
	return stdout.String(), nil
}
