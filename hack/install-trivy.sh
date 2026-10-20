#!/usr/bin/env sh
# Ставит trivy фиксированной версии в ./bin с проверкой sha256 (для CI на Linux amd64).
# Не через aquasecurity/trivy-action: в марте 2026 его теги перезаписывали вредоносным кодом,
# а бинарник с зашитой контрольной суммой подменить незаметно нельзя.
# Обновление: новая версия и строка из trivy_<версия>_checksums.txt её релиза.
set -eu
VERSION=0.74.0
SHA256=2ae6fe3ee734b7fdf11335663e18c75ea12dccc76062f09f164a3b0f8be4371a
f="trivy_${VERSION}_Linux-64bit.tar.gz"
mkdir -p bin
curl -fsSLo "/tmp/$f" "https://github.com/aquasecurity/trivy/releases/download/v${VERSION}/$f"
echo "$SHA256  /tmp/$f" | sha256sum -c -
tar -xzf "/tmp/$f" -C bin trivy
bin/trivy --version | head -1
