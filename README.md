# flake-up

Two-pane TUI that **surveys** flake inputs under `~/src`: which locks are behind,
which pin-style inputs lag tip, and whether `wip` / `main` / origin line up.

Working personal tool — steal ideas freely. **MIT** licensed.

**Read-only:** this app does not update locks or activate systems. Use
`nix flake update` (or your own pin scripts) when you decide to bump something.

## Usage

```fish
flake-up
```

1. Discovers every `~/src/*` with `flake.nix` + `flake.lock`
2. Opens the UI immediately; checks stream in (footer spinner + `checking n/N`)
3. **Left:** flake list — mark is the worst finding on the right (red > amber > green)
4. **Right:** focused flake as the border title; path, VCS strip, every root input

### Keys

| Key | Action |
|-----|--------|
| `j` / `k` or arrows | Move in list |
| `/` | Filter list |
| `pgup` / `pgdn` | Scroll detail pane |
| `q` / `ctrl+c` | Quit |

### Marks

| Glyph | Color | Meaning |
|-------|--------|---------|
| `✓` | green | Ok |
| `!` | amber | Pin lag (e.g. Determinate) and/or VCS drift |
| `✗` | red | Stale input or error |
| `…` | muted | Still checking |

Left-list rollup: any red finding on the right → red; else any amber → amber; else green.

VCS compares `wip`, `main`, and `main@origin` when using [Jujutsu](https://jj-vcs.github.io/jj/); git repos show `main` vs `origin/main`. An empty parked `wip` on `main` counts as aligned. Remotes are fetched with limited concurrency.

### Updating locks (outside this tool)

```fish
nix flake update --flake ~/src/myflake
nix flake update --flake ~/src/myflake nixpkgs home-manager
```

Pin policies (for example Determinate non-prerelease bumps) stay in whatever
scripts or process you already use — flake-up only reports tip drift.

## Develop

```fish
cd ~/src/flake-up
nix develop
go run .
```

`nix build` / `nix run`.

## Layout

| Path | Role |
|------|------|
| `main.go` | Entry, help |
| `ui.go` | Two-pane Bubble Tea UI |
| `survey.go` | Discovery, lock vs tip, metadata cache |
| `vcs.go` | jj/git fetch + bookmark alignment |

## Agents

Prefer the same out-of-band update:

```fish
nix flake update --flake ~/src/example
```
