package hook

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"testing/iotest"
	"time"
)

// countingBody compte les octets lus et peut agir à la fin de la lecture.
type countingBody struct {
	r     io.Reader
	n     atomic.Int64
	onEOF func()
}

func (c *countingBody) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n.Add(int64(n))
	if err == io.EOF && c.onEOF != nil {
		c.onEOF()
		c.onEOF = nil
	}
	return n, err
}

func (c *countingBody) Close() error { return nil }

func withCounter(body string, onEOF func()) (*countingBody, func(*http.Request)) {
	c := &countingBody{r: strings.NewReader(body), onEOF: onEOF}
	return c, func(r *http.Request) { r.Body = c }
}

// Sans signature valide, aucun octet du corps ne doit être lu : un inconnu ne peut ni
// occuper le disque ni retenir la connexion le temps d'un envoi.
func TestBodyNotReadBeforeAuth(t *testing.T) {
	good := req{target: "prod", sha: shaA, body: "archive"}
	cases := map[string]func(q *req){
		"mauvais secret":    func(q *req) { q.key = []byte("autre-secret-autre-secret-autre!!") },
		"signature absente": func(q *req) { q.sig = "sha256=" + strings.Repeat("0", 64) },
		"horodatage périmé": func(q *req) { q.ts = now.Add(-time.Hour) },
		"trop gros":         func(q *req) { q.body = strings.Repeat("x", 2<<10) },
	}
	for name, mod := range cases {
		t.Run(name, func(t *testing.T) {
			q := good
			mod(&q)
			c, tamper := withCounter(q.body, nil)
			q.tamper = tamper
			h, dir := newHandler(t, &fakeDeployer{})
			if code, _ := do(t, h, q.build()); code < 400 {
				t.Fatalf("statut %d", code)
			}
			if c.n.Load() != 0 {
				t.Fatalf("%d octets lus avant authentification", c.n.Load())
			}
			if entries, _ := os.ReadDir(dir); len(entries) != 0 {
				t.Fatalf("fichier créé : %v", entries)
			}
		})
	}
}

func TestOneUploadAtATime(t *testing.T) {
	d := &fakeDeployer{}
	h, _ := newHandler(t, d)
	pr, pw := io.Pipe()
	first := req{target: "prod", sha: shaA, body: "archive",
		tamper: func(r *http.Request) { r.Body = pr }}.build()
	done := make(chan int)
	go func() {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, first)
		done <- w.Code
	}()
	// Write ne rend la main qu'une fois l'octet lu : le premier envoi tient alors le verrou.
	if _, err := pw.Write([]byte("a")); err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	h.ServeHTTP(w, req{target: "preview", pr: "4", sha: shaA, body: "x"}.build())
	if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") == "" {
		t.Fatalf("second envoi : %d, Retry-After %q", w.Code, w.Header().Get("Retry-After"))
	}
	// Les requêtes sans corps ne sont pas bloquées par un envoi en cours.
	if code, _ := do(t, h, req{method: http.MethodDelete, target: "preview", pr: "9"}.build()); code != 200 {
		t.Fatalf("suppression pendant un envoi : %d", code)
	}

	_, _ = pw.Write([]byte("rchive"))
	pw.Close()
	if code := <-done; code != 200 {
		t.Fatalf("premier envoi : %d", code)
	}
	// Verrou rendu : un nouvel envoi passe.
	if code, _ := do(t, h, req{target: "preview", pr: "4", sha: shaA, body: "x", ts: now.Add(time.Second)}.build()); code != 200 {
		t.Fatalf("envoi suivant : %d", code)
	}
}

func TestLedger(t *testing.T) {
	state := filepath.Join(t.TempDir(), "hook-last.json")
	mk := func() *Handler {
		return New(Config{Secret: secret, MaxBytes: 1 << 10, TempDir: t.TempDir(), StatePath: state,
			Now: func() time.Time { return now }}, &fakeDeployer{})
	}
	h := mk()
	steps := []struct {
		name   string
		q      req
		status int
	}{
		{"prod", req{target: "prod", sha: shaA, body: "x"}, 200},
		{"plus ancienne, autre contenu", req{target: "prod", sha: shaA, body: "y", ts: now.Add(-time.Second)}, 409},
		{"même seconde, autre contenu", req{target: "prod", sha: shaA, body: "y"}, 409},
		{"autre cible, même seconde", req{target: "preview", pr: "1", sha: shaA, body: "x"}, 200},
		{"autre preview, même seconde", req{target: "preview", pr: "2", sha: shaA, body: "x"}, 200},
		{"suppression de la même preview, même seconde", req{method: http.MethodDelete, target: "preview", pr: "1"}, 409},
		{"suppression plus récente", req{method: http.MethodDelete, target: "preview", pr: "1", ts: now.Add(time.Second)}, 200},
		{"service", req{target: "service", tag: "v1"}, 200},
	}
	for _, s := range steps {
		if code, out := do(t, h, s.q.build()); code != s.status {
			t.Fatalf("%s : %d, attendu %d (%v)", s.name, code, s.status, out)
		}
	}

	// Après redémarrage, la requête déjà acceptée est toujours refusée.
	h = mk()
	if code, _ := do(t, h, req{target: "prod", sha: shaA, body: "x"}.build()); code != http.StatusConflict {
		t.Fatalf("rejeu après redémarrage : %d", code)
	}
	if code, _ := do(t, h, req{target: "prod", sha: shaA, body: "x", ts: now.Add(time.Second)}.build()); code != 200 {
		t.Fatalf("requête plus récente après redémarrage : %d", code)
	}
	b, err := os.ReadFile(state)
	if err != nil || !strings.Contains(string(b), `"preview:1":`+strconv.FormatInt(now.Unix()+1, 10)) {
		t.Fatalf("registre %s (%v)", b, err)
	}
	if fi, _ := os.Stat(state); fi.Mode().Perm()&0o077 != 0 {
		t.Fatalf("registre lisible par tous : %v", fi.Mode())
	}
}

func TestLedgerCorrupt(t *testing.T) {
	state := filepath.Join(t.TempDir(), "hook-last.json")
	if err := os.WriteFile(state, []byte("{pas du json"), 0o600); err != nil {
		t.Fatal(err)
	}
	d := &fakeDeployer{}
	h := New(Config{Secret: secret, MaxBytes: 1 << 10, TempDir: t.TempDir(), StatePath: state,
		Now: func() time.Time { return now }}, d)
	code, out := do(t, h, req{target: "prod", sha: shaA, body: "x"}.build())
	if code != 500 || len(d.calls) != 0 {
		t.Fatalf("registre illisible : %d, appels %d", code, len(d.calls))
	}
	if msg := fmt.Sprint(out["error"]); strings.Contains(msg, state) || strings.Contains(msg, "json") {
		t.Fatalf("détail interne envoyé au client : %q", msg)
	}
}

// Un envoi qui finit après la fenêtre de la signature est refusé, même signé et complet.
func TestUploadMustFinishInWindow(t *testing.T) {
	clock := now
	d := &fakeDeployer{}
	dir := t.TempDir()
	h := New(Config{Secret: secret, MaxBytes: 1 << 10, TempDir: dir, Now: func() time.Time { return clock }}, d)
	_, tamper := withCounter("archive", func() { clock = clock.Add(5*time.Minute + time.Second) })
	code, _ := do(t, h, req{target: "prod", sha: shaA, body: "archive", tamper: tamper}.build())
	if code != http.StatusRequestTimeout || len(d.calls) != 0 {
		t.Fatalf("statut %d, appels %d", code, len(d.calls))
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("fichier laissé : %v", entries)
	}
}

func TestSeenExpiry(t *testing.T) {
	h, _ := newHandler(t, &fakeDeployer{})
	h.firstUse("futur", now.Add(4*time.Minute))
	h.firstUse("passé", now.Add(-4*time.Minute))
	if got := h.seen["futur"]; !got.Equal(now.Add(9 * time.Minute)) {
		t.Fatalf("futur expire à %v", got)
	}
	if got := h.seen["passé"]; !got.Equal(now.Add(5 * time.Minute)) {
		t.Fatalf("passé expire à %v", got)
	}
}

func TestMinRateReader(t *testing.T) {
	t0 := time.Unix(1000, 0)
	clock := t0
	var deadlines []time.Time
	m := &minRateReader{r: iotest.OneByteReader(strings.NewReader("0123456789")),
		set: func(d time.Time) error { deadlines = append(deadlines, d); return nil },
		now: func() time.Time { return clock }, window: 10 * time.Second, min: 4, hard: t0.Add(15 * time.Second)}
	m.start()
	buf := make([]byte, 1)
	for range 3 {
		_, _ = m.Read(buf)
	}
	if len(deadlines) != 1 || !deadlines[0].Equal(t0.Add(10*time.Second)) {
		t.Fatalf("échéance prolongée trop tôt : %v", deadlines)
	}
	clock = t0.Add(2 * time.Second)
	_, _ = m.Read(buf)
	if len(deadlines) != 2 || !deadlines[1].Equal(t0.Add(12*time.Second)) {
		t.Fatalf("échéance après 4 octets : %v", deadlines)
	}
	clock = t0.Add(8 * time.Second)
	for range 4 {
		_, _ = m.Read(buf)
	}
	if len(deadlines) != 3 || !deadlines[2].Equal(t0.Add(15*time.Second)) {
		t.Fatalf("échéance non plafonnée : %v", deadlines)
	}
}

// Un client signé mais trop lent est coupé par l'échéance glissante du vrai serveur.
func TestSlowUploadCut(t *testing.T) {
	d := &fakeDeployer{}
	h := New(Config{Secret: secret, MaxBytes: 1 << 20, TempDir: t.TempDir(),
		MinRateBytes: 1 << 10, MinRateWindow: 200 * time.Millisecond}, d)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	body := strings.Repeat("x", 4<<10)
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	f := Fields{Timestamp: ts, Method: "POST", Target: "prod", SHA: shaA,
		Length: strconv.Itoa(len(body)), BodySHA256: bodyHash(body)}
	pr, pw := io.Pipe()
	r, _ := http.NewRequest(http.MethodPost, srv.URL, pr)
	r.ContentLength = int64(len(body))
	for k, v := range map[string]string{"X-Tzama-Timestamp": ts, "X-Tzama-Target": "prod", "X-Tzama-SHA": shaA,
		"X-Tzama-Body-SHA256": f.BodySHA256, "X-Tzama-Signature": "sha256=" + hex.EncodeToString(Sign(secret, f))} {
		r.Header.Set(k, v)
	}
	go func() {
		_, _ = pw.Write([]byte(body[:10]))
		time.Sleep(3 * time.Second)
		pw.Close()
	}()
	start := time.Now()
	resp, err := http.DefaultClient.Do(r)
	if err == nil {
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusRequestTimeout {
			t.Fatalf("statut %d : %s", resp.StatusCode, b)
		}
	}
	if el := time.Since(start); el > 2*time.Second {
		t.Fatalf("connexion lente gardée %v", el)
	}
	if len(d.calls) != 0 {
		t.Fatal("envoi incomplet déployé")
	}
}

func TestInternalErrorNotExposed(t *testing.T) {
	h, _ := newHandler(t, &fakeDeployer{err: fmt.Errorf("open /srv/sites/releases/x: disque plein")})
	code, out := do(t, h, req{target: "prod", sha: shaA, body: "x"}.build())
	if code != 500 || strings.Contains(fmt.Sprint(out["error"]), "disque") || strings.Contains(fmt.Sprint(out["error"]), "/srv") {
		t.Fatalf("statut %d, réponse %v", code, out)
	}
}

func TestUnvalidatedHeadersSanitizedInLog(t *testing.T) {
	var logs bytes.Buffer
	h, _ := newHandler(t, &fakeDeployer{})
	h.cfg.Logger = newJSONLogger(&logs)
	long := strings.Repeat("A", 10<<10)
	do(t, h, req{target: "prod", sha: shaA, body: "x", tamper: func(r *http.Request) {
		r.Header["X-Tzama-Target"] = []string{"prod\x1b[31m\nfaux=1"}
		r.Header.Set("X-Tzama-Tag", long)
	}}.build())
	out := logs.String()
	if strings.Contains(out, long[:100]) || strings.Contains(out, `\u001b`) || strings.Contains(out, `\n`) {
		t.Fatalf("en-têtes bruts journalisés : %s", out)
	}
	if !strings.Contains(out, "prod?[31m?faux=1") {
		t.Fatalf("cible absente du journal : %s", out)
	}
}
