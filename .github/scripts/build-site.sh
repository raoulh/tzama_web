#!/bin/sh
# Build Hugo + contrôles + archive, identiques en CI et en local.
# usage : build-site.sh <baseURL> <archive.tgz>   (depuis la racine du dépôt)
# TZAMA_STRICT=true : refuse les témoignages fictifs. MAX_IMAGE_KIB : poids max d'une image.
# PYTHON : interpréteur où sont installés tools/questionnaire/requirements.txt (défaut python3).
set -eu

base=${1:?baseURL manquante}
archive=${2:?archive manquante}
log=$(mktemp)
trap 'rm -f "$log"' EXIT

if [ "${TZAMA_STRICT:-}" = true ]; then
	HUGO_PARAMS_STRICT=true
	export HUGO_PARAMS_STRICT
fi

questionnaire=site/assets/documents/questionnaire-familles.pdf
rm -f "$questionnaire"
if ! "${PYTHON:-python3}" -I tools/questionnaire/generate.py -o "$questionnaire"; then
	echo "::error::questionnaire : PDF non généré (voir site/data/questionnaire.yaml)" >&2
	exit 1
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

# Hugo ne publie une ressource d'assets/ que si une page s'en sert : le PDF doit être dans le site.
if [ ! -f site/public/documents/questionnaire-familles.pdf ]; then
	echo "::error::questionnaire-familles.pdf absent du site construit" >&2
	exit 1
fi

python3 -I .github/scripts/check-site.py site/public "$base" "${MAX_IMAGE_KIB:-500}"

tar czf "$archive" -C site/public .
