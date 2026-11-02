#!/bin/sh
# Installation et mise à jour de la VM (Debian 13). Idempotent : relancer après chaque modification de deploy/.
# Usage, en root sur la VM, depuis une copie de deploy/ : ./install.sh
set -eu

DIR=/srv/tzama
SRC=$(cd "$(dirname "$0")" && pwd)
[ "$(id -u)" -eq 0 ] || { echo "à lancer en root" >&2; exit 1; }

if ! command -v docker >/dev/null 2>&1; then
	apt-get update
	apt-get install -y ca-certificates curl
	install -m 0755 -d /etc/apt/keyrings
	curl -fsSL https://download.docker.com/linux/debian/gpg -o /etc/apt/keyrings/docker.asc
	. /etc/os-release
	printf 'Types: deb\nURIs: https://download.docker.com/linux/debian\nSuites: %s\nComponents: stable\nSigned-By: /etc/apt/keyrings/docker.asc\n' \
		"$VERSION_CODENAME" > /etc/apt/sources.list.d/docker.sources
	apt-get update
	apt-get install -y docker-ce docker-ce-cli containerd.io docker-compose-plugin
fi
systemctl enable --now docker

# Utilisateur sans shell propriétaire des sites : le service deployd tournera sous cet uid.
id tzama >/dev/null 2>&1 || useradd --system --home-dir "$DIR" --no-create-home --shell /usr/sbin/nologin tzama

install -d -m 0755 "$DIR"
install -d -m 0755 -o tzama -g tzama "$DIR/sites" "$DIR/sites/prod" "$DIR/sites/previews" "$DIR/state"
install -d -m 0755 "$DIR/sites/mockups"
[ -e "$DIR/sites/previews/index.html" ] || install -m 0644 -o tzama -g tzama "$SRC/www/previews-index.html" "$DIR/sites/previews/index.html"

install -m 0644 "$SRC/Caddyfile" "$DIR/Caddyfile"
install -m 0644 "$SRC/docker-compose.yml" "$DIR/compose.yml"
if [ ! -e "$DIR/.env" ]; then
	printf 'HAPROXY_IP=%s\nTZAMA_UID=%s\nTZAMA_GID=%s\n' "${HAPROXY_IP:?HAPROXY_IP requis au premier passage (adresse du HAProxy)}" "$(id -u tzama)" "$(id -g tzama)" > "$DIR/.env"
	chmod 0600 "$DIR/.env"
fi

if [ -e /etc/nftables.conf ] && ! cmp -s "$SRC/nftables.conf" /etc/nftables.conf; then
	cp /etc/nftables.conf "/etc/nftables.conf.bak-$(date +%Y%m%d-%H%M%S)"
fi
install -m 0755 "$SRC/nftables.conf" /etc/nftables.conf
nft -c -f /etc/nftables.conf
systemctl enable nftables
nft -f /etc/nftables.conf

cd "$DIR"
docker compose pull --quiet
docker compose up -d --remove-orphans
# Caddy relit sa config sans couper les connexions ; « up -d » ne le relance pas si seul le Caddyfile a changé.
docker compose exec -T caddy caddy validate --config /etc/caddy/Caddyfile --adapter caddyfile
docker compose restart caddy
echo "ok"
