#!/bin/sh
# Build Hugo + contrôles + archive, identiques en CI et en local.
# usage : build-site.sh <baseURL> <archive.tgz>   (depuis la racine du dépôt)
# TZAMA_STRICT=true : refuse les témoignages fictifs. MAX_IMAGE_KIB : poids max d'une image.
# PYTHON : interpréteur où sont installés tools/questionnaire/requirements.txt (défaut python3).
# PREVIEW_REPORT=<dossier> : aperçu de PR. Les erreurs de saisie, avertissements et contrôles
# ne bloquent plus : ils sont écrits dans ce dossier (errors.json, warnings.txt, checks.txt,
# build-failed.txt) pour le commentaire de la PR. Seul un échec du build lui-même reste fatal.
set -eu

base=${1:?baseURL manquante}
archive=${2:?archive manquante}
report=${PREVIEW_REPORT:-}
log=$(mktemp)
trap 'rm -f "$log"' EXIT

if [ "${TZAMA_STRICT:-}" = true ]; then
	HUGO_PARAMS_STRICT=true
	export HUGO_PARAMS_STRICT
fi
if [ -n "$report" ]; then
	mkdir -p "$report"
	rm -f "$report/errors.json" "$report/warnings.txt" "$report/checks.txt" "$report/build-failed.txt"
	HUGO_PARAMS_PREVIEW=true
	export HUGO_PARAMS_PREVIEW
fi

# Échec fatal ; en aperçu, la raison est aussi gardée pour le commentaire de la PR.
fail() {
	echo "::error::$1" >&2
	[ -z "$report" ] || printf '%s\n' "$1" >>"$report/build-failed.txt"
	exit 1
}

questionnaire=site/assets/documents/questionnaire-familles.pdf
rm -f "$questionnaire"
if ! "${PYTHON:-python3}" -I tools/questionnaire/generate.py -o "$questionnaire"; then
	fail "questionnaire : PDF non généré (voir site/data/questionnaire.yaml)"
fi

rm -rf site/public
status=0
hugo --source site --gc --minify --baseURL "$base" >"$log" 2>&1 || status=$?
cat "$log"
if [ "$status" -ne 0 ]; then
	[ -n "$report" ] || exit "$status"
	grep -E '^ERROR( |$)' "$log" >>"$report/build-failed.txt" || true
	fail "le build Hugo a échoué (code $status)"
fi

# Hugo sort en 0 malgré les avertissements : on les refuse (en aperçu, on les signale).
if [ -n "$report" ]; then
	grep -E '^(WARN|ERROR)( |$)' "$log" >"$report/warnings.txt" || rm -f "$report/warnings.txt"
elif grep -E '^(WARN|ERROR)( |$)' "$log" >&2; then
	echo "::error::le build Hugo a émis des avertissements (voir ci-dessus)" >&2
	exit 1
fi

# Hugo ne publie une ressource d'assets/ que si une page s'en sert : le PDF doit être dans le site.
if [ ! -f site/public/documents/questionnaire-familles.pdf ]; then
	fail "questionnaire-familles.pdf absent du site construit"
fi

if [ -n "$report" ]; then
	# Liste des erreurs pour la CI : pas dans le site publié.
	mv site/public/preview-errors.json "$report/errors.json"
	if ! python3 -I .github/scripts/check-site.py site/public "$base" "${MAX_IMAGE_KIB:-500}" >"$report/checks.txt" 2>&1; then
		cat "$report/checks.txt"
	else
		rm -f "$report/checks.txt"
	fi
else
	python3 -I .github/scripts/check-site.py site/public "$base" "${MAX_IMAGE_KIB:-500}"
fi

tar czf "$archive" -C site/public .
