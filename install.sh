#!/usr/bin/env sh
# Установщик rxmcp: находит последний релиз, кладёт бинарник в PATH, снимает карантин macOS.
# Запуск: curl -fsSL https://drxinfra.ru/dl/rxmcp/install.sh | sh
# Переменные: RXMCP_VERSION=v0.3.0, RXMCP_BIN=~/.local/bin, RXMCP_BASE=<зеркало с архивами>
set -eu

REPO=drxinfra/rxmcp
GH="https://github.com/$REPO"

say() { printf '%s\n' "$*"; }
die() { printf 'rxmcp: %s\n' "$*" >&2; exit 1; }

os=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$os" in
  linux|darwin) ;;
  *) die "эта ОС ($os) через скрипт не ставится: возьмите архив на $GH/releases" ;;
esac
case "$(uname -m)" in
  x86_64|amd64) arch=amd64 ;;
  arm64|aarch64) arch=arm64 ;;
  *) die "неизвестная архитектура $(uname -m): возьмите архив на $GH/releases" ;;
esac

command -v curl >/dev/null 2>&1 || die "нужен curl"
command -v tar  >/dev/null 2>&1 || die "нужен tar"

ver="${RXMCP_VERSION:-}"
if [ -z "$ver" ]; then
  # Последний релиз узнаём по редиректу /releases/latest: без токена и без лимитов API.
  loc=$(curl -fsSLI -o /dev/null -w '%{url_effective}' "$GH/releases/latest" 2>/dev/null || true)
  ver=$(printf '%s' "$loc" | sed -n 's|.*/tag/\(.*\)$|\1|p')
  [ -n "$ver" ] || die "не удалось узнать последнюю версию; задайте RXMCP_VERSION=vX.Y.Z"
fi

name="rxmcp-${ver}-${os}-${arch}"
base="${RXMCP_BASE:-$GH/releases/download/$ver}"
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT INT TERM

say "Скачиваю $name"
curl -fsSL "$base/$name.tar.gz" -o "$tmp/rxmcp.tar.gz" || die "не скачался $base/$name.tar.gz"
tar -xzf "$tmp/rxmcp.tar.gz" -C "$tmp"
bin=$(find "$tmp" -type f -name rxmcp -perm -u+x | head -1)
[ -n "$bin" ] || die "в архиве нет бинарника"

dir="${RXMCP_BIN:-}"
if [ -z "$dir" ]; then
  if [ -w /usr/local/bin ] 2>/dev/null; then dir=/usr/local/bin; else dir="$HOME/.local/bin"; fi
fi
mkdir -p "$dir"
target="$dir/rxmcp"
# Работающий сервер нельзя перезаписать на месте: сначала в сторону, потом переименование.
[ -e "$target" ] && mv -f "$target" "$target.old" 2>/dev/null || true
cp "$bin" "$target.new" && chmod 755 "$target.new" && mv -f "$target.new" "$target"
rm -f "$target.old"
[ "$os" = darwin ] && xattr -d com.apple.quarantine "$target" 2>/dev/null || true

say "Установлен: $target ($("$target" version))"
case ":$PATH:" in
  *":$dir:"*) say ""; say "Дальше: rxmcp setup" ;;
  *) say ""; say "Каталог $dir не в PATH. Добавьте строку в ~/.zshrc или ~/.bashrc:"
     say "  export PATH=\"$dir:\$PATH\""
     say "Дальше: $target setup" ;;
esac
