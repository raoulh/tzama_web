# Questionnaire aux familles (PDF remplissable)

`generate.py` lit `site/data/questionnaire.yaml` (modifiable dans le CMS, collection « Questionnaire aux familles ») et produit `site/assets/documents/questionnaire-familles.pdf`, ignoré par git, que Hugo publie sous `/documents/questionnaire-familles.pdf`.
Le PDF est refait à chaque build par `.github/scripts/build-site.sh`, qui échoue si le YAML est invalide ; en local : `python3 -m venv ~/.venvs/tzama && ~/.venvs/tzama/bin/pip install -r tools/questionnaire/requirements.txt`, puis `PYTHON=~/.venvs/tzama/bin/python3 sh .github/scripts/build-site.sh <baseURL> <archive.tgz>`.
Avec `hugo server`, lancer d'abord `python3 tools/questionnaire/generate.py` (même venv) : sans le PDF, le lien de l'étape 1 de l'inscription est cassé.
