#!/bin/sh
# Lancé par tzama-deployd-update.path quand deployd écrit state/deployd.tag (cible « service » du
# webhook) : passe deployd sur ce tag, et revient à l'ancien s'il ne devient pas sain.
set -eu

DIR=${TZAMA_DIR:-/srv/tzama}
cd "$DIR"
tag=$(tr -d ' \t\r\n' < state/deployd.tag)
# Le fichier est écrit par le conteneur : on n'en accepte qu'un nom de tag d'image.
if ! printf '%s' "$tag" | grep -Eq '^[A-Za-z0-9_][A-Za-z0-9._-]{0,127}$'; then
	echo "tag refusé : $tag" >&2
	exit 1
fi
old=$(sed -n 's/^DEPLOYD_TAG=//p' .env)
[ "$tag" != "$old" ] || exit 0

set_tag() {
	if grep -q '^DEPLOYD_TAG=' .env; then
		sed -i "s/^DEPLOYD_TAG=.*/DEPLOYD_TAG=$1/" .env
	else
		echo "DEPLOYD_TAG=$1" >> .env
	fi
}

healthy() {
	i=0
	while [ $i -lt 30 ]; do
		id=$(docker compose ps -q deployd)
		if [ -n "$id" ] && [ "$(docker inspect -f '{{.State.Health.Status}}' "$id")" = healthy ]; then
			return 0
		fi
		sleep 2
		i=$((i + 1))
	done
	return 1
}

echo "deployd : ${old:-latest} -> $tag"
set_tag "$tag"
if docker compose pull --quiet deployd && docker compose up -d deployd && healthy; then
	echo "deployd $tag en service"
	exit 0
fi
echo "deployd $tag en échec, retour à ${old:-latest}" >&2
set_tag "${old:-latest}"
docker compose up -d deployd || true
exit 1
