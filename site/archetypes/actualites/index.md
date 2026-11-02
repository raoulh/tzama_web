---
title: "{{ replace .File.ContentBaseName "-" " " | title }}"
date: {{ .Date }}
# event_date: AAAA-MM-JJ, si l'actualité annonce un événement
summary: ""
pinned: false
# flyer: flyer.jpg, image à déposer dans ce dossier ; flyer_alt devient alors obligatoire
---
