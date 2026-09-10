#!/usr/bin/env sh
# Страж утечек: в публичной репе не должно быть адресов стендов, учёток и чужих имён.
# Запускается в CI и перед коммитом.
set -eu
cd "$(dirname "$0")/.."
pat="акелон|akelon|звездоч|zvezdoch|cbr\.|\.cbr|leibniz|лейбниц|password=['\"]?[a-z0-9]|1q2w3e|[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}"
if git ls-files -z | grep -zv '^hack/leakcheck.sh$' | xargs -0 grep -EnI -i "$pat" 2>/dev/null | grep -Ev '127\.0\.0\.1|0\.0\.0\.0|2020-12|jsonschema-go|x/text v|LICENSE:'; then
  echo "leakcheck: найдены подозрительные строки, см. выше" >&2
  exit 1
fi
echo "leakcheck: чисто"
