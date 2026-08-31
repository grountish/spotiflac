#!/usr/bin/env bash
# Build SpotiFLAC as a macOS .app bundle using Wails.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$SCRIPT_DIR"
APP_PATH="$REPO_ROOT/build/bin/SpotiFLAC.app"
WAILS_MODULE_VERSION="v2.12.0"
SLSKD_PUBLISH_DIR="$REPO_ROOT/build/slskd-publish"
SLSKD_RID="osx-$(uname -m | sed 's/^x86_64$/x64/')"

cleanup_incomplete_bundle() {
  if [[ -d "$APP_PATH" ]] && [[ ! -x "$APP_PATH/Contents/MacOS/SpotiFLAC" ]]; then
    rm -rf "$APP_PATH"
  fi
}

on_error() {
  cleanup_incomplete_bundle
}

trap on_error ERR

log() {
  printf '\n[%s] %s\n' "build-macos" "$1"
}

require_cmd() {
  local cmd="$1"
  if ! command -v "$cmd" >/dev/null 2>&1; then
    echo "Error: '$cmd' is not installed or not in PATH."
    exit 1
  fi
}

run_wails() {
  if command -v wails >/dev/null 2>&1; then
    wails "$@"
    return
  fi

  log "Global 'wails' CLI not found, using go run github.com/wailsapp/wails/v2/cmd/wails@${WAILS_MODULE_VERSION}"
  go run "github.com/wailsapp/wails/v2/cmd/wails@${WAILS_MODULE_VERSION}" "$@"
}

check_node_version() {
  local node_version major minor
  node_version="$(node -p "process.versions.node" 2>/dev/null || true)"
  if [[ -z "$node_version" ]]; then
    return
  fi

  IFS='.' read -r major minor _ <<<"$node_version"
  if (( major < 20 )) || { (( major == 20 )) && (( minor < 19 )); }; then
    echo "Error: detected Node.js $node_version. This project requires Node.js 20.19+ or 22.12+ for Vite/Wails builds."
    exit 1
  fi
}

check_architecture_alignment() {
  local host_arch go_arch node_arch
  host_arch="$(uname -m)"
  go_arch="$(go env GOHOSTARCH)"
  node_arch="$(node -p "process.arch")"

  if [[ "$host_arch" == "arm64" && "$go_arch" != "arm64" ]]; then
    cat <<EOF
Error: architecture mismatch detected.

  Host macOS architecture: $host_arch
  Go architecture:         $go_arch
  Node architecture:       $node_arch

Your current Go installation is x86_64/Rosetta, which causes Wails frontend packaging issues on Apple Silicon.
Install a native arm64 Go build, then rerun this script.
EOF
    exit 1
  fi
}

cd "$REPO_ROOT"

log "Checking required tools"
for cmd in go node pnpm; do
  require_cmd "$cmd"
done
check_node_version
check_architecture_alignment

cleanup_incomplete_bundle

log "Installing frontend dependencies"
pnpm --dir frontend install

log "Building frontend assets"
pnpm --dir frontend build

log "Building Wails macOS app"
run_wails build -clean

if [[ ! -d "$APP_PATH" ]]; then
  echo "Error: build finished but '$APP_PATH' was not created."
  exit 1
fi

bundle_slskd() {
  local csproj="$REPO_ROOT/frontend/third_party/slskd/src/slskd/slskd.csproj"
  local dest="$APP_PATH/Contents/Resources/third_party/slskd/bin"

  if [[ ! -f "$csproj" ]]; then
    echo "Error: vendored slskd project not found at '$csproj'."
    echo "Run: git submodule update --init --recursive"
    exit 1
  fi

  require_cmd dotnet

  log "Publishing slskd (self-contained $SLSKD_RID)"
  rm -rf "$SLSKD_PUBLISH_DIR"
  dotnet publish "$csproj" \
    -c Release \
    -r "$SLSKD_RID" \
    --self-contained true \
    -p:PublishTrimmed=false \
    -o "$SLSKD_PUBLISH_DIR" \
    --nologo -v quiet

  if [[ ! -x "$SLSKD_PUBLISH_DIR/slskd" ]]; then
    echo "Error: slskd publish did not produce an executable at '$SLSKD_PUBLISH_DIR/slskd'."
    exit 1
  fi

  log "Embedding slskd into app bundle"
  rm -rf "$APP_PATH/Contents/Resources/third_party"
  mkdir -p "$dest"
  cp -R "$SLSKD_PUBLISH_DIR/." "$dest/"

  # Adding files to Contents/ invalidates the signature Wails applied, so re-sign.
  log "Re-signing app bundle"
  codesign --force --deep --sign - "$APP_PATH"
  codesign --verify --deep "$APP_PATH"
}

bundle_slskd

log "Build complete"
echo "App bundle created at:"
echo "  $APP_PATH"
