#!/usr/bin/env python3
"""Contrôles du site construit : liens internes (ancres en simple avertissement) et poids des images.

usage : check-site.py <dossier public> <baseURL> [poids max d'une image en Kio]
Sans dépendance (bibliothèque standard). Les liens externes ne sont pas vérifiés.
"""
import os
import sys
from html.parser import HTMLParser
from urllib.parse import unquote, urljoin, urlsplit

IMAGE_EXT = {".jpg", ".jpeg", ".png", ".webp", ".avif", ".gif", ".svg"}
LINK_ATTRS = {"a": "href", "link": "href", "img": "src", "script": "src", "source": "src",
              "iframe": "src", "video": "src", "audio": "src"}
SKIP_SCHEMES = {"mailto", "tel", "javascript", "data", "sms", "geo"}


class Page(HTMLParser):
    def __init__(self):
        super().__init__()
        self.ids = set()
        self.links = []

    def handle_starttag(self, tag, attrs):
        a = dict(attrs)
        for key in ("id", "name"):
            if a.get(key):
                self.ids.add(a[key])
        attr = LINK_ATTRS.get(tag)
        if attr and a.get(attr):
            self.links.append(a[attr])
        if tag in ("img", "source") and a.get("srcset"):
            for part in a["srcset"].split(","):
                if part.strip():
                    self.links.append(part.split()[0])


def main():
    if len(sys.argv) < 3:
        sys.exit(__doc__)
    root = os.path.abspath(sys.argv[1])
    base = urlsplit(sys.argv[2])
    max_kib = int(sys.argv[3]) if len(sys.argv) > 3 else 500
    base_path = base.path if base.path.endswith("/") else base.path + "/"
    errors = []
    anchors = {}

    pages = {}
    for dirpath, _, names in os.walk(root):
        for name in names:
            full = os.path.join(dirpath, name)
            ext = os.path.splitext(name)[1].lower()
            if ext in IMAGE_EXT:
                size = os.path.getsize(full) / 1024
                if size > max_kib:
                    errors.append(f"image trop lourde : {os.path.relpath(full, root)} "
                                  f"({size:.0f} Kio, max {max_kib} Kio)")
            if ext == ".html":
                p = Page()
                with open(full, encoding="utf-8", errors="replace") as f:
                    p.feed(f.read())
                pages[full] = p

    def url_of(full):
        rel = os.path.relpath(full, root).replace(os.sep, "/")
        if rel.endswith("index.html"):
            rel = rel[: -len("index.html")]
        return base_path + rel

    def resolve(path):
        """Fichier de public/ servi pour un chemin relatif à la racine du site, ou None."""
        full = os.path.normpath(os.path.join(root, unquote(path)))
        if full != root and not full.startswith(root + os.sep):
            return None
        if os.path.isdir(full):
            full = os.path.join(full, "index.html")
        return full if os.path.isfile(full) else None

    count = 0
    for full, page in pages.items():
        name = os.path.relpath(full, root)
        here = url_of(full)
        for raw in page.links:
            raw = raw.strip()
            if not raw:
                continue
            parts = urlsplit(raw)
            if parts.scheme in SKIP_SCHEMES:
                continue
            if parts.scheme or parts.netloc:
                if (parts.scheme, parts.netloc) != (base.scheme, base.netloc):
                    continue
            elif raw.startswith("/") and not raw.startswith("//") and not raw.startswith(base_path):
                # Sous un préfixe de preview, un lien absolu sans préfixe sortirait de la preview.
                errors.append(f"{name} : lien hors du préfixe {base_path} : {raw}")
                continue
            target = urlsplit(urljoin(f"{base.scheme}://{base.netloc}{here}", raw))
            if not target.path.startswith(base_path):
                continue
            count += 1
            dest = resolve(target.path[len(base_path):])
            if dest is None:
                errors.append(f"{name} : lien cassé : {raw}")
                continue
            frag = unquote(target.fragment)
            if frag and frag != "top" and dest in pages and frag not in pages[dest].ids:
                anchors.setdefault(raw, name)

    print(f"{len(pages)} pages, {count} liens internes vérifiés, images <= {max_kib} Kio")
    # Une ancre absente ne casse pas la page : avertissement, sans faire échouer le build.
    for link, name in sorted(anchors.items()):
        print(f"::warning::ancre absente : {link} (depuis {name})")
    if errors:
        for e in sorted(set(errors)):
            print(f"ERREUR {e}", file=sys.stderr)
        sys.exit(1)


if __name__ == "__main__":
    main()
