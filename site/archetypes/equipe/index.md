---
title: "{{ replace .File.ContentBaseName "-" " " | title }}"
name: "{{ replace .File.ContentBaseName "-" " " | title }}"
role: ""
group: equipe # equipe ou ca
order: 99
# photo: photo.jpg, image à déposer dans ce dossier ; photo_alt devient alors obligatoire.
# Sans photo, le portrait générique s'affiche.
---
