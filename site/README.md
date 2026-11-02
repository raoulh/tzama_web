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
- Saison (couleur d'accent) : tirée de la date du build.
- Images : `partial "image.html" (dict "src" $res "alt" "…" "sizes" "…")`, qui produit le `srcset` WebP.
- Icônes : `partial "icon.html" (dict "id" "d-coeur")`, symboles de `assets/img/sprite.svg`.
- Ajouter une langue : la déclarer dans `hugo.yaml` avec son `contentDir`, créer `content/<lang>/` et `i18n/<lang>.yaml`.
