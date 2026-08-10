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
3. **Left:** flake list (`✓` / `✗` / `!` pin lag / `…` pending)
4. **Right:** VCS strip + every root input with status

### Keys

| Key | Action |
|-----|--------|
| `j` / `k` or arrows | Move in list |
| `/` | Filter list |
| `pgup` / `pgdn` | Scroll detail pane |
| `q` / `ctrl+c` | Quit |

### Marks

| Glyph | Meaning |
|-------|---------|
| `✓` | Input at tip / flake ok |
| `✗` | Input (or flake) behind tip or error |
| `!` | Pin-style input behind tip (e.g. Determinate) — amber |
| `…` | Still checking |

VCS line compares `wip`, `main`, and `main@origin` when using [Jujutsu](https://jj-vcs.github.io/jj/); git repos show `main` vs `origin/main`. An empty parked `wip` on `main` counts as aligned. Remotes are fetched with limited concurrency.

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
