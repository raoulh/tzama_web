"""Génère le questionnaire aux familles (PDF A4 remplissable et imprimable) depuis site/data/questionnaire.yaml."""

import argparse
import re
import sys
import tempfile
import unicodedata
from pathlib import Path

import yaml
from fontTools.ttLib import TTFont as FTFont
from fontTools.varLib.instancer import instantiateVariableFont
from reportlab.lib.colors import HexColor
from reportlab.lib.pagesizes import A4
from reportlab.lib.units import mm
from reportlab.pdfbase import pdfmetrics
from reportlab.pdfbase.ttfonts import TTFont
from reportlab.pdfgen import canvas

ROOT = Path(__file__).resolve().parents[2]
DATA = ROOT / "site/data/questionnaire.yaml"
SETTINGS = ROOT / "site/data/settings.yaml"
FONTS = ROOT / "site/assets/fonts"
LOGO = ROOT / "site/assets/img/logo/logo-tzama.png"
OUT = ROOT / "site/assets/documents/questionnaire-familles.pdf"

# Palette de site/assets/css/tokens.css
ENCRE = HexColor("#26241e")
ENCRE_2 = HexColor("#57544a")
SAPIN = HexColor("#1f4d2e")
ACCENT = HexColor("#3f7320")
VERT = HexColor("#669900")
TEINTE = HexColor("#e6eddb")
FOND = HexColor("#fdfbf7")
# --ligne (#e3ddd1) disparaît à l'impression : même teinte, plus soutenue.
CADRE = HexColor("#b4ac9b")

PAGE_W, PAGE_H = A4
MARGIN = 16 * mm
BOTTOM = MARGIN
CONTENT_W = PAGE_W - 2 * MARGIN
FIELD_H = 7.5 * mm
BOX = 3.8 * mm
GAP = 4 * mm
COL_GAP = 8 * mm
LABEL = ("Baloo-Semi", 9.5)
QUESTION = ("Baloo-Semi", 10)

WIDTHS = {"full": 1, "two_thirds": 2 / 3, "half": 1 / 2, "third": 1 / 3, "quarter": 1 / 4}
SIZES = {"small": 12 * mm, "medium": 19 * mm, "large": 45 * mm}


# --- validation ---------------------------------------------------------------


class Invalid(Exception):
    pass


def _str(obj, key, where, required=True):
    v = obj.get(key)
    if not required and (v is None or v == ""):
        return None
    if not isinstance(v, str) or not v.strip():
        raise Invalid(f"{where} : « {key} » doit être un texte non vide")
    return v.strip()


def _keys(obj, allowed, where):
    if not isinstance(obj, dict):
        raise Invalid(f"{where} : un objet est attendu")
    unknown = set(obj) - set(allowed)
    if unknown:
        raise Invalid(f"{where} : champ inconnu : {', '.join(sorted(unknown))}")


def _choice(obj, key, allowed, default, where):
    v = obj.get(key, default)
    if v not in allowed:
        raise Invalid(f"{where} : « {key} » doit valoir {' ou '.join(allowed)} (reçu : {v!r})")
    return v


def validate(data):
    _keys(data, {"title", "subtitle", "intro", "document", "blocks"}, "racine")
    _str(data, "title", "racine")
    _str(data, "subtitle", "racine", required=False)
    _str(data, "intro", "racine", required=False)
    doc = data.get("document")
    _keys(doc, {"title", "description"}, "document")
    _str(doc, "title", "document")
    _str(doc, "description", "document", required=False)
    blocks = data.get("blocks")
    if not isinstance(blocks, list) or not blocks:
        raise Invalid("racine : « blocks » doit être une liste non vide")
    for i, b in enumerate(blocks, 1):
        where = f"bloc {i}"
        _keys(b, {"title", "note", "columns", "page_break", "fields"}, where)
        where = f"bloc {i} « {_str(b, 'title', where)} »"
        _str(b, "note", where, required=False)
        if not isinstance(b.get("page_break", False), bool):
            raise Invalid(f"{where} : « page_break » doit valoir true ou false")
        cols = b.get("columns")
        if cols not in (None, []) and (not isinstance(cols, list) or len(cols) > 3
                                 or not all(isinstance(c, str) and c.strip() for c in cols)):
            raise Invalid(f"{where} : « columns » doit être une liste de 1 à 3 titres")
        fields = b.get("fields")
        if not isinstance(fields, list) or not fields:
            raise Invalid(f"{where} : « fields » doit être une liste non vide")
        for j, f in enumerate(fields, 1):
            fw = f"{where}, champ {j}"
            if not isinstance(f, dict):
                raise Invalid(f"{fw} : un objet est attendu")
            kind = _choice(f, "type", ("text", "textarea", "checkboxes"), None, fw)
            if kind == "text":
                _keys(f, {"type", "label", "width"}, fw)
                _str(f, "label", fw)
                _choice(f, "width", tuple(WIDTHS), "full", fw)
            elif kind == "textarea":
                _keys(f, {"type", "label", "hint", "size"}, fw)
                _str(f, "label", fw)
                _str(f, "hint", fw, required=False)
                _choice(f, "size", tuple(SIZES), "medium", fw)
            else:
                _keys(f, {"type", "label", "options", "other"}, fw)
                _str(f, "label", fw, required=False)
                _str(f, "other", fw, required=False)
                opts = f.get("options")
                if not isinstance(opts, list) or not opts or not all(isinstance(o, str) and o.strip() for o in opts):
                    raise Invalid(f"{fw} : « options » doit être une liste de textes non vides")
    return data


# --- polices et outils -------------------------------------------------------


def register_fonts(tmp: Path) -> None:
    # reportlab n'embarque que du TrueType statique : on fige les graisses de la police variable.
    for name, wght in (("Baloo", 400), ("Baloo-Semi", 600), ("Baloo-Bold", 700)):
        font = FTFont(FONTS / "baloo-2-400-800-normal.woff2")
        font.flavor = None
        instantiateVariableFont(font, {"wght": wght}, inplace=True)
        font.save(tmp / f"{name}.ttf")
        pdfmetrics.registerFont(TTFont(name, tmp / f"{name}.ttf"))
    font = FTFont(FONTS / "gochi-hand-400-normal.woff2")
    font.flavor = None
    font.save(tmp / "Gochi.ttf")
    pdfmetrics.registerFont(TTFont("Gochi", tmp / "Gochi.ttf"))


def wrap(text, font, size, width):
    lines, line = [], ""
    for word in text.split():
        trial = f"{line} {word}".strip()
        if line and pdfmetrics.stringWidth(trial, font, size) > width:
            lines.append(line)
            line = word
        else:
            line = trial
    return lines + [line] if line else lines


def slug(s):
    s = unicodedata.normalize("NFKD", s).encode("ascii", "ignore").decode().lower()
    return re.sub(r"[^a-z0-9]+", "_", s).strip("_")[:40] or "champ"


class Names:
    """Noms de champs PDF stables et uniques, tirés des libellés."""

    def __init__(self):
        self.seen = set()

    def __call__(self, *parts):
        base = "_".join(slug(p) for p in parts if p)
        name, n = base, 2
        while name in self.seen:
            name, n = f"{base}_{n}", n + 1
        self.seen.add(name)
        return name


# --- éléments mesurables -----------------------------------------------------
# Chaque élément connaît sa hauteur pour une largeur donnée : la pagination se calcule
# avant de dessiner, ce qui donne le nombre total de pages pour le pied de page.


class TextRow:
    def __init__(self, fields, prefix, names, tip=None):
        self.fields = [(f["label"], names(prefix, f["label"]), WIDTHS[f.get("width", "full")]) for f in fields]
        self.tip = tip

    def height(self, width):
        return 5 * mm + FIELD_H + 2.5 * mm

    def draw(self, pdf, x, y, width):
        for label, name, part in self.fields:
            # Même grille quelle que soit la ligne : deux « moitié » s'alignent sur une « moitié » seule.
            w = part * (width + GAP) - GAP
            pdf.fit_text(x, y - 3.6 * mm, label, *LABEL, w)
            tooltip = f"{self.tip} : {label}" if self.tip else label
            pdf.textfield(name, tooltip, x, y - 5 * mm - FIELD_H, w, FIELD_H)
            x += w + GAP


class Checkboxes:
    def __init__(self, f, prefix, names):
        self.label = f.get("label")
        self.options = [(o, names(prefix, f.get("label"), o)) for o in f["options"]]
        self.other = f.get("other")
        if self.other:
            self.other_names = (names(prefix, f.get("label"), self.other), names(prefix, f.get("label"), self.other, "precision"))

    def _layout(self, width):
        """Options en ligne, retour à la ligne quand la largeur est atteinte."""
        rows, row, used = [], [], 0
        for label, name in self.options:
            w = BOX + 2 * mm + pdfmetrics.stringWidth(label, "Baloo", 9.5)
            if row and used + w > width:
                rows.append(row)
                row, used = [], 0
            row.append((used, label, name))
            used += w + 7 * mm
        return rows + [row]

    def height(self, width):
        h = len(wrap(self.label, *QUESTION, width)) * 4.6 * mm if self.label else 0
        h += len(self._layout(width)) * 7 * mm
        return h + (9 * mm if self.other else 0) + 1 * mm

    def draw(self, pdf, x, y, width):
        if self.label:
            for line in wrap(self.label, *QUESTION, width):
                pdf.text(x, y - 3.8 * mm, line, *QUESTION)
                y -= 4.6 * mm
        for row in self._layout(width):
            for dx, label, name in row:
                pdf.checkbox(name, label, x + dx, y - 4.6 * mm)
                pdf.text(x + dx + BOX + 2 * mm, y - 4.1 * mm, label)
            y -= 7 * mm
        if self.other:
            box, field = self.other_names
            pdf.checkbox(box, self.other, x, y - 4.6 * mm)
            pdf.text(x + BOX + 2 * mm, y - 4.1 * mm, self.other)
            lx = x + BOX + 4 * mm + pdfmetrics.stringWidth(self.other, "Baloo", 9.5)
            pdf.textfield(field, self.other, lx, y - 6.3 * mm, max(x + width - lx, 30 * mm), 7 * mm)


class TextArea:
    def __init__(self, f, prefix, names):
        self.label, self.hint = f["label"], f.get("hint")
        self.size = f.get("size", "medium")
        self.name = names(prefix, f["label"])
        self.fill = None

    def _head(self, width):
        return len(wrap(self.label, *QUESTION, width)) * 4.6 * mm + (4.2 * mm if self.hint else 0) + 1.2 * mm

    def height(self, width):
        return self._head(width) + (self.fill or SIZES[self.size]) + 3.4 * mm

    def draw(self, pdf, x, y, width):
        for line in wrap(self.label, *QUESTION, width):
            pdf.text(x, y - 3.8 * mm, line, *QUESTION)
            y -= 4.6 * mm
        if self.hint:
            pdf.text(x, y - 3.4 * mm, self.hint, "Baloo", 8.5, ENCRE_2)
            y -= 4.2 * mm
        y -= 1.2 * mm
        h = self.fill or SIZES[self.size]
        pdf.textfield(self.name, self.label, x, y - h, width, h, multiline=True)


class Heading:
    def __init__(self, title, note=None):
        self.title, self.note = title, note

    def height(self, width):
        return 10 * mm + (len(wrap(self.note, "Baloo", 9, width)) * 4.5 * mm if self.note else 0)

    def draw(self, pdf, x, y, width):
        y -= 7 * mm
        pdf.text(x, y, self.title, "Gochi", 17, SAPIN)
        tw = pdfmetrics.stringWidth(self.title, "Gochi", 17)
        c = pdf.c
        c.setStrokeColor(VERT)
        c.setLineWidth(1.4)
        c.setLineCap(1)
        if x + tw + 3 * mm < x + width:
            c.line(x + tw + 3 * mm, y + 1.6 * mm, x + width, y + 1.6 * mm)
        y -= 3 * mm
        if self.note:
            for line in wrap(self.note, "Baloo", 9, width):
                pdf.text(x, y - 3.6 * mm, line, "Baloo", 9, ENCRE_2)
                y -= 4.5 * mm


def fields_to_elements(fields, prefix, names, tip=None):
    """Regroupe les champs texte consécutifs en lignes selon leur largeur."""
    out, row, used = [], [], 0
    for f in fields:
        if f["type"] == "text":
            part = WIDTHS[f.get("width", "full")]
            if row and used + part > 1 + 1e-6:
                out.append(TextRow(row, prefix, names, tip))
                row, used = [], 0
            row.append(f)
            used += part
            continue
        if row:
            out.append(TextRow(row, prefix, names, tip))
            row, used = [], 0
        out.append((Checkboxes if f["type"] == "checkboxes" else TextArea)(f, prefix, names))
    if row:
        out.append(TextRow(row, prefix, names, tip))
    return out


class Columns:
    """Mêmes champs répétés côte à côte, un titre par colonne (Parent 1, Parent 2…)."""

    def __init__(self, titles, fields, block, names):
        self.cols = [(t, fields_to_elements(fields, f"{block} {t}", names, t)) for t in titles]

    def _w(self, width):
        return (width - COL_GAP * (len(self.cols) - 1)) / len(self.cols)

    def height(self, width):
        w = self._w(width)
        return max(5 * mm + sum(e.height(w) for e in els) for _, els in self.cols)

    def draw(self, pdf, x, y, width):
        w = self._w(width)
        for title, els in self.cols:
            pdf.text(x, y - 4 * mm, title, "Baloo-Bold", 10.5, ACCENT)
            cy = y - 5 * mm
            for e in els:
                e.draw(pdf, x, cy, w)
                cy -= e.height(w)
            x += w + COL_GAP


# --- document ------------------------------------------------------------------


class Pdf:
    def __init__(self, path, data, footer):
        self.c = canvas.Canvas(str(path), pagesize=A4, invariant=1, lang="fr-FR")
        self.c.setTitle(data["title"])
        self.c.setAuthor(footer.split(" · ")[0])
        self.c.setSubject(data["document"]["title"])
        self.footer_text = footer

    def text(self, x, y, s, font="Baloo", size=9.5, color=ENCRE):
        self.c.setFillColor(color)
        self.c.setFont(font, size)
        self.c.drawString(x, y, s)

    def fit_text(self, x, y, s, font, size, width, color=ENCRE):
        while size > 7 and pdfmetrics.stringWidth(s, font, size) > width:
            size -= 0.5
        self.text(x, y, s, font, size, color)

    def frame(self, x, y, w, h, radius=1.6 * mm):
        self.c.setStrokeColor(CADRE)
        self.c.setFillColor(FOND)
        self.c.setLineWidth(0.7)
        self.c.roundRect(x, y, w, h, radius, stroke=1, fill=1)

    def textfield(self, name, tooltip, x, y, w, h, multiline=False):
        self.frame(x, y, w, h)
        pad = 1.2 * mm
        self.c.acroForm.textfield(
            name=name, tooltip=tooltip,
            x=x + pad, y=y + 0.6 * mm, width=w - 2 * pad, height=h - 1.2 * mm,
            fontName="Helvetica", fontSize=10, textColor=ENCRE,
            # Cadre et fond sont dessinés sur la page : le champ reste transparent pour que
            # le formulaire imprimé vide ait le même aspect que rempli à l'écran.
            fillColor=None, borderColor=None, borderWidth=0,
            fieldFlags="multiline" if multiline else "",
            maxlen=None if multiline else 200,
        )

    def checkbox(self, name, tooltip, x, y):
        self.frame(x, y, BOX, BOX, 0.7 * mm)
        self.c.acroForm.checkbox(
            name=name, tooltip=tooltip, x=x, y=y, size=BOX,
            buttonStyle="check", textColor=SAPIN,
            fillColor=None, borderColor=None, borderWidth=0, fieldFlags="",
        )

    def footer(self, page, total):
        y = MARGIN - 7 * mm
        self.c.setStrokeColor(TEINTE)
        self.c.setLineWidth(0.8)
        self.c.line(MARGIN, y + 4 * mm, PAGE_W - MARGIN, y + 4 * mm)
        self.text(MARGIN, y, self.footer_text, "Baloo", 8, ENCRE_2)
        self.c.drawRightString(PAGE_W - MARGIN, y, f"{page} / {total}")

    def header(self, data):
        """Logo, titre et introduction de la première page ; renvoie la hauteur occupée."""
        logo_w = 44 * mm
        logo_h = logo_w * 325 / 600
        top = PAGE_H - MARGIN
        self.c.drawImage(str(LOGO), MARGIN - 2 * mm, top - logo_h + 2 * mm, width=logo_w, height=logo_h, mask="auto")
        tx = MARGIN + logo_w + 4 * mm
        size = 25
        lines = wrap(data["title"], "Gochi", size, PAGE_W - MARGIN - tx)
        while len(lines) > 2 and size > 16:
            size -= 1
            lines = wrap(data["title"], "Gochi", size, PAGE_W - MARGIN - tx)
        y = top - 6 * mm
        for line in lines:
            self.text(tx, y, line, "Gochi", size, SAPIN)
            y -= size * 1.02
        y += size * 1.02
        if data.get("subtitle"):
            y -= 6.5 * mm
            self.fit_text(tx, y, data["subtitle"], "Baloo-Semi", 9.5, PAGE_W - MARGIN - tx, ACCENT)
        y = min(y - 3 * mm, top - logo_h) - 1 * mm
        if data.get("intro"):
            lines = wrap(data["intro"], "Baloo", 9, CONTENT_W - 7 * mm)
            h = len(lines) * 4.3 * mm + 4.6 * mm
            self.c.setFillColor(TEINTE)
            self.c.roundRect(MARGIN, y - h, CONTENT_W, h, 2 * mm, stroke=0, fill=1)
            ly = y - 5.6 * mm
            for line in lines:
                self.text(MARGIN + 3.5 * mm, ly, line, "Baloo", 9, SAPIN)
                ly -= 4.3 * mm
            y -= h + 1 * mm
        return top - y


def paginate(data, header_h):
    """Répartit les éléments sur les pages : [[(élément, y), …], …]."""
    names = Names()
    pages, page = [], []
    top = PAGE_H - MARGIN
    y = top - header_h
    blocks = data["blocks"]

    def new_page():
        nonlocal page, y
        pages.append(page)
        page, y = [], top

    for bi, b in enumerate(blocks):
        heading = Heading(b["title"], b.get("note"))
        if b.get("columns"):
            els = [Columns(b["columns"], b["fields"], b["title"], names)]
        else:
            els = fields_to_elements(b["fields"], b["title"], names)
        last = els[-1]
        # Grande zone finale : elle s'étire jusqu'au bas de la dernière page.
        stretch = bi == len(blocks) - 1 and isinstance(last, TextArea) and last.size == "large"
        if b.get("page_break") and page:
            new_page()
        # Un titre de bloc ne reste jamais seul en bas de page.
        if y - heading.height(CONTENT_W) - els[0].height(CONTENT_W) < BOTTOM and page:
            new_page()
        page.append((heading, y))
        y -= heading.height(CONTENT_W)
        for e in els:
            h = e.height(CONTENT_W)
            if y - h < BOTTOM and page:
                new_page()
            if stretch and e is last:
                e.fill = y - BOTTOM - e._head(CONTENT_W) - 3.4 * mm
                h = e.height(CONTENT_W)
            page.append((e, y))
            y -= h
    pages.append(page)
    return pages


def footer_text():
    s = yaml.safe_load(SETTINGS.read_text(encoding="utf-8"))
    return f"{s['name']} · {s['address']['city']} · {s['email']}"


def build(data_path: Path, out: Path) -> None:
    try:
        data = validate(yaml.safe_load(data_path.read_text(encoding="utf-8")) or {})
    except (yaml.YAMLError, Invalid) as e:
        sys.exit(f"{data_path.relative_to(ROOT) if data_path.is_relative_to(ROOT) else data_path} : {e}")
    out.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory() as tmp:
        register_fonts(Path(tmp))
        pdf = Pdf(out, data, footer_text())
        header_h = pdf.header(data)
        pages = paginate(data, header_h)
        for n, page in enumerate(pages, 1):
            if n > 1:
                pdf.c.showPage()
            for el, y in page:
                el.draw(pdf, MARGIN, y, CONTENT_W)
            pdf.footer(n, len(pages))
        pdf.c.save()
    print(f"{out.relative_to(ROOT) if out.is_relative_to(ROOT) else out} : {len(pages)} page(s)")


def main() -> None:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("-d", "--data", type=Path, default=DATA)
    ap.add_argument("-o", "--output", type=Path, default=OUT)
    args = ap.parse_args()
    build(args.data, args.output)


if __name__ == "__main__":
    main()
