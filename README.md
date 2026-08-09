# flake-up

Interactive survey of flake inputs under `~/src`, then **fzf** multi-select to
update locks. Same behavior for every repo.

Working personal tool — steal ideas freely.

## Usage

```fish
flake-up
```

1. Finds every `~/src/*` with `flake.nix` + `flake.lock`
2. Checks inputs (`nix flake metadata`, cached per URL)
3. **fzf** multi-select (`TAB` / `ctrl-a`)
4. Confirm (`yes` / `no`)
5. For each selection: `nix flake update` + commit `flake.lock` (jj or git)

Does **not** run `nh` or activate systems. Pure fzf — no CLI subcommands.
`--help` only.

## Develop

```fish
cd ~/src/flake-up
nix develop
go build -o flake-up .
./flake-up
```

Or: `nix build` / `nix run`.

## Install (this machine)

Home-manager in `~/src/nix-config` takes this flake as input and puts `flake-up`
on the interactive PATH. After changes:

```fish
cd ~/src/flake-up && go test ./...   # when tests exist
# then from nix-config: nh home switch  (rebuilds the wrapper package)
```

## Layout

| Path | Role |
|------|------|
| `main.go` | Program |
| `flake.nix` | Package + devShell (go, fzf, nix, jj) |

## Agents

Use tools directly:

```fish
nix flake update --flake ~/src/CV
```
