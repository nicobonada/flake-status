# flake-status

Two-pane **status dashboard** for flakes under `~/src`: which locks are behind,
which exact version pins have a newer release, and whether `wip` / `main` / origin
line up.

Working personal tool — steal ideas freely. **MIT** licensed.

**Read-only:** surfaces potential problems; it does not update locks or activate
systems. Use `nix flake update` (or your own pin scripts) when you decide to bump
something.

## Usage

```fish
flake-status
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
| `!` | amber | Exact `=` version pin has a newer release, and/or VCS drift |
| `✗` | red | Floating input behind tip or error |
| `…` | muted | Still checking |

Left-list rollup: any red finding on the right → red; else any amber → amber; else green.

**Exact pins:** if the flake ref pins a version with `=` (often URL-encoded as `%3D` in the path), metadata of that ref always resolves to the pin. flake-status compares the lock to a floating tip (`*` in place of the pin segment) and shows amber `!` when a newer release exists — host-agnostic, not tied to one vendor.

VCS compares `wip`, `main`, and `main@origin` when using [Jujutsu](https://jj-vcs.github.io/jj/); git repos show `main` vs `origin/main`. An empty parked `wip` on `main` counts as aligned. Remotes are fetched with limited concurrency.

### Updating locks (outside this tool)

```fish
nix flake update --flake ~/src/myflake
nix flake update --flake ~/src/myflake nixpkgs home-manager
```

How you bump an exact pin (and whether to allow prereleases) stays outside this
tool — flake-status only reports that a newer floating tip exists.

## Develop

```fish
cd ~/src/flake-status
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
