---
title: "{{ replace .File.ContentBaseName "-" " " | title }}"
start_date: {{ dateFormat "2006-01-02" .Date }} # sert au tri
dates: "" # texte affiché, ex. « Du lundi 26 au vendredi 30 octobre, de 9 h à 12 h »
ages: ""
# price: "" à renseigner seulement si le prix diffère du tarif des réglages
registration_url: "" # lien HelloAsso ou mailto:
---
