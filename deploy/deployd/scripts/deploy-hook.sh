#!/bin/sh
# Client du webhook de deployd, utilisé par la CI et à la main. Dépend de openssl, od, awk et
# curl. Le HMAC est construit ici (RFC 2104) pour que le secret ne passe jamais dans les
# arguments d'un processus : seul le printf intégré au shell et des tubes le voient.
set -eu

usage() {
	cat >&2 <<'EOF'
usage : deploy-hook.sh prod <sha> <archive.tar.gz>
        deploy-hook.sh preview <pr> <sha> <archive.tar.gz>
        deploy-hook.sh delete-preview <pr>
        deploy-hook.sh service <tag>
Variables : DEPLOY_URL (ex. https://deploy.tzama.fr/hooks/deploy), HOOK_SECRET,
            DEPLOY_TIMEOUT (secondes par tentative, 600 par défaut),
            DEPLOY_RETRIES (tentatives si un envoi est déjà en cours, 20 par défaut).
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

# Clé XOR pad (54 = ipad, 92 = opad), complétée à 64 octets, en échappements octaux pour printf.
padded_key() {
	if [ "$(printf '%s' "$HOOK_SECRET" | wc -c)" -gt 64 ]; then
		printf '%s' "$HOOK_SECRET" | openssl dgst -sha256 -binary
	else
		printf '%s' "$HOOK_SECRET"
	fi | od -An -v -tu1 | awk -v pad="$1" '
		function bxor(a, b,   r, i) {
			r = 0
			for (i = 1; i < 256; i *= 2) if (int(a / i) % 2 != int(b / i) % 2) r += i
			return r
		}
		{ for (i = 1; i <= NF; i++) k[n++] = $i }
		END { for (i = 0; i < 64; i++) printf "\\%03o", bxor(i < n ? k[i] : 0, pad) }'
}
ipad=$(padded_key 54)
opad=$(padded_key 92)

# Les pads sont des séquences \ooo que seul le format de printf décode (%b exige \0ooo sous dash).
# shellcheck disable=SC2059
hmac_hex() {
	{
		printf "$opad"
		{
			printf "$ipad"
			printf '%s' "$1"
		} | openssl dgst -sha256 -binary
	} | openssl dgst -sha256 -r | cut -d' ' -f1
}

if [ -n "$archive" ]; then
	length=$(wc -c <"$archive" | tr -d ' ')
	body_sha=$(openssl dgst -sha256 -r "$archive" | cut -d' ' -f1)
else
	length=0
	body_sha=e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855
fi

out=$(mktemp)
trap 'rm -f "$out"' EXIT
tries=${DEPLOY_RETRIES:-20}
while :; do
	ts=$(date +%s)
	# Le « _ » protège le dernier saut de ligne, que $(...) supprimerait.
	canon=$(printf '%s\n%s\n%s\n%s\n%s\n%s\n%s\n%s\n_' "$ts" "$method" "$target" "$pr" "$sha" "$tag" "$length" "$body_sha")
	sig=$(hmac_hex "${canon%_}")
	case "$sig" in
	[0-9a-f]*) ;;
	*)
		echo "calcul de la signature impossible" >&2
		exit 1
		;;
	esac
	set -- -sS -o "$out" -w '%{http_code}' --max-time "${DEPLOY_TIMEOUT:-600}" -X "$method" \
		-H "X-Tzama-Timestamp: $ts" -H "X-Tzama-Target: $target" -H "X-Tzama-Signature: sha256=$sig" \
		-H "X-Tzama-Body-SHA256: $body_sha"
	[ -z "$pr" ] || set -- "$@" -H "X-Tzama-PR: $pr"
	[ -z "$sha" ] || set -- "$@" -H "X-Tzama-SHA: $sha"
	[ -z "$tag" ] || set -- "$@" -H "X-Tzama-Tag: $tag"
	# -T envoie le fichier en flux avec son Content-Length, sans le charger en mémoire.
	[ -z "$archive" ] || set -- "$@" -H "Content-Type: application/gzip" -T "$archive"
	code=$(curl "$@" "$DEPLOY_URL") || code=000
	cat "$out"
	echo
	case "$code" in
	2??) exit 0 ;;
	# 429 : un autre envoi est en cours. 409 : même seconde qu'une requête déjà acceptée pour
	# cette cible. Dans les deux cas une nouvelle signature, plus tard, passe.
	429 | 409)
		tries=$((tries - 1))
		[ "$tries" -gt 0 ] || break
		if [ "$code" = 429 ]; then sleep 30; else sleep 1; fi
		;;
	*) break ;;
	esac
done
echo "échec du déploiement (HTTP $code)" >&2
exit 1
