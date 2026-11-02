#!/bin/sh
# Build Hugo + contrôles + archive, identiques en CI et en local.
# usage : build-site.sh <baseURL> <archive.tgz>   (depuis la racine du dépôt)
# TZAMA_STRICT=true : refuse les témoignages fictifs. MAX_IMAGE_KIB : poids max d'une image.
set -eu

base=${1:?baseURL manquante}
archive=${2:?archive manquante}
log=$(mktemp)
trap 'rm -f "$log"' EXIT

if [ "${TZAMA_STRICT:-}" = true ]; then
	HUGO_PARAMS_STRICT=true
	export HUGO_PARAMS_STRICT
fi

rm -rf site/public
status=0
hugo --source site --gc --minify --baseURL "$base" >"$log" 2>&1 || status=$?
cat "$log"
[ "$status" -eq 0 ] || exit "$status"

# Hugo sort en 0 malgré les avertissements : on les refuse.
if grep -E '^(WARN|ERROR)( |$)' "$log" >&2; then
	echo "::error::le build Hugo a émis des avertissements (voir ci-dessus)" >&2
	exit 1
fi

python3 -I .github/scripts/check-site.py site/public "$base" "${MAX_IMAGE_KIB:-500}"

tar czf "$archive" -C site/public .
