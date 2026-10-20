#!/usr/bin/env sh
# Проверка уязвимостей: собирает rxmcp под linux/amd64 и прогоняет trivy по бинарнику
# (stdlib Go и модули из go.sum). Падает на HIGH и CRITICAL, у которых уже есть исправление:
# такие закрываются подъёмом Go или модуля. Без исправления — печатаются, но не валят сборку.
# Аргумент: путь к готовому бинарнику или образу (image:tag), иначе собирается из исходников.
set -eu
cd "$(dirname "$0")/.."
command -v trivy >/dev/null || { echo "нет trivy: brew install trivy или см. .github/workflows/ci.yml" >&2; exit 2; }
target="${1:-}"
if [ -z "$target" ]; then
  tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
  CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o "$tmp/rxmcp" .
  target="$tmp/rxmcp"
fi
# Существующий файл сканируется как бинарник, остальное считается ссылкой на образ.
if [ -e "$target" ]; then mode=rootfs; else mode=image; fi
echo "trivy $mode $target"
# Полный список для журнала, затем гейт.
trivy "$mode" --quiet --scanners vuln --severity MEDIUM,HIGH,CRITICAL "$target"
trivy "$mode" --quiet --scanners vuln --severity HIGH,CRITICAL --ignore-unfixed --exit-code 1 "$target"
echo "уязвимостей HIGH/CRITICAL с исправлением нет"
