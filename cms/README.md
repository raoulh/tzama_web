# CMS du site

Interface d'édition du contenu ([Sveltia CMS](https://sveltiacms.app/)) : `index.html` charge l'application, `config.yml` décrit les collections et leurs champs.

## Publication sur /cms/

`site/hugo.yaml` monte ce dossier dans `static/cms` (`module.mounts`) : Hugo le copie tel quel, sans ce README, dans `public/cms/`. Le CMS est donc servi sur `https://v2.tzama.fr/cms/` et sur chaque preview (`https://test.tzama.fr/pr-<N>/cms/`). Toute modification de `cms/` déclenche le build et la preview du site (filtre de `.github/workflows/site.yml`).

Le CMS est exclu des moteurs de recherche par la balise `robots` de `index.html` et par le `Disallow` de `robots.txt` (`site/layouts/robots.txt`). Caddy ajoute en plus `X-Robots-Tag` sur v2 et test tant que le site n'est pas sur tzama.fr.

## Version et chargement

`index.html` charge une version figée depuis unpkg, avec une empreinte SRI : le navigateur refuse le script si le fichier servi diffère de celui publié sur npm. Le script n'est pas copié dans le dépôt : Sveltia télécharge de toute façon ses modules secondaires, ses traductions et ses polices depuis unpkg et jsDelivr au fil de l'usage, et une copie locale de 2,6 Mo ne supprimerait pas cette dépendance.

Pour changer de version, remplacer le numéro dans l'URL puis recalculer l'empreinte :

```sh
curl -sL https://unpkg.com/@sveltia/cms@<version>/dist/sveltia-cms.js | openssl dgst -sha384 -binary | openssl base64 -A
```

et vérifier `config.yml` contre le schéma de la nouvelle version (`https://unpkg.com/@sveltia/cms@<version>/schema/sveltia-cms.json`, par exemple avec l'extension YAML de l'éditeur).

## Connexion

1. Ouvrir `/cms/`, choisir « Se connecter avec GitHub ».
2. Une fenêtre `deploy.tzama.fr/auth` s'ouvre : c'est le relais OAuth de deployd (`deploy/deployd/README.md`), qui échange le code GitHub contre un jeton et le rend à la page du CMS.
3. Il faut un compte GitHub avec les droits d'écriture sur le dépôt. La saisie d'un jeton personnel est désactivée (`auth_methods`).

Le relais n'accepte que les origines de `OAUTH_ALLOWED_ORIGINS` : pour se connecter depuis une preview, `https://test.tzama.fr` doit y figurer.

L'interface s'affiche dans la langue du navigateur (Sveltia n'a pas d'option pour l'imposer) ; on peut la changer dans les réglages du CMS, menu du compte.

## Relecture avant publication

`publish_mode: editorial_workflow` : chaque fiche enregistrée devient une branche `cms/…` et une pull request. La CI construit sa preview ; le lien « Aperçu » du CMS y mène (déploiement GitHub d'environnement `preview`). Le build échoue si un champ obligatoire manque (`site/layouts/_partials/validate.html`), avec le nom du fichier et du champ. Publier depuis le CMS fusionne la pull request.

## Images

Les actualités, l'équipe et les témoignages sont des page bundles : l'image est enregistrée dans le dossier de la fiche, à côté de `index.md`. Le CMS accepte JPEG, PNG et WebP jusqu'à 15 Mo, réduit l'image à 2000 px et la convertit en WebP avant l'envoi. Le texte alternatif devient obligatoire dès qu'une image est posée ; le CMS ne sait pas exprimer cette condition, c'est la validation du build qui la fait respecter.

L'insertion d'images dans le texte est désactivée : une image passe toujours par un champ dédié, avec son texte alternatif, et par `image.html`.

## Ajouter un champ

1. `config.yml` : ajouter le champ dans la collection, à sa place dans l'ordre de saisie, avec `label`, `hint`, `required` et `i18n` (`true` pour un texte à traduire, `duplicate` pour une valeur commune aux langues).
2. `site/layouts/_partials/validate.html` : déclarer le champ s'il est obligatoire ou typé (date, entier, booléen, liste de valeurs, image).
3. `site/archetypes/` : l'ajouter au modèle de la section, pour les fiches créées avec `hugo new`.
4. Le gabarit qui l'affiche, et le modèle de contenu de `site/README.md` si besoin.

Une liste de valeurs (`select`) doit reprendre exactement les valeurs permises par `validate.html`.
