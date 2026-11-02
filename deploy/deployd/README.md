# deployd

Service Go (bibliothèque standard seule) qui tourne à côté de Caddy sur la VM. Il :

- reçoit les déploiements de la CI (`POST`/`DELETE /hooks/deploy`) : prod, previews par PR, mise à jour de sa propre image ;
- relaie la connexion GitHub du CMS (`GET /auth`, `GET /callback`) ;
- reçoit le formulaire de contact (`POST /api/contact`) et l'envoie par SMTP.

Caddy ne lui transmet que `/hooks/deploy`, `/auth`, `/callback`, `/healthz` sur `deploy.tzama.fr` et `/api/*` sur `v2.tzama.fr` et `test.tzama.fr`. Il n'a aucun port publié.

## Arborescence

```
/srv/sites/                       (/srv/tzama/sites sur l'hôte)
  releases/<sha>/                 une release par commit, les 5 dernières sont gardées
  prod -> releases/<sha>          lien relatif, basculé atomiquement (rename)
  previews/pr-<N>/                une preview par PR
  previews/index.html             liste des previews (noindex), régénérée à chaque changement
  mockups/                        maquettes, non gérées par deployd
/state/                           (/srv/tzama/state sur l'hôte)
  releases.json  previews.json    historique des déploiements
  deployd.tag                     tag d'image demandé par la CI (lu par l'unité systemd)
```

Une archive est extraite dans un dossier temporaire, vérifiée (chemins, liens, nombre de fichiers, taille décompressée), puis renommée : un déploiement interrompu ne laisse jamais la prod ou une preview à moitié écrite. Au premier déploiement, un dossier `prod/` vide (créé par `install.sh`) est remplacé par le lien. Les déploiements sont sérialisés, y compris avec un `deployd rollback` lancé à côté.

## Protocole du webhook

`POST https://deploy.tzama.fr/hooks/deploy` (ou `DELETE` pour retirer une preview).

| En-tête | Valeur |
|---|---|
| `X-Tzama-Target` | `prod`, `preview` ou `service` |
| `X-Tzama-PR` | numéro de PR, `^[1-9][0-9]{0,8}$` |
| `X-Tzama-SHA` | sha du commit, 40 caractères hexadécimaux en minuscules |
| `X-Tzama-Tag` | tag d'image, `^[A-Za-z0-9._-]{1,128}$` |
| `X-Tzama-Timestamp` | heure Unix en secondes, à ±300 s de l'horloge de la VM |
| `X-Tzama-Signature` | `sha256=<hex>` |

Combinaisons acceptées (tout autre en-tête présent est refusé) :

| Méthode | Cible | En-têtes | Corps |
|---|---|---|---|
| `POST` | `prod` | SHA | tar.gz du dossier `public/` |
| `POST` | `preview` | PR, SHA | tar.gz du dossier `public/` |
| `POST` | `service` | Tag | vide |
| `DELETE` | `preview` | PR | vide |

L'archive contient le *contenu* de `public/` (`tar czf site.tar.gz -C public .`), 512 Mio au plus.

### Signature

HMAC-SHA256 avec `HOOK_SECRET`, sur la chaîne suivante suivie du corps brut, en hexadécimal minuscule :

```
<timestamp>\n<MÉTHODE>\n<cible>\n<pr>\n<sha>\n<tag>\n
```

Les champs absents sont des chaînes vides (leur `\n` reste). Exemple de référence (testé dans `internal/hook`) : secret `secret-de-test`, chaîne `1700000000\nPOST\npreview\n12\n` + 40 × `a` + `\n\n`, corps `corps` → `6f96e336e45115e20c15cee6ee6696eb133144a63065fd6cfa0f13b1e06efe2d`.

Une requête déjà reçue (même signature, dans la fenêtre) est refusée : la CI doit signer à nouveau pour réessayer.

### Réponses

JSON, `{"ok":true,…}` en cas de succès, sinon `{"ok":false,"error":"…"}`.

| Statut | Cas |
|---|---|
| 200 | effectué ; pour la prod, `previous` (sha remplacé) et `reused` (sha déjà présent, archive non réextraite) |
| 400 | en-tête invalide, combinaison non prévue, corps manquant ou inattendu, archive refusée |
| 401 | signature absente ou fausse, horodatage hors fenêtre |
| 404 | `DELETE` d'une preview inexistante |
| 409 | rejeu |
| 413 | corps ou archive décompressée trop gros |
| 503 | `HOOK_SECRET` absent |
| 500 | erreur d'écriture sur la VM (voir les journaux) |

### Client

`scripts/deploy-hook.sh` (POSIX sh, `openssl` et `curl`) signe et envoie ; c'est lui que la CI appelle.

```sh
export DEPLOY_URL=https://deploy.tzama.fr/hooks/deploy HOOK_SECRET=…
deploy-hook.sh prod <sha> site.tar.gz
deploy-hook.sh preview <pr> <sha> site.tar.gz
deploy-hook.sh delete-preview <pr>
deploy-hook.sh service <tag>
```

Le code de sortie est non nul si le service refuse. `DEPLOY_TIMEOUT` (secondes, défaut 1800) borne la requête. Le secret passe dans les arguments d'`openssl` : visible des autres processus du même runner, acceptable sur un runner GitHub éphémère, pas sur une machine partagée.

## Configuration

Variables lues au démarrage ; `deploy/deployd.env.example` est le modèle de `/srv/tzama/deployd.env`. Une valeur invalide arrête le service avec un message ; un secret absent ne désactive que la fonction concernée (503).

| Variable | Défaut | Rôle |
|---|---|---|
| `HOOK_SECRET` | | secret du webhook, 32 caractères au moins |
| `GITHUB_CLIENT_ID`, `GITHUB_CLIENT_SECRET` | | application OAuth GitHub du CMS |
| `OAUTH_REDIRECT_URL` | | callback envoyée à GitHub ; vide : celle de l'application |
| `ALLOWED_ORIGINS` | | origines autorisées pour le CMS et le formulaire, séparées par des virgules |
| `SMTP_HOST`, `SMTP_PORT` | , 587 | serveur d'envoi ; `SMTP_HOST` vide : formulaire en 503 |
| `SMTP_TLS` | `starttls` (`tls` si port 465) | `starttls`, `tls` ou `none` |
| `SMTP_TLS_SKIP_VERIFY` | `false` | voir ci-dessous |
| `SMTP_USERNAME`, `SMTP_PASSWORD` | | authentification SMTP (PLAIN), facultative |
| `SMTP_FROM` | | adresse d'envoi : enveloppe et en-tête `From` |
| `SMTP_FROM_NAME` | `Formulaire Tzama` | nom affiché dans `From` |
| `CONTACT_TO` | | destinataire (une adresse) |
| `CONTACT_OK_PATH`, `CONTACT_ERROR_PATH` | `/contact/merci/`, `/contact/` | redirections du formulaire HTML |
| `CONTACT_RATE_LIMIT`, `CONTACT_RATE_WINDOW` | 5, `10m` | envois par IP (/64 en IPv6) et par fenêtre glissante |
| `RELEASES_KEEP` | 5 | releases gardées (la prod l'est toujours) |
| `HOOK_MAX_BYTES` | 536870912 | taille maximale du corps |
| `HOOK_MAX_FILES`, `HOOK_MAX_UNPACKED_BYTES` | 50000, 2147483648 | limites de l'archive décompressée |
| `HOOK_UPLOAD_TIMEOUT` | `30m` | durée maximale d'un envoi |
| `SITES_DIR`, `STATE_DIR`, `PORT`, `TRUST_PROXY` | posées par `compose.yml` | |

`TRUST_PROXY=true` : l'IP du visiteur est lue dans `X-Real-IP`, posé par Caddy. Sans proxy devant, laisser à `false`.

### Expéditeur et `SMTP_TLS_SKIP_VERIFY`

Le serveur SMTP n'est autorisé (SPF/DKIM) que pour le domaine de `SMTP_FROM` : l'enveloppe et l'en-tête `From` sont toujours `SMTP_FROM`, l'adresse du parent n'est que dans `Reply-To`.

Le serveur SMTP utilisé est sur le réseau interne et présente un certificat auto-signé, sans nom alternatif (SAN) : la vérification standard échoue. `SMTP_TLS_SKIP_VERIFY=true` garde STARTTLS (le trafic reste chiffré) mais accepte n'importe quel certificat, donc sans protection contre une usurpation du serveur. Un avertissement est écrit au démarrage. À n'activer que pour un serveur joint par un réseau de confiance ; avec un certificat valide, laisser `false`.

## Formulaire de contact

`POST /api/contact`, `application/x-www-form-urlencoded` (formulaire HTML : redirection 303 vers `CONTACT_OK_PATH`, ou vers `CONTACT_ERROR_PATH?erreur=<code>&champs=…`) ou `application/json` (réponse JSON). L'origine doit être dans `ALLOWED_ORIGINS`.

Champs : `name`, `email`, `phone` (facultatif), `child_age`, `school_year`, `subject` (`visite`, `question`, `autre`), `message`. Un champ caché `website` rempli (robot) est accepté sans envoi.

| Statut | Code | Cas |
|---|---|---|
| 200 / 303 | | envoyé |
| 400 | `bad_request` | corps illisible ou trop gros (32 Kio) |
| 403 | `forbidden_origin` | origine absente ou non autorisée |
| 415 | `unsupported_media_type` | autre type de contenu |
| 422 | `validation` | champs en erreur (`errors` : champ → `required`, `too_long`, `invalid`, `invalid_email`, `invalid_phone`, `invalid_choice`) |
| 429 | `rate_limited` | trop d'envois, avec `Retry-After` |
| 502 | `send_failed` | refus du serveur SMTP |
| 503 | `unavailable` | SMTP non configuré |

## Exploitation

Depuis `/srv/tzama` :

```sh
docker compose exec deployd /deployd releases          # releases présentes, * = prod
docker compose exec deployd /deployd rollback          # prod -> release précédente
docker compose exec deployd /deployd rollback 1a2b3c4  # prod -> release donnée (préfixe de 7 caractères au moins)
docker compose logs deployd                            # journaux JSON
```

- **Purger une preview** : `deploy-hook.sh delete-preview <pr>` (la CI le fait à la fermeture de la PR).
- **Changer le secret** : `openssl rand -hex 32`, le mettre dans `deployd.env` et dans le secret GitHub Actions, puis `docker compose up -d deployd`.
- **Mettre à jour deployd** : la CI publie l'image `ghcr.io/raoulh/tzama-deployd:<tag>` puis appelle `deploy-hook.sh service <tag>`. deployd écrit `state/deployd.tag` ; l'unité `tzama-deployd-update.path` voit le changement et lance `tzama-deployd-update.sh`, qui met `DEPLOYD_TAG` dans `.env`, tire l'image et relance le conteneur. S'il n'est pas sain en une minute, l'ancien tag est remis. Suivi : `journalctl -u tzama-deployd-update.service`.
- **Arrêt** : `docker compose stop` laisse 2 minutes aux déploiements en cours.

## Développement

```sh
cd deploy/deployd
gofmt -l . && go vet ./... && go test -race -count=1 ./...
docker build -t tzama-deployd .
```

`script_test.go` exécute `scripts/deploy-hook.sh` contre le service (ignoré sans `sh`, `openssl` ou `curl`).
