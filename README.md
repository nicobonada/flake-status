# flake-up

Interactive survey of flake inputs under `~/src`, then a **Huh** multi-select to
update locks. Same behavior for every repo — simple lazygit-adjacent pick list,
not a full multi-panel TUI.

Working personal tool — steal ideas freely.

## Usage

```fish
flake-up
```

1. Finds every `~/src/*` with `flake.nix` + `flake.lock`
2. Checks inputs (`nix flake metadata`, cached per URL)
3. **Huh** multi-select (stale pre-selected; filterable)
4. Confirm update
5. For each selection: `nix flake update` + commit `flake.lock` (jj or git)

Does **not** run `nh` or activate systems. Interactive only — no CLI subcommands.
`--help` prints a short blurb.

Keys (Huh defaults): space toggle, enter submit, `/` filter when enabled, ctrl+c cancel.

## Develop

```fish
cd ~/src/flake-up
nix develop
go run .
# or
go build -o flake-up . && ./flake-up
```

Package: `nix build` / `nix run`.

After Go edits that should hit PATH via home-manager:

```fish
nh home switch ~/src/nix-config
```

## Install

`~/src/nix-config` takes this flake as a path input and puts `flake-up` on the
interactive PATH (`home/programs/flake-up.nix`).

## Layout

| Path | Role |
|------|------|
| `main.go` | Program (survey + Huh + update) |
| `flake.nix` | Package + devShell |

## Agents

```fish
nix flake update --flake ~/src/CV
```
