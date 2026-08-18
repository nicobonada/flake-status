package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestSummarizeJJAligned(t *testing.T) {
	main := &jjRef{name: "main", id: "aaa", desc: "on main"}
	origin := &jjRef{name: "origin", id: "aaa", desc: "on main"}

	got := summarizeJJ(main, origin, nil, jjRef{name: "@", id: "bbb", empty: true, parent: "aaa"})
	if !got.Aligned || got.Summary != "main = origin" {
		t.Fatalf("clean: aligned=%v summary=%q", got.Aligned, got.Summary)
	}

	parked := []jjRef{{
		name: "topic-bookmarks", id: "bbb", empty: true, parent: "aaa",
	}}
	got = summarizeJJ(main, origin, parked, jjRef{name: "@", id: "bbb", empty: true, parent: "aaa"})
	if !got.Aligned || got.Summary != "main = origin" {
		t.Fatalf("empty parked topic: aligned=%v summary=%q", got.Aligned, got.Summary)
	}

	onMain := []jjRef{{name: "leftover", id: "aaa", desc: "on main"}}
	got = summarizeJJ(main, origin, onMain, jjRef{name: "@", id: "aaa", empty: true, parent: "zzz"})
	if !got.Aligned || got.Summary != "main = origin" {
		t.Fatalf("topic on main: aligned=%v summary=%q", got.Aligned, got.Summary)
	}
}

func TestSummarizeJJTopicDrift(t *testing.T) {
	main := &jjRef{name: "main", id: "aaa", desc: "on main"}
	origin := &jjRef{name: "origin", id: "aaa", desc: "on main"}
	topics := []jjRef{{name: "niri-binds", id: "ccc", desc: "feat(niri): bind"}}

	got := summarizeJJ(main, origin, topics, jjRef{name: "@", id: "ddd", empty: true, parent: "ccc"})
	if got.Aligned {
		t.Fatal("expected drift for topic off main")
	}
	if got.Summary != "niri-binds ≠ main = origin" {
		t.Fatalf("summary=%q", got.Summary)
	}
	if len(got.Lines) != 2 || got.Lines[0].Name != "niri-binds" || got.Lines[1].Name != "main" {
		t.Fatalf("lines=%v", got.Lines)
	}
}

func TestSummarizeJJMultipleTopicsSorted(t *testing.T) {
	main := &jjRef{name: "main", id: "aaa"}
	origin := &jjRef{name: "origin", id: "aaa"}
	topics := []jjRef{
		{name: "write-access", id: "ccc"},
		{name: "niri-binds", id: "ddd"},
		{name: "main", id: "aaa"}, // trunk, ignored
	}
	got := summarizeJJ(main, origin, topics, jjRef{})
	if got.Summary != "niri-binds, write-access ≠ main = origin" {
		t.Fatalf("summary=%q", got.Summary)
	}
}

func TestSummarizeJJDirtyWorkingCopy(t *testing.T) {
	main := &jjRef{name: "main", id: "aaa"}
	origin := &jjRef{name: "origin", id: "aaa"}

	got := summarizeJJ(main, origin, nil, jjRef{name: "@", id: "bbb", empty: false, parent: "aaa", desc: "uncommitted"})
	if got.Aligned || got.Summary != "@ ≠ main = origin" {
		t.Fatalf("dirty @: aligned=%v summary=%q", got.Aligned, got.Summary)
	}

	// Topic already points at the dirty working copy — do not also add @.
	topics := []jjRef{{name: "niri-binds", id: "bbb", empty: false, parent: "aaa"}}
	got = summarizeJJ(main, origin, topics, jjRef{name: "@", id: "bbb", empty: false, parent: "aaa"})
	if got.Summary != "niri-binds ≠ main = origin" {
		t.Fatalf("covered @: summary=%q", got.Summary)
	}
}

func TestSummarizeJJMainOriginDrift(t *testing.T) {
	main := &jjRef{name: "main", id: "aaa", desc: "local"}
	origin := &jjRef{name: "origin", id: "zzz", desc: "remote"}

	got := summarizeJJ(main, origin, nil, jjRef{name: "@", id: "aaa", empty: true})
	if got.Aligned || got.Summary != "main ≠ origin" {
		t.Fatalf("main/origin: aligned=%v summary=%q", got.Aligned, got.Summary)
	}
	if len(got.Lines) != 2 || got.Lines[0].Name != "main" || got.Lines[1].Name != "origin" {
		t.Fatalf("lines=%v", got.Lines)
	}

	topics := []jjRef{{name: "niri-binds", id: "ccc"}}
	got = summarizeJJ(main, origin, topics, jjRef{})
	if got.Summary != "niri-binds ≠ main ≠ origin" {
		t.Fatalf("topic+origin: summary=%q", got.Summary)
	}
}

func TestSummarizeJJNoOrigin(t *testing.T) {
	main := &jjRef{name: "main", id: "aaa"}
	got := summarizeJJ(main, nil, nil, jjRef{})
	if got.Aligned || got.Summary != "main (no origin)" {
		t.Fatalf("no origin: aligned=%v summary=%q", got.Aligned, got.Summary)
	}

	topics := []jjRef{{name: "niri-binds", id: "ccc"}}
	got = summarizeJJ(main, nil, topics, jjRef{})
	if got.Summary != "niri-binds ≠ main (no origin)" {
		t.Fatalf("topic no origin: summary=%q", got.Summary)
	}
}

func TestParkedEmptyOn(t *testing.T) {
	if !parkedEmptyOn(jjRef{empty: true, parent: "aaa"}, "aaa") {
		t.Fatal("expected parked empty on main")
	}
	if parkedEmptyOn(jjRef{empty: false, parent: "aaa"}, "aaa") {
		t.Fatal("dirty child is not parked empty")
	}
	if parkedEmptyOn(jjRef{empty: true, parent: "bbb"}, "aaa") {
		t.Fatal("empty on another parent is not parked on main")
	}
}

func TestJJLocalBookmarksAndWorkingCopy(t *testing.T) {
	if _, err := exec.LookPath("jj"); err != nil {
		t.Skip("jj not on PATH")
	}
	dir := t.TempDir()
	jj := func(args ...string) {
		t.Helper()
		cmd := exec.Command("jj", append([]string{
			"--config=user.name=test",
			"--config=user.email=test@example.com",
		}, args...)...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("jj %v: %v\n%s", args, err, out)
		}
	}

	jj("git", "init", "--colocate")
	if err := os.WriteFile(filepath.Join(dir, "f"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	jj("commit", "-m", "first")
	jj("bookmark", "set", "main", "-r", "@-")
	jj("bookmark", "set", "niri-binds", "-r", "@")
	if err := os.WriteFile(filepath.Join(dir, "f"), []byte("a\nb\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	refs, err := jjLocalBookmarks(dir)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]jjRef{}
	for _, r := range refs {
		byName[r.name] = r
	}
	if _, ok := byName["main"]; !ok {
		t.Fatalf("missing main in %v", refs)
	}
	topic, ok := byName["niri-binds"]
	if !ok {
		t.Fatalf("missing niri-binds in %v", refs)
	}
	if topic.empty {
		t.Fatal("expected niri-binds to be dirty/non-empty after edit")
	}
	if isTrunkBookmark(topic.name) {
		t.Fatal("niri-binds should be a topic")
	}

	wc, err := jjWorkingCopy(dir)
	if err != nil {
		t.Fatal(err)
	}
	if wc.id != topic.id {
		t.Fatalf("working copy %s != topic %s", wc.id, topic.id)
	}
	if wc.empty {
		t.Fatal("expected dirty working copy")
	}

	main := byName["main"]
	got := summarizeJJ(&main, &jjRef{name: "origin", id: main.id}, refs, wc)
	if got.Aligned || got.Summary != "niri-binds ≠ main = origin" {
		t.Fatalf("aligned=%v summary=%q", got.Aligned, got.Summary)
	}
}
