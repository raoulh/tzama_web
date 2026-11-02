package sites

import (
	"bytes"
	"html/template"
	"os"
	"regexp"
	"slices"
	"strconv"
)

var previewDirRe = regexp.MustCompile(`^pr-([1-9][0-9]{0,9})$`)

var indexTmpl = template.Must(template.New("index").Parse(`<!DOCTYPE html>
<html lang="fr">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="robots" content="noindex">
<title>Tzama · prévisualisations</title>
<style>body{font-family:system-ui,sans-serif;max-width:40rem;margin:2rem auto;padding:0 1rem;line-height:1.5}li{margin:.4rem 0}</style>
</head>
<body>
<h1>Prévisualisations des pull requests</h1>
{{if .Previews}}<ul>
{{range .Previews}}<li><a href="pr-{{.PR}}/">PR n° {{.PR}}</a> · <code>{{.Short}}</code> · {{.Date}}</li>
{{end}}</ul>
{{else}}<p>Aucune prévisualisation active.</p>
{{end}}<p><a href="maquettes/home/">Maquettes</a></p>
<p><small>Page générée automatiquement le {{.Updated}}.</small></p>
</body>
</html>
`))

type indexItem struct {
	PR    int
	Short string
	Date  string
}

// writeIndex reconstruit previews/index.html à partir des dossiers présents : l'état sert
// seulement à afficher sha et date.
func (s *Store) writeIndex() error {
	entries, err := os.ReadDir(s.path(previewsDir))
	if err != nil {
		return err
	}
	st := s.loadPreviews()
	var items []indexItem
	for _, e := range entries {
		m := previewDirRe.FindStringSubmatch(e.Name())
		if m == nil || !e.IsDir() {
			continue
		}
		pr, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}
		it := indexItem{PR: pr, Short: "?", Date: "?"}
		if meta, ok := st.Previews[pr]; ok {
			it.Short = meta.SHA[:min(7, len(meta.SHA))]
			it.Date = meta.DeployedAt.In(s.cfg.Location).Format("02/01/2006 15:04")
		}
		items = append(items, it)
	}
	slices.SortFunc(items, func(a, b indexItem) int { return b.PR - a.PR })
	var buf bytes.Buffer
	err = indexTmpl.Execute(&buf, map[string]any{
		"Previews": items,
		"Updated":  s.cfg.Now().In(s.cfg.Location).Format("02/01/2006 à 15:04"),
	})
	if err != nil {
		return err
	}
	return writeFileAtomic(s.path(previewsDir, "index.html"), buf.Bytes(), 0o644)
}
