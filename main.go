// flake-up: survey flake inputs under ~/src, two-pane TUI, update locks.
//
// Interactive TUI only (no subcommands). Same update for every repo:
//   nix flake update --flake <path>  then commit flake.lock if dirty.
// Does not activate systems (no nh switch).
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
		fmt.Fprintln(os.Stderr, "  Run with no arguments. Agents: nix flake update --flake <path>")
		return 2
	}

	if !isInteractive() {
		fmt.Fprintln(os.Stderr, "flake-up: needs an interactive TTY")
		return 1
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
	fmt.Printf("checking %d flake(s) under %s: %s …\n", len(paths), srcRoot, stringsJoin(labels, ", "))

	cache := newMetaCache()
	statuses := survey(paths, srcRoot, cache)

	m := newUI(statuses)
	p := tea.NewProgram(m, tea.WithAltScreen())
	final, err := p.Run()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	um, ok := final.(uiModel)
	if !ok {
		return 1
	}
	if um.cancelled || len(um.toUpdate) == 0 {
		if um.cancelled {
			fmt.Println("cancelled")
		} else {
			fmt.Println("nothing selected")
		}
		return 0
	}

	for _, st := range um.toUpdate {
		if err := updateOne(st.path, st.label); err != nil {
			fmt.Fprintf(os.Stderr, "update failed for %s: %v\n", st.label, err)
			return 1
		}
	}
	fmt.Println("done")
	return 0
}

const helpText = `flake-up — two-pane survey of ~/src flakes, update locks.

  flake-up          interactive UI
  flake-up --help   this text

Left: flake list (space toggle for update). Right: input status for focus.
  j/k or arrows  move
  space          toggle selected for update
  a              toggle all stale/error
  enter / u      update selected (confirm)
  q / ctrl+c     quit

Same action for every selected repo:
  nix flake update --flake <path>
  commit flake.lock if changed (jj or git)

Does not run nh / OS switch.
`

func stringsJoin(ss []string, sep string) string {
	if len(ss) == 0 {
		return ""
	}
	out := ss[0]
	for i := 1; i < len(ss); i++ {
		out += sep + ss[i]
	}
	return out
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
