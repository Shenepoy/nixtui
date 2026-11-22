# nixtui

A terminal UI for managing a NixOS flake: search nixpkgs, add packages to your
config in one keypress, edit any file in the repo, and apply with
`nixos-rebuild`.

## Features

- **Search** the nixpkgs channel index (about 130k packages). It downloads once,
  caches a slim index locally, refreshes weekly, and searches as you type.
- **Install declaratively**: pick a package, choose which list in
  `modules/packages.nix` it belongs in, and it is added there. Nothing is
  installed behind your back.
- **Installed** tab: every entry in your package lists; remove one with `x`, or
  jump to it in the editor with `e`.
- **Files** tab and built-in editor for the whole flake repo, with a `nix`
  syntax check on save.
- **Changes** tab: git diff, dry build, commit, `nix flake update`, rollback.
- **Store** tab: size of every package in your config (alone and with its
  dependencies), how many copies and versions of it sit in the Nix store, how
  much of that is stale (no longer used by the running system), and what keeps
  an old copy alive (generations, profiles, `result` links). The totals line
  shows the whole store, the running system and the generation count.
- **Size before you install**: `ctrl+g` on a search result asks cache.nixos.org
  for its size, closure size and download size.
- **Formatter bundled**: `nixfmt` ships inside the Nix package. Format the open
  buffer (`ctrl+f`), a file (`f` in Files), or the whole repo (`F` in Changes).
  nixtui's own edits keep nixfmt's layout, so the two never fight.
- **Apply** from anywhere with `ctrl+a`: syncs the working copy to `/etc/nixos`
  and runs `nixos-rebuild switch --flake` (sudo prompts in your terminal).

## Usage

```
nixtui                              # open the TUI
nixtui search video editor
nixtui add ripgrep -c cliAndCore    # one line: add to a list
nixtui add ripgrep -c cliAndCore --apply   # ...and rebuild
nixtui remove ripgrep
nixtui list
nixtui store                        # package sizes, copies and versions in the store
nixtui info firefox                 # size before installing (binary cache)
nixtui fmt [--check] [files...]     # format .nix files
nixtui apply
```

Flags (or environment): `--repo` / `NIXTUI_REPO` (default `~/nixos-config`),
`--live` (default `/etc/nixos`), `--host` / `NIXTUI_HOST` (default `nixos`),
`--file` (default `<repo>/modules/packages.nix`), `--channel` (read from your
`flake.nix` by default).

## Expected config layout

`packages.nix` contains lists of this shape; nixtui edits them line by line and
leaves everything else untouched:

```nix
let
  cliAndCore = with pkgs; [
    vim git
    ripgrep
  ];
in { environment.systemPackages = cliAndCore ++ [ ]; }
```

Parenthesised entries (overrides, `.override { ... }`) are shown as
`(complex)` and are edited in the editor, not removed by `x`.

## Keys

| Key | Action |
| --- | --- |
| `tab` / `shift+tab` | next / previous tab |
| `enter` (Search) | add selected package |
| `x` (Installed) | remove entry |
| `e` / `enter` | open in editor |
| `ctrl+s` / `ctrl+f` / `esc` (editor) | save / format / close |
| `ctrl+g` (Search) | size of the selected package from the binary cache |
| `f` (Files) | format the selected file |
| `b` `c` `F` `u` `R` `r` (Changes) | dry build, commit, format all, flake update, rollback, refresh |
| `s` `enter` `r` (Store) | change sort, show every copy and its GC roots, rescan |
| `ctrl+a` | apply (rebuild) |

## Build

```
nix build            # or: CGO_ENABLED=0 go build
go test ./...
```
