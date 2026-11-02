package sites

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func prodTarget(t *testing.T, sitesDir string) string {
	t.Helper()
	target, err := os.Readlink(filepath.Join(sitesDir, "prod"))
	if err != nil {
		t.Fatalf("prod n'est pas un lien : %v", err)
	}
	return target
}

func deployProd(t *testing.T, s *Store, c byte) ProdResult {
	t.Helper()
	res, err := s.DeployProd(sha(c), bytes.NewReader(site(t, string(c))))
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestDeployProdFirstReplacesEmptyDir(t *testing.T) {
	s, sitesDir, stateDir := newTestStore(t)
	res := deployProd(t, s, 'a')
	if res.Previous != "" || res.Reused {
		t.Fatalf("résultat %+v", res)
	}
	if got := prodTarget(t, sitesDir); got != "releases/"+sha('a') {
		t.Fatalf("lien prod %q, attendu relatif releases/<sha>", got)
	}
	if got := readFile(t, filepath.Join(sitesDir, "prod", "index.html")); got != "<h1>a</h1>" {
		t.Fatalf("contenu servi %q", got)
	}
	assertEmptyDir(t, filepath.Join(sitesDir, ".tmp"))
	if !strings.Contains(readFile(t, filepath.Join(stateDir, "releases.json")), sha('a')) {
		t.Fatal("historique non écrit")
	}

	res = deployProd(t, s, 'b')
	if res.Previous != sha('a') {
		t.Fatalf("previous %q", res.Previous)
	}
	if got := readFile(t, filepath.Join(sitesDir, "prod", "index.html")); got != "<h1>b</h1>" {
		t.Fatalf("contenu servi %q", got)
	}
}

func TestDeployProdRefusesNonEmptyProdDir(t *testing.T) {
	s, sitesDir, _ := newTestStore(t)
	if err := os.WriteFile(filepath.Join(sitesDir, "prod", "index.html"), []byte("ancien"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeployProd(sha('a'), bytes.NewReader(site(t, "a"))); err == nil {
		t.Fatal("un dossier prod non vide a été remplacé")
	}
	if got := readFile(t, filepath.Join(sitesDir, "prod", "index.html")); got != "ancien" {
		t.Fatalf("prod modifiée : %q", got)
	}
}

func TestDeployProdReusesExistingRelease(t *testing.T) {
	s, sitesDir, _ := newTestStore(t)
	deployProd(t, s, 'a')
	deployProd(t, s, 'b')
	// Archive invalide : prouve qu'elle n'est pas relue quand la release existe.
	res, err := s.DeployProd(sha('a'), strings.NewReader("pas une archive"))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Reused || res.Previous != sha('b') {
		t.Fatalf("résultat %+v", res)
	}
	if got := prodTarget(t, sitesDir); got != "releases/"+sha('a') {
		t.Fatalf("lien prod %q", got)
	}
	rels, _ := s.Releases()
	if !slices.Equal(rels, []string{sha('b'), sha('a')}) {
		t.Fatalf("ordre des releases %v", rels)
	}
}

func TestDeployProdFailureKeepsProd(t *testing.T) {
	cases := []struct {
		name      string
		archive   func(t *testing.T) []byte
		failpoint string
		// la release extraite reste disponible si seule la bascule a échoué
		releaseLeft bool
	}{
		{name: "archive tronquée", archive: func(t *testing.T) []byte { a := site(t, "b"); return a[:len(a)-20] }},
		{name: "archive dangereuse", archive: func(t *testing.T) []byte {
			return tarGz(t, file("index.html", "x"), file("../evil", "x"))
		}},
		{name: "panne après extraction", archive: func(t *testing.T) []byte { return site(t, "b") }, failpoint: "prod:extracted"},
		{name: "panne avant bascule", archive: func(t *testing.T) []byte { return site(t, "b") }, failpoint: "prod:before-switch", releaseLeft: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, sitesDir, _ := newTestStore(t)
			deployProd(t, s, 'a')
			boom := errors.New("panne simulée")
			s.failpoint = func(stage string) error {
				if stage == tc.failpoint {
					return boom
				}
				return nil
			}
			if _, err := s.DeployProd(sha('b'), bytes.NewReader(tc.archive(t))); err == nil {
				t.Fatal("déploiement accepté")
			}
			if got := prodTarget(t, sitesDir); got != "releases/"+sha('a') {
				t.Fatalf("prod a bougé : %q", got)
			}
			if got := readFile(t, filepath.Join(sitesDir, "prod", "index.html")); got != "<h1>a</h1>" {
				t.Fatalf("contenu servi %q", got)
			}
			assertEmptyDir(t, filepath.Join(sitesDir, ".tmp"))
			_, err := os.Stat(filepath.Join(sitesDir, "releases", sha('b')))
			if left := err == nil; left != tc.releaseLeft {
				t.Fatalf("release b présente = %v, attendu %v", left, tc.releaseLeft)
			}
			if cur, _ := s.Current(); cur != sha('a') {
				t.Fatalf("Current %q", cur)
			}
		})
	}
}

func TestDeployProdRetention(t *testing.T) {
	s, sitesDir, stateDir := newTestStore(t)
	for _, c := range []byte("1234567") {
		deployProd(t, s, c)
	}
	rels, err := s.Releases()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{sha('3'), sha('4'), sha('5'), sha('6'), sha('7')}
	if !slices.Equal(rels, want) {
		t.Fatalf("releases %v, attendu %v", rels, want)
	}
	hist := readFile(t, filepath.Join(stateDir, "releases.json"))
	if strings.Contains(hist, sha('1')) || strings.Contains(hist, sha('2')) {
		t.Fatal("l'historique garde des releases supprimées")
	}
	assertEmptyDir(t, filepath.Join(sitesDir, ".tmp"))
}

func TestPruneNeverRemovesCurrent(t *testing.T) {
	s, sitesDir, _ := newTestStore(t)
	for _, c := range []byte("abcde") {
		deployProd(t, s, c)
	}
	if _, _, err := s.Rollback(sha('a')); err != nil {
		t.Fatal(err)
	}
	// La release en ligne est la plus ancienne, hors de la fenêtre conservée.
	s.cfg.Keep = 2
	if err := s.prune(sha('a')); err != nil {
		t.Fatal(err)
	}
	rels, _ := s.Releases()
	if want := []string{sha('a'), sha('d'), sha('e')}; !slices.Equal(rels, want) {
		t.Fatalf("releases %v, attendu %v", rels, want)
	}
	if got := readFile(t, filepath.Join(sitesDir, "prod", "index.html")); got != "<h1>a</h1>" {
		t.Fatalf("contenu servi %q", got)
	}
}

func TestRollback(t *testing.T) {
	s, sitesDir, _ := newTestStore(t)
	if _, _, err := s.Rollback(""); err == nil {
		t.Fatal("rollback sans prod accepté")
	}
	for _, c := range []byte("abc") {
		deployProd(t, s, c)
	}
	from, to, err := s.Rollback("")
	if err != nil || from != sha('c') || to != sha('b') {
		t.Fatalf("rollback : %s -> %s, %v", from, to, err)
	}
	if got := prodTarget(t, sitesDir); got != "releases/"+sha('b') {
		t.Fatalf("lien prod %q", got)
	}
	if _, to, _ = s.Rollback(""); to != sha('a') {
		t.Fatalf("second rollback vers %q", to)
	}
	if _, _, err = s.Rollback(""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("rollback au-delà de la plus ancienne : %v", err)
	}
	if _, to, err = s.Rollback(sha('c')[:7]); err != nil || to != sha('c') {
		t.Fatalf("rollback par préfixe : %q, %v", to, err)
	}
	for _, bad := range []string{"cccc", "ZZZZZZZ", "../../x"} {
		if _, _, err := s.Rollback(bad); err == nil {
			t.Errorf("sha %q accepté", bad)
		}
	}
	if _, _, err := s.Rollback(sha('d')); !errors.Is(err, ErrNotFound) {
		t.Fatalf("release absente : %v", err)
	}
	assertEmptyDir(t, filepath.Join(sitesDir, ".tmp"))
}

func TestRollbackAmbiguousPrefix(t *testing.T) {
	s, _, _ := newTestStore(t)
	a := "abcdef0" + strings.Repeat("1", 33)
	b := "abcdef0" + strings.Repeat("2", 33)
	for _, x := range []string{a, b} {
		if _, err := s.DeployProd(x, bytes.NewReader(site(t, x))); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := s.Rollback("abcdef0"); err == nil || !strings.Contains(err.Error(), "ambigu") {
		t.Fatalf("préfixe ambigu : %v", err)
	}
}

func TestInvalidSHA(t *testing.T) {
	s, _, _ := newTestStore(t)
	for _, bad := range []string{"", "abc", strings.Repeat("A", 40), "../" + strings.Repeat("a", 37)} {
		if _, err := s.DeployProd(bad, bytes.NewReader(site(t, "x"))); err == nil {
			t.Errorf("DeployProd(%q) accepté", bad)
		}
		if err := s.DeployPreview(1, bad, bytes.NewReader(site(t, "x"))); err == nil {
			t.Errorf("DeployPreview(%q) accepté", bad)
		}
	}
	if err := s.DeployPreview(0, sha('a'), bytes.NewReader(site(t, "x"))); err == nil {
		t.Error("PR 0 acceptée")
	}
}

func TestPreviewLifecycle(t *testing.T) {
	s, sitesDir, stateDir := newTestStore(t)
	previews := filepath.Join(sitesDir, "previews")
	index := filepath.Join(previews, "index.html")
	if !strings.Contains(readFile(t, index), "Aucune prévisualisation active") {
		t.Fatal("index initial sans mention d'absence")
	}

	if err := s.DeployPreview(12, sha('a'), bytes.NewReader(site(t, "v1"))); err != nil {
		t.Fatal(err)
	}
	if err := s.DeployPreview(7, sha('b'), bytes.NewReader(site(t, "autre"))); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(previews, "pr-12", "index.html")); got != "<h1>v1</h1>" {
		t.Fatalf("preview %q", got)
	}
	html := readFile(t, index)
	for _, want := range []string{`href="pr-12/"`, `href="pr-7/"`, "aaaaaaa", "bbbbbbb", `href="maquettes/home/"`,
		`<meta name="robots" content="noindex">`, `lang="fr"`} {
		if !strings.Contains(html, want) {
			t.Errorf("index sans %q", want)
		}
	}
	if strings.Index(html, "pr-12/") > strings.Index(html, "pr-7/") {
		t.Error("index non trié de la PR la plus récente à la plus ancienne")
	}

	if err := s.DeployPreview(12, sha('c'), bytes.NewReader(site(t, "v2"))); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(previews, "pr-12", "index.html")); got != "<h1>v2</h1>" {
		t.Fatalf("preview remplacée %q", got)
	}
	if html := readFile(t, index); !strings.Contains(html, "ccccccc") || strings.Contains(html, "aaaaaaa") {
		t.Fatal("index non mis à jour après remplacement")
	}
	if !strings.Contains(readFile(t, filepath.Join(stateDir, "previews.json")), sha('c')) {
		t.Fatal("état des previews non écrit")
	}

	deleted, err := s.DeletePreview(12)
	if err != nil || !deleted {
		t.Fatalf("suppression : %v %v", deleted, err)
	}
	if _, err := os.Stat(filepath.Join(previews, "pr-12")); !os.IsNotExist(err) {
		t.Fatal("dossier de preview encore là")
	}
	if strings.Contains(readFile(t, index), "pr-12/") {
		t.Fatal("index garde la preview supprimée")
	}
	if strings.Contains(readFile(t, filepath.Join(stateDir, "previews.json")), sha('c')) {
		t.Fatal("état garde la preview supprimée")
	}
	if deleted, err = s.DeletePreview(12); err != nil || deleted {
		t.Fatalf("seconde suppression : %v %v", deleted, err)
	}
	assertEmptyDir(t, filepath.Join(sitesDir, ".tmp"))
}

func TestPreviewFailureKeepsOld(t *testing.T) {
	for _, stage := range []string{"preview:extracted", "preview:swapping", "archive"} {
		t.Run(stage, func(t *testing.T) {
			s, sitesDir, _ := newTestStore(t)
			if err := s.DeployPreview(3, sha('a'), bytes.NewReader(site(t, "v1"))); err != nil {
				t.Fatal(err)
			}
			s.failpoint = func(st string) error {
				if st == stage {
					return errors.New("panne simulée")
				}
				return nil
			}
			arc := site(t, "v2")
			if stage == "archive" {
				arc = arc[:len(arc)/2]
			}
			if err := s.DeployPreview(3, sha('b'), bytes.NewReader(arc)); err == nil {
				t.Fatal("déploiement accepté")
			}
			if got := readFile(t, filepath.Join(sitesDir, "previews", "pr-3", "index.html")); got != "<h1>v1</h1>" {
				t.Fatalf("ancienne preview perdue : %q", got)
			}
			if !strings.Contains(readFile(t, filepath.Join(sitesDir, "previews", "index.html")), "aaaaaaa") {
				t.Fatal("index modifié par un échec")
			}
			assertEmptyDir(t, filepath.Join(sitesDir, ".tmp"))
		})
	}
}

func TestIndexIgnoresForeignDirs(t *testing.T) {
	s, sitesDir, _ := newTestStore(t)
	for _, d := range []string{"maquettes", "pr-0", "pr-x", "pr-01"} {
		if err := os.MkdirAll(filepath.Join(sitesDir, "previews", d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// Preview présente sur disque mais absente de l'état : listée sans sha.
	if err := os.MkdirAll(filepath.Join(sitesDir, "previews", "pr-5"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	html := readFile(t, filepath.Join(sitesDir, "previews", "index.html"))
	if !strings.Contains(html, `href="pr-5/"`) {
		t.Fatal("pr-5 absente de l'index")
	}
	for _, bad := range []string{`href="pr-0/"`, `href="pr-x/"`, `href="pr-01/"`, `href="maquettes/"`} {
		if strings.Contains(html, bad) {
			t.Errorf("index liste %s", bad)
		}
	}
}

func TestInitCleansWorkDir(t *testing.T) {
	s, sitesDir, _ := newTestStore(t)
	junk := filepath.Join(sitesDir, ".tmp", "stage-123", "sub")
	if err := os.MkdirAll(junk, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	assertEmptyDir(t, filepath.Join(sitesDir, ".tmp"))
}

func TestSetServiceTag(t *testing.T) {
	s, _, stateDir := newTestStore(t)
	for _, tag := range []string{"v1.2.0", "sha-0123456"} {
		if err := s.SetServiceTag(tag); err != nil {
			t.Fatal(err)
		}
		if got := readFile(t, filepath.Join(stateDir, "deployd.tag")); got != tag+"\n" {
			t.Fatalf("tag %q", got)
		}
	}
	entries, _ := os.ReadDir(stateDir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") || strings.Contains(e.Name(), "tmp") {
			t.Fatalf("fichier temporaire laissé : %s", e.Name())
		}
	}
}
