# Déploiement

Une VM Debian derrière un HAProxy qui termine le TLS. Sur la VM, Docker compose fait tourner Caddy, qui route selon le nom de domaine.

| Domaine | Contenu |
|---|---|
| `v2.tzama.fr` | site de prod (`sites/prod`) ; maquettes (`sites/mockups`) tant que la CI n'a pas déployé la prod |
| `test.tzama.fr` | une preview par PR : `/pr-<N>/` (`sites/previews/pr-<N>`) ; maquettes sous `/maquettes/` |
| `deploy.tzama.fr` | service de déploiement (à venir) |

Sur chaque site : `/u/*` relaie Umami Cloud ; `/api/*` ira au service de déploiement (formulaire de contact). Tout est en `noindex` hors `tzama.fr`.

## Installer ou mettre à jour

```sh
tar czf - -C deploy . | ssh root@<vm> 'rm -rf /root/tzama-deploy && mkdir /root/tzama-deploy && tar xzf - -C /root/tzama-deploy && HAPROXY_IP=<ip> /root/tzama-deploy/install.sh'
```

`install.sh` est idempotent. Il installe Docker si besoin, crée l'utilisateur `tzama`, l'arborescence `/srv/tzama`, installe `Caddyfile` et `compose.yml` puis relance Caddy. Le `.env` (adresse du HAProxy, uid de `tzama`) n'est créé qu'au premier passage ; `HAPROXY_IP` est donc obligatoire au premier passage, ensuite il se modifie dans `/srv/tzama/.env`.

## Sur la VM

```
/srv/tzama/
  compose.yml  Caddyfile  .env
  sites/prod/  sites/previews/  sites/mockups/   (servis en lecture seule par Caddy)
  state/                                         (état du service de déploiement)
```

- Port exposé : 80 (Caddy). La VM n'est pas exposée directement à Internet : seul le HAProxy la joint, d'où l'absence de pare-feu local. Caddy ne fait pas de TLS.
- Journaux d'accès en JSON sur la sortie de Caddy : `docker compose -f /srv/tzama/compose.yml logs caddy` ; `client_ip` = IP réelle du visiteur.
- Volumes Docker `caddy_data`, `caddy_config` : sans données utiles (pas de certificats ici).

## Sauvegarde

À sauvegarder : `/srv/tzama/.env` et, une fois le service de déploiement en place, `/srv/tzama/state/`. Le reste se recrée depuis ce dossier (`install.sh`) et depuis la CI (sites).
