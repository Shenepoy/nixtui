#!/bin/sh
# nixtui installer.
#
#   curl -fsSL https://raw.githubusercontent.com/Shenepoy/nixtui/main/install.sh | sh
#
# Downloads the release binary for this machine, verifies its SHA-256 against
# the release's sha256sums.txt, and installs it. Nothing else is touched.
#
# Environment:
#   NIXTUI_VERSION      version to install, e.g. 26.11.0 (default: latest release)
#   NIXTUI_INSTALL_DIR  where to put the binary (default: ~/.local/bin)
#   NIXTUI_BASE_URL     download base URL override (mirrors, testing)

set -eu

REPO="Shenepoy/nixtui"
VERSION="${NIXTUI_VERSION:-latest}"
INSTALL_DIR="${NIXTUI_INSTALL_DIR:-$HOME/.local/bin}"

say() { printf '%s\n' "$*"; }
die() { printf 'nixtui install: %s\n' "$*" >&2; exit 1; }

# --- platform -------------------------------------------------------------

[ "$(uname -s)" = "Linux" ] || die "nixtui manages NixOS flakes and only supports Linux (found $(uname -s))."

case "$(uname -m)" in
  x86_64 | amd64) ARCH="x86_64" ;;
  aarch64 | arm64) ARCH="aarch64" ;;
  *) die "unsupported architecture: $(uname -m) (supported: x86_64, aarch64)" ;;
esac

ASSET="nixtui-linux-$ARCH"

if [ -n "${NIXTUI_BASE_URL:-}" ]; then
  BASE="${NIXTUI_BASE_URL%/}"
elif [ "$VERSION" = "latest" ]; then
  BASE="https://github.com/$REPO/releases/latest/download"
else
  BASE="https://github.com/$REPO/releases/download/v${VERSION#v}"
fi

# --- download tools --------------------------------------------------------

if command -v curl >/dev/null 2>&1; then
  fetch() { curl -fsSL --retry 3 -o "$2" "$1"; }
elif command -v wget >/dev/null 2>&1; then
  fetch() { wget -q -O "$2" "$1"; }
else
  die "need curl or wget"
fi

if command -v sha256sum >/dev/null 2>&1; then
  sha256() { sha256sum "$1" | cut -d ' ' -f 1; }
elif command -v shasum >/dev/null 2>&1; then
  sha256() { shasum -a 256 "$1" | cut -d ' ' -f 1; }
else
  die "need sha256sum or shasum to verify the download"
fi

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT INT TERM

# --- fetch and verify ------------------------------------------------------

say "Downloading $ASSET ($VERSION)…"
fetch "$BASE/$ASSET" "$TMP/$ASSET" || die "could not download $BASE/$ASSET"
fetch "$BASE/sha256sums.txt" "$TMP/sha256sums.txt" || die "could not download $BASE/sha256sums.txt"

want="$(awk -v f="$ASSET" '$2 == f || $2 == "*" f { print $1; exit }' "$TMP/sha256sums.txt")"
[ -n "$want" ] || die "$ASSET is not listed in sha256sums.txt"
got="$(sha256 "$TMP/$ASSET")"
[ "$want" = "$got" ] || die "checksum mismatch for $ASSET (expected $want, got $got); not installing."
say "Checksum OK."

# --- install ---------------------------------------------------------------

mkdir -p "$INSTALL_DIR" || die "cannot create $INSTALL_DIR"
chmod 755 "$TMP/$ASSET"
mv "$TMP/$ASSET" "$INSTALL_DIR/nixtui" 2>/dev/null || cp "$TMP/$ASSET" "$INSTALL_DIR/nixtui" \
  || die "cannot write to $INSTALL_DIR (set NIXTUI_INSTALL_DIR to a writable directory)"

say "Installed: $INSTALL_DIR/nixtui ($("$INSTALL_DIR/nixtui" --version 2>/dev/null || echo unknown version))"

case ":$PATH:" in
  *":$INSTALL_DIR:"*) ;;
  *) say "Note: $INSTALL_DIR is not on your PATH. Add it, e.g.  export PATH=\"$INSTALL_DIR:\$PATH\"" ;;
esac

command -v nix >/dev/null 2>&1 || say "Note: nix was not found on PATH; nixtui needs it to search sizes, build and apply."
command -v nixfmt >/dev/null 2>&1 || say "Tip: install nixfmt to use the formatter (the Nix package bundles it)."

say "Run: nixtui"
