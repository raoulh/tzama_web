# Site Hugo

Version de Hugo : **0.162.1** (édition standard suffit, `hugo.yaml` exige au moins 0.162.0). La CI utilise la même version.

```sh
hugo server                                         # http://localhost:1313/
hugo --gc                                           # build de prod dans public/
hugo --gc --baseURL https://test.tzama.fr/pr-1/     # build d'une preview servie sous un sous-chemin
```

- Contenu : `content/fr/` ; libellés d'interface : `i18n/fr.yaml` ; coordonnées et réseaux : `data/settings.yaml`.
- Menus : `hugo.yaml`, par langue (`languages.fr.menus`).
- CSS : `assets/css/`, concaténé et minifié par Hugo Pipes ; `tokens.css` porte les couleurs, polices et dimensions.
- Saison (couleur d'accent) : posée côté navigateur selon la date du jour (script en tête de `head.html`) ; sans JS, printemps.
- Images : `partial "image.html" (dict "src" $res "alt" "…" "sizes" "…")`, qui produit le `srcset` WebP.
- Icônes : `partial "icon.html" (dict "id" "d-coeur")`, symboles de `assets/img/sprite.svg`.
- Modèle de contenu : actualités, équipe et CA, stages, postes, témoignages et FAQ sont des fichiers de `content/fr/<section>/` (un dossier par langue). Les modèles de départ sont dans `archetypes/` : `hugo new content equipe/prenom-nom`. Les tarifs ne sont que dans `data/settings.yaml`.
- Validation : `layouts/_partials/validate.html` fait échouer le build, en nommant le fichier et le champ, si un champ obligatoire manque ou est invalide.
- Mode strict : `HUGO_PARAMS_STRICT=true hugo --gc` refuse en plus les témoignages fictifs (`placeholder: true`). À poser pour le build de production seulement.
- Ajouter une langue : la déclarer dans `hugo.yaml` avec son `contentDir`, créer `content/<lang>/` et `i18n/<lang>.yaml`.
