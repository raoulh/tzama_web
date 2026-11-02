#!/bin/sh
# Écrit `changed=true|false` dans $GITHUB_OUTPUT : la PR ou le push touche-t-il un fichier
# correspondant à l'expression régulière étendue donnée ?
# Les workflows n'ont volontairement pas de filtre `paths:` : sinon leur job de checks ne
# serait jamais créé pour une PR hors périmètre, et la protection de main le attendrait sans fin.
# Variables : GH_TOKEN, GITHUB_EVENT_NAME, GITHUB_REPOSITORY, GITHUB_SHA, PR_NUMBER, BEFORE.
set -eu

pattern=${1:?expression manquante}
files=''

case "$GITHUB_EVENT_NAME" in
pull_request)
	files=$(gh api --paginate "repos/$GITHUB_REPOSITORY/pulls/$PR_NUMBER/files" -q '.[].filename') || files='?'
	;;
push)
	# Première poussée d'une branche (before nul) ou comparaison impossible : on construit.
	if [ -z "${BEFORE:-}" ] || [ "$BEFORE" = 0000000000000000000000000000000000000000 ]; then
		files='?'
	else
		files=$(gh api "repos/$GITHUB_REPOSITORY/compare/$BEFORE...$GITHUB_SHA" -q '.files[].filename') || files='?'
	fi
	;;
*) files='?' ;;
esac

if [ "$files" = '?' ] || printf '%s\n' "$files" | grep -Eq "$pattern"; then
	echo "changed=true" >>"$GITHUB_OUTPUT"
else
	echo "changed=false" >>"$GITHUB_OUTPUT"
fi
