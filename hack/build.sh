#!/usr/bin/env sh
# Сборка бинарников под все платформы в dist/.
set -eu
cd "$(dirname "$0")/.."
VERSION="${1:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"
rm -rf dist && mkdir -p dist
for target in linux/amd64 linux/arm64 windows/amd64 darwin/amd64 darwin/arm64; do
  os="${target%/*}"; arch="${target#*/}"
  out="dist/rxmcp-${VERSION}-${os}-${arch}"
  bin="rxmcp"; [ "$os" = windows ] && bin="rxmcp.exe"
  mkdir -p "$out"
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o "$out/$bin" .
  cp README.md LICENSE "$out/"
  if [ "$os" = windows ]; then (cd dist && zip -qr "rxmcp-${VERSION}-${os}-${arch}.zip" "$(basename "$out")"); else (cd dist && tar -czf "rxmcp-${VERSION}-${os}-${arch}.tar.gz" "$(basename "$out")"); fi
  rm -rf "$out"
done
(cd dist && shasum -a 256 * > SHA256SUMS)
ls -la dist
