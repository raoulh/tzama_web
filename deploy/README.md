# Déploiement

Une VM Debian derrière un HAProxy qui termine le TLS. Sur la VM, Docker compose fait tourner Caddy, qui route selon le nom de domaine.

| Domaine | Contenu |
|---|---|
| `v2.tzama.fr` | site de prod (`sites/prod`) ; maquettes (`sites/mockups`) tant que la CI n'a pas déployé la prod |
| `test.tzama.fr` | une preview par PR : `/pr-<N>/` (`sites/previews/pr-<N>`) ; maquettes sous `/maquettes/` |
| `deploy.tzama.fr` | [deployd](deployd/README.md) : webhook de la CI, connexion du CMS ; le reste répond 404 |

Sur chaque site : `/u/*` relaie Umami Cloud ; `/api/*` va à deployd (formulaire de contact). Tout est en `noindex` hors `tzama.fr`.

## Installer ou mettre à jour

```sh
tar czf - -C deploy . | ssh root@<vm> 'rm -rf /root/tzama-deploy && mkdir /root/tzama-deploy && tar xzf - -C /root/tzama-deploy && HAPROXY_IP=<ip> /root/tzama-deploy/install.sh'
```

`install.sh` est idempotent. Il installe Docker si besoin, crée l'utilisateur `tzama`, l'arborescence `/srv/tzama`, installe `Caddyfile`, `compose.yml` et les unités systemd de mise à jour de deployd, puis relance les conteneurs. Le `.env` (adresse du HAProxy, uid de `tzama`, `DEPLOYD_TAG`) n'est créé qu'au premier passage ; `HAPROXY_IP` est donc obligatoire au premier passage, ensuite il se modifie dans `/srv/tzama/.env`.

`deployd.env` (secrets de deployd, 0600) est copié depuis `deployd.env.example` au premier passage seulement : le compléter sur la VM puis `docker compose -f /srv/tzama/compose.yml up -d deployd`. Ses variables sont décrites dans [deployd/README.md](deployd/README.md).

## Sur la VM

```
/srv/tzama/
  compose.yml  Caddyfile  .env  deployd.env
  sites/releases/  sites/prod -> releases/<sha>  sites/previews/  sites/mockups/
                                                 (écrits par deployd, servis en lecture seule par Caddy)
  state/                                         (état de deployd)
/usr/local/sbin/tzama-deployd-update.sh          (mise à jour de l'image de deployd)
/etc/systemd/system/tzama-deployd-update.{path,service}
```

- Port exposé : 80 (Caddy). La VM n'est pas exposée directement à Internet : seul le HAProxy la joint, d'où l'absence de pare-feu local. Caddy ne fait pas de TLS.
- Journaux d'accès en JSON sur la sortie de Caddy : `docker compose -f /srv/tzama/compose.yml logs caddy` ; `client_ip` = IP réelle du visiteur.
- Le HAProxy doit laisser passer les envois de la CI vers `deploy.tzama.fr` : corps jusqu'à 512 Mio, délais client/serveur d'au moins 30 minutes.
- deployd n'a pas de port publié ; il tourne sous l'uid de `tzama`, en lecture seule hors `sites/` et `state/`.
- Volumes Docker `caddy_data`, `caddy_config` : sans données utiles (pas de certificats ici).

## Sauvegarde

À sauvegarder : `/srv/tzama/.env`, `/srv/tzama/deployd.env` et `/srv/tzama/state/`. Le reste se recrée depuis ce dossier (`install.sh`) et depuis la CI (sites).
