#!/bin/sh
# Client du webhook de deployd, utilisé par la CI et à la main. Dépend de openssl et curl.
# Le secret passe dans les arguments d'openssl : à n'utiliser que sur une machine non partagée
# (runner éphémère de la CI, poste d'administration).
set -eu

usage() {
	cat >&2 <<'EOF'
usage : deploy-hook.sh prod <sha> <archive.tar.gz>
        deploy-hook.sh preview <pr> <sha> <archive.tar.gz>
        deploy-hook.sh delete-preview <pr>
        deploy-hook.sh service <tag>
Variables : DEPLOY_URL (ex. https://deploy.tzama.fr/hooks/deploy), HOOK_SECRET,
            DEPLOY_TIMEOUT (secondes, 1800 par défaut).
EOF
	exit 2
}

method=POST target='' pr='' sha='' tag='' archive=''
case "${1:-}" in
prod)
	[ $# -eq 3 ] || usage
	target=prod sha=$2 archive=$3
	;;
preview)
	[ $# -eq 4 ] || usage
	target=preview pr=$2 sha=$3 archive=$4
	;;
delete-preview)
	[ $# -eq 2 ] || usage
	method=DELETE target=preview pr=$2
	;;
service)
	[ $# -eq 2 ] || usage
	target=service tag=$2
	;;
*) usage ;;
esac

: "${DEPLOY_URL:?DEPLOY_URL manquant}"
: "${HOOK_SECRET:?HOOK_SECRET manquant}"
if [ -n "$archive" ] && [ ! -f "$archive" ]; then
	echo "archive introuvable : $archive" >&2
	exit 2
fi

ts=$(date +%s)
sig=$(
	{
		printf '%s\n%s\n%s\n%s\n%s\n%s\n' "$ts" "$method" "$target" "$pr" "$sha" "$tag"
		if [ -n "$archive" ]; then cat "$archive"; fi
	} | openssl dgst -sha256 -mac HMAC -macopt "key:$HOOK_SECRET" -r | cut -d' ' -f1
)
case "$sig" in
[0-9a-f]*) ;;
*)
	echo "calcul de la signature impossible" >&2
	exit 1
	;;
esac

set -- -sS --fail-with-body --max-time "${DEPLOY_TIMEOUT:-1800}" -X "$method" \
	-H "X-Tzama-Timestamp: $ts" -H "X-Tzama-Target: $target" -H "X-Tzama-Signature: sha256=$sig"
[ -z "$pr" ] || set -- "$@" -H "X-Tzama-PR: $pr"
[ -z "$sha" ] || set -- "$@" -H "X-Tzama-SHA: $sha"
[ -z "$tag" ] || set -- "$@" -H "X-Tzama-Tag: $tag"
# -T envoie le fichier en flux, sans le charger en mémoire comme --data-binary.
[ -z "$archive" ] || set -- "$@" -H "Content-Type: application/gzip" -T "$archive"
curl "$@" "$DEPLOY_URL"
echo
