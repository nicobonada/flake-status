// flake-status: two-pane status dashboard for flakes under ~/src.
//
// Read-only — surfaces potential problems (stale inputs, exact-pin lag, VCS
// drift). Does not update locks or activate systems.
package main

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
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
		fmt.Fprintln(os.Stderr, "flake-status: interactive TUI only — no CLI subcommands.")
		fmt.Fprintln(os.Stderr, "  Run with no arguments.")
		return 2
	}

	if !isInteractive() {
		fmt.Fprintln(os.Stderr, "flake-status: needs an interactive TTY")
		return 1
	}

	root, err := srcRoot()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

	paths, err := discoverFlakes(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if len(paths) == 0 {
		fmt.Fprintf(os.Stderr, "no flakes under %s (need flake.nix + flake.lock)\n", root)
		return 1
	}

	// UI first; metadata + VCS checks stream in as they finish.
	cache := newMetaCache()
	statuses := pendingStatuses(paths, root)
	ch := surveyStream(paths, root, cache)

	m := newUI(statuses, ch)
	p := tea.NewProgram(m, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

const helpText = `flake-status — status dashboard for flakes under ~/src.

  flake-status          interactive UI
  flake-status --help   this text

Read-only: shows potential problems (does not update locks).

Left:  flake list — severity from the worst finding on the right
Right: focused flake (border title) — path, VCS, every root input

Keys:
  j/k or arrows   move
  /               filter
  pgup/pgdn       scroll detail
  q / ctrl+c      quit

Severity (left list and input rows):
  ✓  green  — ok
  !  amber  — exact "=" version pin has a newer release, and/or VCS drift
  ✗  red    — floating input behind tip or error
  …  pending

Exact pins (flake ref version segment uses "=" / "%3D") are compared to a
floating tip ("*") so amber means "an update exists", not "lock ≠ pin".

VCS uses local tools (jj preferred, else git) and fetches remotes with
limited concurrency. VCS and inputs stream independently. Shared flake
refs share one metadata fetch (singleflight). Each nix/git call has a
timeout so a stall cannot freeze the UI. Empty parked wip on main counts
as aligned with main.

To update locks (out of band), for example:

  nix flake update --flake ~/src/myflake
  nix flake update --flake ~/src/myflake nixpkgs

Does not run nh / OS switch.
`

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
