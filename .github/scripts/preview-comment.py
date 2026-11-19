#!/usr/bin/env python3
"""Commentaire unique sur la PR : erreurs de saisie de l'aperçu et lien vers l'aperçu.

usage : preview-comment.py [--dry-run]
Variables : REPORT_DIR (dossier écrit par build-site.sh avec PREVIEW_REPORT), BUILD_RESULT et
DEPLOY_RESULT (résultats des jobs), PREVIEW_URL, RUN_URL, PR_NUMBER, SHA, GITHUB_REPOSITORY, GH_TOKEN.
Le commentaire est créé au premier problème, puis mis à jour ; quand tout est corrigé, il le dit.
Sans dépendance : passe par la commande gh.
"""
import json
import os
import subprocess
import sys
from pathlib import Path
from urllib.parse import urljoin

MARKER = "<!-- tzama-preview-errors -->"
BOT = "github-actions[bot]"


def esc(text):
    """Texte saisi dans le CMS : rien ne doit être interprété comme HTML ni casser la liste."""
    text = " ".join(str(text).split())
    text = text.replace("&", "&amp;").replace("<", "&lt;").replace(">", "&gt;")
    for c in "\\`*_[]":
        text = text.replace(c, "\\" + c)
    return text


def block(text):
    return "```\n" + text.strip().replace("```", "'''") + "\n```"


def read(report, name):
    path = report / name
    return path.read_text(encoding="utf-8", errors="replace").strip() if path.is_file() else ""


def compose(env):
    report = Path(env.get("REPORT_DIR", "."))
    build = env.get("BUILD_RESULT", "")
    deploy = env.get("DEPLOY_RESULT", "")
    url = env.get("PREVIEW_URL", "")
    run = env.get("RUN_URL", "")
    sha = env.get("SHA", "")[:7]

    errors = []
    if (report / "errors.json").is_file():
        errors = json.loads((report / "errors.json").read_text(encoding="utf-8")).get("errors") or []
    warnings = read(report, "warnings.txt")
    checks = read(report, "checks.txt")
    failed = read(report, "build-failed.txt")
    build_failed = build != "success"
    deploy_failed = not build_failed and deploy != "success"
    problems = bool(errors or warnings or checks or build_failed or deploy_failed)

    out = [MARKER]
    if not problems:
        out.append("### Aperçu du site : tout est corrigé")
        out.append(f"Plus aucune erreur. L'aperçu est à jour : {url}")
        out.append("")
        out.append(f"<sub>Mis à jour automatiquement (version {sha}).</sub>")
        return False, "\n".join(out)

    if build_failed:
        out.append("### Aperçu du site : le site n'a pas pu être construit")
        out.append("Aucun aperçu n'a été publié pour cette version. La cause est dans le détail "
                   "ci-dessous ; si elle n'est pas claire, prévenez la personne qui s'occupe du site. "
                   f"[Journal du build]({run})")
        if failed:
            out += ["", "<details><summary>Détail technique</summary>", "", block(failed), "", "</details>"]
    else:
        n = len(errors)
        if n:
            title = "1 erreur à corriger" if n == 1 else f"{n} erreurs à corriger"
        elif warnings or checks:
            title = "contrôles techniques en échec"
        else:
            title = "mise en ligne échouée"
        out.append(f"### Aperçu du site : {title}")
        if deploy_failed:
            out.append(f"L'aperçu n'a pas pu être mis en ligne. [Journal]({run})")
        else:
            out.append(f"Aperçu en ligne : {url}")
            if n:
                out.append("Les erreurs y sont aussi affichées dans un bandeau rouge, en haut des pages.")
        if n:
            out.append("")
            for i, e in enumerate(errors, 1):
                out.append(f"{i}. **{esc(e.get('where', ''))}** : {esc(e.get('msg', ''))}.  ")
                out.append(f"   {esc(e.get('fix', ''))}  ")
                seen = ", ".join(f"[{esc(s.get('title', ''))}]({urljoin(url, s.get('url', ''))})"
                                 for s in e.get("seen") or [])
                file = str(e.get("file", "")).replace("`", "")
                out.append(f"   <sub>Fichier `{file}`" + (f" · visible sur {seen}" if seen else "") + "</sub>")
        tech = [("Avertissements du build", warnings), ("Contrôles du site", checks)]
        for label, text in tech:
            if text:
                out += ["", f"<details><summary>{label}</summary>", "", block(text), "", "</details>"]
    if build_failed or errors or warnings or checks:
        out.append("")
        out.append("Tant que ces points restent, la vérification « Site - checks » est en échec "
                   "et la modification ne peut pas être publiée.")
    out.append("")
    out.append(f"<sub>Mis à jour automatiquement à chaque modification (version {sha}).</sub>")
    return True, "\n".join(out)


def gh(*args):
    return subprocess.run(["gh", "api", *args], check=True, capture_output=True, text=True).stdout


def main():
    env = os.environ
    if env.get("BUILD_RESULT") == "cancelled" or env.get("DEPLOY_RESULT") == "cancelled":
        print("Run annulé : commentaire inchangé.")
        return
    problems, body = compose(env)
    if "--dry-run" in sys.argv:
        print(body)
        return

    repo, pr = env["GITHUB_REPOSITORY"], env["PR_NUMBER"]
    query = f'.[] | select(.user.login == "{BOT}" and (.body | startswith("{MARKER}"))) | .id'
    ids = gh("--paginate", f"repos/{repo}/issues/{pr}/comments", "--jq", query).split()
    if ids:
        gh("-X", "PATCH", f"repos/{repo}/issues/comments/{ids[0]}", "-f", f"body={body}")
        print(f"Commentaire {ids[0]} mis à jour.")
    elif problems:
        gh("-X", "POST", f"repos/{repo}/issues/{pr}/comments", "-f", f"body={body}")
        print("Commentaire créé.")
    else:
        print("Aucun problème et aucun commentaire existant : rien à faire.")


if __name__ == "__main__":
    main()
