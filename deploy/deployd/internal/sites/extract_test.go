package sites

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExtractValid(t *testing.T) {
	dst := t.TempDir()
	arc := tarGz(t,
		dir("./"),
		file("./index.html", "accueil"),
		entry{name: "./secret.txt", typ: tar.TypeReg, body: "x", mode: 0o600},
		entry{name: "./bin.sh", typ: tar.TypeReg, body: "x", mode: 0o777},
		file("img/a/b/c.png", "png"), // parents sans entrée de dossier
		entry{name: "./ouvert/", typ: tar.TypeDir, mode: 0o777},
		entry{name: "pax_global_header", typ: tar.TypeXGlobalHeader},
	)
	if err := extract(bytes.NewReader(arc), dst, Limits{MaxFiles: 100, MaxBytes: 1 << 20}); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(dst, "img/a/b/c.png")); got != "png" {
		t.Fatalf("contenu %q", got)
	}
	for path, want := range map[string]os.FileMode{
		"index.html": 0o644, "secret.txt": 0o644, "bin.sh": 0o644,
		"img": 0o755, "img/a/b": 0o755, "ouvert": 0o755,
	} {
		fi, err := os.Stat(filepath.Join(dst, path))
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != want {
			t.Errorf("%s : permissions %o, attendu %o", path, fi.Mode().Perm(), want)
		}
	}
}

func TestExtractRejects(t *testing.T) {
	idx := file("./index.html", "ok")
	zeros := strings.Repeat("\x00", 4<<20)
	cases := []struct {
		name   string
		arc    []entry
		limits Limits
	}{
		{name: "remontée ../", arc: []entry{idx, file("../evil.html", "x")}},
		{name: "remontée au milieu", arc: []entry{idx, file("a/../../evil.html", "x")}},
		{name: "remontée ./..", arc: []entry{idx, file("./../evil.html", "x")}},
		{name: "chemin absolu", arc: []entry{idx, file("/tmp/evil.html", "x")}},
		{name: "élément vide", arc: []entry{idx, file("a//b.html", "x")}},
		{name: "antislash", arc: []entry{idx, file(`a\..\b.html`, "x")}},
		{name: "lien symbolique", arc: []entry{idx, {name: "lien", typ: tar.TypeSymlink, link: "/etc/passwd"}}},
		{name: "lien physique", arc: []entry{idx, {name: "lien", typ: tar.TypeLink, link: "index.html"}}},
		{name: "fifo", arc: []entry{idx, {name: "fifo", typ: tar.TypeFifo}}},
		{name: "périphérique", arc: []entry{idx, {name: "dev", typ: tar.TypeChar}}},
		{name: "entrée en double", arc: []entry{idx, file("index.html", "bis")}},
		{name: "fichier puis dossier", arc: []entry{idx, file("a", "x"), file("a/b", "y")}},
		{name: "sans index.html", arc: []entry{file("autre.html", "x")}},
		{name: "trop d'entrées", arc: []entry{idx, file("a", "1"), file("b", "2")}, limits: Limits{MaxFiles: 2, MaxBytes: 1 << 20}},
		{name: "bombe de décompression", arc: []entry{idx, file("zeros.bin", zeros)}, limits: Limits{MaxFiles: 10, MaxBytes: 1 << 20}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			dst := filepath.Join(root, "dst")
			if err := os.Mkdir(dst, 0o755); err != nil {
				t.Fatal(err)
			}
			lim := tc.limits
			if lim.MaxFiles == 0 {
				lim = Limits{MaxFiles: 100, MaxBytes: 1 << 20}
			}
			arc := tarGz(t, tc.arc...)
			if tc.name == "bombe de décompression" && len(arc) > 64<<10 {
				t.Fatalf("archive de test trop grosse (%d), la bombe ne prouve rien", len(arc))
			}
			err := extract(bytes.NewReader(arc), dst, lim)
			if !errors.Is(err, ErrUnsafeArchive) {
				t.Fatalf("erreur %v, attendu ErrUnsafeArchive", err)
			}
			entries, _ := os.ReadDir(root)
			if len(entries) != 1 {
				t.Fatalf("écriture hors de la destination : %v", entries)
			}
			if _, err := os.Lstat(filepath.Join(dst, "lien")); err == nil {
				t.Fatal("un lien a été créé")
			}
		})
	}
}

func TestExtractCorrupt(t *testing.T) {
	arc := site(t, "a")
	cases := map[string][]byte{
		"pas du gzip":      []byte("bonjour"),
		"gzip tronqué":     arc[:len(arc)/2],
		"crc final faussé": append(append([]byte{}, arc[:len(arc)-8]...), 0, 0, 0, 0, 0, 0, 0, 0),
	}
	var notTar bytes.Buffer
	gz := gzip.NewWriter(&notTar)
	_, _ = gz.Write(bytes.Repeat([]byte("x"), 2000))
	_ = gz.Close()
	cases["gzip sans tar"] = notTar.Bytes()
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			if err := extract(bytes.NewReader(data), t.TempDir(), Limits{MaxFiles: 100, MaxBytes: 1 << 20}); err == nil {
				t.Fatal("archive corrompue acceptée")
			}
		})
	}
}
