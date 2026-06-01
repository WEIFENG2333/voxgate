#!/usr/bin/env sh
set -eu

REPO="${VOXGATE_REPO:-WEIFENG2333/voxgate}"
VERSION="${VOXGATE_VERSION:-latest}"
if [ -n "${VOXGATE_INSTALL_DIR:-}" ]; then
  INSTALL_DIR="$VOXGATE_INSTALL_DIR"
elif [ -n "${HOME:-}" ]; then
  INSTALL_DIR="$HOME/.local/bin"
else
  echo "error: HOME is not set; set VOXGATE_INSTALL_DIR to choose an install directory" >&2
  exit 1
fi

need() {
  command -v "$1" >/dev/null 2>&1 || {
    echo "error: missing required command: $1" >&2
    exit 1
  }
}

detect_os() {
  case "$(uname -s)" in
    Linux) echo linux ;;
    Darwin) echo darwin ;;
    MINGW*|MSYS*|CYGWIN*) echo windows ;;
    *) echo "error: unsupported OS: $(uname -s)" >&2; exit 1 ;;
  esac
}

detect_arch() {
  case "$(uname -m)" in
    x86_64|amd64) echo amd64 ;;
    arm64|aarch64) echo arm64 ;;
    *) echo "error: unsupported architecture: $(uname -m)" >&2; exit 1 ;;
  esac
}

latest_version() {
  version=""
  if command -v curl >/dev/null 2>&1; then
    version="$(curl -fsSL -H "User-Agent: voxgate-installer" "https://api.github.com/repos/$REPO/releases/latest" 2>/dev/null |
      sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -n 1 || true)"
    if [ -z "$version" ]; then
      url="$(curl -fsSL -o /dev/null -w '%{url_effective}' "https://github.com/$REPO/releases/latest" 2>/dev/null || true)"
      version="$(printf '%s\n' "$url" | sed -n 's#.*/releases/tag/\([^/?#]*\).*#\1#p' | head -n 1)"
    fi
  elif command -v wget >/dev/null 2>&1; then
    version="$(wget -qO- --header="User-Agent: voxgate-installer" "https://api.github.com/repos/$REPO/releases/latest" 2>/dev/null |
      sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -n 1 || true)"
    if [ -z "$version" ]; then
      version="$(wget -O /dev/null -S "https://github.com/$REPO/releases/latest" 2>&1 |
        sed -n 's#.*[Ll]ocation: .*/releases/tag/\([^[:space:]]*\).*#\1#p' | tail -n 1 || true)"
    fi
  else
    echo "error: curl or wget is required" >&2
    exit 1
  fi
  printf '%s\n' "$version"
}

download() {
  url="$1"
  out="$2"
  if command -v curl >/dev/null 2>&1; then
    curl -fL "$url" -o "$out"
  else
    wget -O "$out" "$url"
  fi
}

need uname
need sed
need mktemp
need head
need find
need grep

OS="$(detect_os)"
ARCH="$(detect_arch)"
if [ "$OS" = "windows" ] && [ "$ARCH" = "arm64" ]; then
  echo "error: Windows ARM64 release asset is not published yet; install a compatible voxgate.exe manually" >&2
  exit 1
fi
if [ "$VERSION" = "latest" ]; then
  VERSION="$(latest_version)"
fi
if [ -z "$VERSION" ]; then
  echo "error: could not determine latest voxgate release" >&2
  exit 1
fi

EXT="tar.gz"
if [ "$OS" = "windows" ]; then
  EXT="zip"
fi
ASSET="voxgate_${OS}_${ARCH}.${EXT}"
BASE_URL="https://github.com/$REPO/releases/download/$VERSION"
TARGET="$INSTALL_DIR/voxgate"
if [ "$OS" = "windows" ]; then
  TARGET="$INSTALL_DIR/voxgate.exe"
fi
if [ -x "$TARGET" ]; then
  CURRENT="$("$TARGET" version 2>/dev/null | sed -n 's/^voxgate //p' | head -n 1 || true)"
  if [ -n "$CURRENT" ] && { [ "$CURRENT" = "$VERSION" ] || [ "v$CURRENT" = "$VERSION" ]; }; then
    echo "voxgate $CURRENT is already installed at $TARGET"
    exit 0
  fi
fi
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT INT TERM

echo "Installing voxgate $VERSION for $OS/$ARCH"
download "$BASE_URL/$ASSET" "$TMP/$ASSET"
download "$BASE_URL/checksums.txt" "$TMP/checksums.txt" || true

if [ -s "$TMP/checksums.txt" ] && command -v sha256sum >/dev/null 2>&1; then
  (cd "$TMP" && grep " $ASSET\$" checksums.txt | sha256sum -c -)
fi

mkdir -p "$TMP/extract" "$INSTALL_DIR"
case "$EXT" in
  tar.gz)
    need tar
    tar -xzf "$TMP/$ASSET" -C "$TMP/extract"
    BIN="$(find "$TMP/extract" -type f -name voxgate | head -n 1)"
    ;;
  zip)
    need unzip
    unzip -q "$TMP/$ASSET" -d "$TMP/extract"
    BIN="$(find "$TMP/extract" -type f -name voxgate.exe | head -n 1)"
    ;;
esac

if [ -z "${BIN:-}" ]; then
  echo "error: voxgate binary not found in release archive" >&2
  exit 1
fi

cp "$BIN" "$TARGET"
chmod +x "$TARGET" 2>/dev/null || true
if [ "$OS" = "windows" ]; then
  find "$TMP/extract" -type f -name '*.dll' -exec cp {} "$INSTALL_DIR" \;
fi

echo "Installed: $TARGET"
if ! command -v ffmpeg >/dev/null 2>&1; then
  echo "warning: ffmpeg is not on PATH; install it before transcribing audio/video" >&2
fi
if [ "$OS" != "windows" ] && ! "$TARGET" doctor >/dev/null 2>&1; then
  echo "warning: voxgate doctor failed; install ffmpeg and libopus runtime packages if needed" >&2
fi
case ":$PATH:" in
  *":$INSTALL_DIR:"*) ;;
  *) echo "note: add $INSTALL_DIR to PATH if 'voxgate' is not found" ;;
esac
