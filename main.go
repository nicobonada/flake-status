// flake-up: survey flake inputs under ~/src in a two-pane TUI.
//
// Read-only status dashboard — does not update locks or activate systems.
// For updates, use nix flake update (or project-specific pin scripts) outside.
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
		fmt.Fprintln(os.Stderr, "flake-up: interactive TUI only — no CLI subcommands.")
		fmt.Fprintln(os.Stderr, "  Run with no arguments.")
		return 2
	}

	if !isInteractive() {
		fmt.Fprintln(os.Stderr, "flake-up: needs an interactive TTY")
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

const helpText = `flake-up — two-pane survey of flakes under ~/src.

  flake-up          interactive UI
  flake-up --help   this text

Read-only status dashboard (does not update locks).

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
limited concurrency. Empty parked wip on main counts as aligned with main.

Updates are intentional and out of band, for example:

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
