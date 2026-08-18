package main

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestMetaCacheSingleflight(t *testing.T) {
	var calls atomic.Int32
	c := newMetaCache()
	c.fetch = func(url string) (map[string]any, error) {
		calls.Add(1)
		time.Sleep(40 * time.Millisecond)
		return map[string]any{"revision": url}, nil
	}

	const n = 8
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			meta, err := c.get("github:NixOS/nixpkgs")
			if err != nil {
				t.Errorf("get: %v", err)
				return
			}
			if meta["revision"] != "github:NixOS/nixpkgs" {
				t.Errorf("unexpected meta: %v", meta)
			}
		}()
	}
	wg.Wait()
	if got := calls.Load(); got != 1 {
		t.Fatalf("singleflight: fetch called %d times, want 1", got)
	}

	// After the flight, a later get is a cache hit (no extra fetch).
	if _, err := c.get("github:NixOS/nixpkgs"); err != nil {
		t.Fatal(err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("cache hit called fetch again (%d)", got)
	}
}

func TestMergeSurveyPartialThenComplete(t *testing.T) {
	base := flakeStatus{
		path:  "/src/demo",
		label: "demo",
		kind:  kindPending,
		vcs:   vcsStatus{Pending: true, Summary: "…"},
	}

	afterVCS := mergeSurvey(base, flakeStatus{
		hasVCS: true,
		vcs:    vcsStatus{Summary: "main = origin", Aligned: true},
	})
	if afterVCS.kind != kindPending {
		t.Fatalf("after VCS only: kind=%s want pending", afterVCS.kind)
	}
	if !afterVCS.vcsDone || afterVCS.inputsDone {
		t.Fatalf("after VCS: vcsDone=%v inputsDone=%v", afterVCS.vcsDone, afterVCS.inputsDone)
	}
	if afterVCS.vcs.Summary != "main = origin" {
		t.Fatalf("VCS summary lost: %q", afterVCS.vcs.Summary)
	}

	done := mergeSurvey(afterVCS, flakeStatus{
		hasInputs: true,
		inputs:    []inputStatus{{Name: "nixpkgs", State: inputOK}},
	})
	if done.kind != kindOK {
		t.Fatalf("after both: kind=%s want ok", done.kind)
	}
	if !done.vcsDone || !done.inputsDone {
		t.Fatal("expected both sides done")
	}
}

func TestMergeSurveyInputsFirst(t *testing.T) {
	base := flakeStatus{path: "/src/demo", kind: kindPending}
	afterIn := mergeSurvey(base, flakeStatus{
		hasInputs: true,
		inputs:    []inputStatus{{Name: "nixpkgs", State: inputStale, Detail: "stale"}},
	})
	if afterIn.kind != kindPending {
		t.Fatalf("inputs only should stay pending, got %s", afterIn.kind)
	}
	done := mergeSurvey(afterIn, flakeStatus{
		hasVCS: true,
		vcs:    vcsStatus{Summary: "main = origin", Aligned: true},
	})
	if done.kind != kindStale {
		t.Fatalf("stale input should roll up red, got %s", done.kind)
	}
}
