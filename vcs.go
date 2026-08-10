package main

import (
	"bytes"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// checkVCS fetches remotes (rate-limited) and compares wip / main / origin.
// Prefer jj when .jj exists; otherwise git. No VCS → empty summary.
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
	_ = runQuiet(repo, "jj", "git", "fetch")
	<-vcsFetchSem

	// Resolve commit ids for bookmarks we care about.
	mainID, mainDesc, errMain := jjBookmark(repo, "main")
	originID, originDesc, errOrigin := jjBookmark(repo, "main@origin")
	wipID, wipDesc, errWip := jjBookmark(repo, "wip")

	if errMain != nil && errOrigin != nil && errWip != nil {
		return vcsStatus{Summary: "vcs unavailable", Err: errMain.Error()}
	}

	// Empty parked wip: empty change whose parent is main → treat as aligned with main.
	if errWip == nil && errMain == nil && wipID != mainID {
		if empty, parent := jjChangeMeta(repo, wipID); empty && parent == mainID {
			wipID = mainID
			wipDesc = mainDesc
		}
	}

	hasWip := errWip == nil && wipID != ""
	hasMain := errMain == nil && mainID != ""
	hasOrigin := errOrigin == nil && originID != ""

	// Build equality summary pieces present in this repo.
	type ref struct {
		name, id, desc string
		ok             bool
	}
	wip := ref{"wip", wipID, wipDesc, hasWip}
	main := ref{"main", mainID, mainDesc, hasMain}
	origin := ref{"origin", originID, originDesc, hasOrigin}

	eq := func(a, b ref) bool {
		if !a.ok || !b.ok {
			return false
		}
		return a.id == b.id
	}

	var summary string
	switch {
	case hasWip && hasMain && hasOrigin:
		// wip ? main ? origin
		wm := "="
		if !eq(wip, main) {
			wm = "≠"
		}
		mo := "="
		if !eq(main, origin) {
			mo = "≠"
		}
		// Collapse wip = main = origin when all equal.
		if wm == "=" && mo == "=" {
			summary = "wip = main = origin"
		} else if wm == "=" {
			summary = "wip = main " + mo + " origin"
		} else if mo == "=" {
			summary = "wip " + wm + " main = origin"
		} else {
			summary = "wip ≠ main ≠ origin"
		}
	case hasMain && hasOrigin:
		if eq(main, origin) {
			summary = "main = origin"
		} else {
			summary = "main ≠ origin"
		}
	case hasMain:
		summary = "main (no origin)"
	default:
		summary = "vcs (incomplete bookmarks)"
	}

	aligned := hasMain && hasOrigin && eq(main, origin) && (!hasWip || eq(wip, main))

	var lines []string
	if !aligned {
		if hasWip && (!hasMain || !eq(wip, main)) {
			lines = append(lines, formatVCSLine("wip", wipID, wipDesc))
		}
		if hasMain {
			lines = append(lines, formatVCSLine("main", mainID, mainDesc))
		}
		if hasOrigin && (!hasMain || !eq(main, origin)) {
			lines = append(lines, formatVCSLine("origin", originID, originDesc))
		}
	} else if hasMain {
		// One quiet line when fully aligned.
		lines = append(lines, formatVCSLine("main", mainID, mainDesc))
	}

	return vcsStatus{
		Summary: summary,
		Lines:   lines,
		Aligned: aligned,
	}
}

func formatVCSLine(name, id, desc string) string {
	id = shortHash(id)
	desc = strings.TrimSpace(desc)
	if desc == "" {
		return fmt.Sprintf("%-6s %s", name, id)
	}
	// Keep description on one line, truncated later by the viewport if needed.
	desc = strings.ReplaceAll(desc, "\n", " ")
	return fmt.Sprintf("%-6s %s  %s", name, id, desc)
}

func jjBookmark(repo, name string) (id, desc string, err error) {
	// commit_id + tab + first line of description (tab is a real \t in the template).
	tmpl := "commit_id ++ \"\t\" ++ description.first_line() ++ \"\n\""
	out, err := runOut(repo, "jj", "log", "-r", name, "--no-graph", "-n", "1", "-T", tmpl)
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

// jjChangeMeta reports whether change is empty and its parent commit id (first parent).
func jjChangeMeta(repo, commitID string) (empty bool, parent string) {
	tmpl := "if(empty, \"1\", \"0\") ++ \"\t\" ++ parents.map(|c| c.commit_id()).join(\" \") ++ \"\n\""
	out, err := runOut(repo, "jj", "log", "-r", commitID, "--no-graph", "-n", "1", "-T", tmpl)
	if err != nil {
		return false, ""
	}
	line := strings.TrimSpace(out)
	flag, rest, _ := strings.Cut(line, "\t")
	empty = flag == "1"
	fields := strings.Fields(rest)
	if len(fields) > 0 {
		parent = fields[0]
	}
	return empty, parent
}

func checkVCSGit(repo string) vcsStatus {
	vcsFetchSem <- struct{}{}
	_ = runQuiet(repo, "git", "fetch", "--quiet")
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
			Lines:   []string{formatVCSLine("main", mainID, mainDesc)},
			Aligned: false,
		}
	}
	originDesc := gitSubject(repo, originID)
	if mainID == originID {
		return vcsStatus{
			Summary: "main = origin",
			Lines:   []string{formatVCSLine("main", mainID, mainDesc)},
			Aligned: true,
		}
	}
	return vcsStatus{
		Summary: "main ≠ origin",
		Lines: []string{
			formatVCSLine("main", mainID, mainDesc),
			formatVCSLine("origin", originID, originDesc),
		},
		Aligned: false,
	}
}

func gitRev(repo, ref string) (string, error) {
	out, err := runOut(repo, "git", "rev-parse", "--verify", ref)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func gitSubject(repo, rev string) string {
	out, err := runOut(repo, "git", "log", "-1", "--format=%s", rev)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

func runQuiet(dir string, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stdout = &bytes.Buffer{}
	cmd.Stderr = &stderr
	return cmd.Run()
}

func runOut(dir string, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("%s", msg)
	}
	return stdout.String(), nil
}
