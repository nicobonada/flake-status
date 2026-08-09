# flake-up

Two-pane TUI for flake inputs under `~/src`: pick flakes, inspect inputs, update
locks. Same behavior for every repo.

Working personal tool — steal ideas freely. **MIT** licensed (private for now;
fine to treat like public).

## Usage

```fish
flake-up
```

1. Surveys every `~/src/*` with `flake.nix` + `flake.lock`
2. Opens a **two-pane** UI (Bubble Tea):
   - **Left:** flake list (✓ / ✗ / !)
   - **Right:** input status for the focused flake
3. Mark flakes with **space**, confirm with **enter** / **u**
4. Runs `nix flake update` + commits `flake.lock` (jj or git)

Does **not** run `nh` or activate systems. Interactive only.

### Keys

| Key | Action |
|-----|--------|
| `j` / `k` or arrows | Move in list |
| `space` | Toggle mark for update |
| `a` | Select / clear all stale+error |
| `enter` or `u` | Confirm update of marked flakes |
| `/` | Filter list |
| `pgup` / `pgdn` | Scroll detail pane |
| `q` / `ctrl+c` | Quit |

Stale and error flakes start **marked**.

## Develop

```fish
cd ~/src/flake-up
nix develop
go run .
```

`nix build` / `nix run`. After Go changes for PATH via HM: `nh home switch ~/src/nix-config`.

## Layout

| Path | Role |
|------|------|
| `main.go` | Entry |
| `ui.go` | Two-pane Bubble Tea UI |
| `survey.go` | Lock check / metadata cache |
| `update.go` | `nix flake update` + commit |

## Agents

```fish
nix flake update --flake ~/src/CV
```
