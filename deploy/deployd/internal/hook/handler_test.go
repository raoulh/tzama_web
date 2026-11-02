package hook

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/raoulh/tzama_web/deploy/deployd/internal/sites"
)

var (
	secret = []byte("0123456789abcdef0123456789abcdef")
	shaA   = strings.Repeat("a", 40)
	now    = time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
)

type call struct {
	op, sha, tag string
	pr           int
	body         string
}

type fakeDeployer struct {
	calls []call
	err   error
}

func (f *fakeDeployer) DeployProd(sha string, archive io.Reader) (sites.ProdResult, error) {
	b, _ := io.ReadAll(archive)
	f.calls = append(f.calls, call{op: "prod", sha: sha, body: string(b)})
	return sites.ProdResult{SHA: sha, Previous: "prev"}, f.err
}

func (f *fakeDeployer) DeployPreview(pr int, sha string, archive io.Reader) error {
	b, _ := io.ReadAll(archive)
	f.calls = append(f.calls, call{op: "preview", pr: pr, sha: sha, body: string(b)})
	return f.err
}

func (f *fakeDeployer) DeletePreview(pr int) (bool, error) {
	f.calls = append(f.calls, call{op: "delete", pr: pr})
	return true, f.err
}

func (f *fakeDeployer) SetServiceTag(tag string) error {
	f.calls = append(f.calls, call{op: "service", tag: tag})
	return f.err
}

type req struct {
	method, target, pr, sha, tag, body string
	ts                                 time.Time
	key                                []byte
	sig                                string // forcée si non vide
	tamper                             func(r *http.Request)
}

func sign(key []byte, f Fields, body string) string {
	m := NewMAC(key, f)
	m.Write([]byte(body))
	return "sha256=" + hex.EncodeToString(m.Sum(nil))
}

func (q req) build() *http.Request {
	if q.method == "" {
		q.method = http.MethodPost
	}
	if q.ts.IsZero() {
		q.ts = now
	}
	if q.key == nil {
		q.key = secret
	}
	f := Fields{Timestamp: strconv.FormatInt(q.ts.Unix(), 10), Method: q.method, Target: q.target,
		PR: q.pr, SHA: q.sha, Tag: q.tag}
	r := httptest.NewRequest(q.method, "/hooks/deploy", strings.NewReader(q.body))
	set := func(k, v string) {
		if v != "" {
			r.Header.Set(k, v)
		}
	}
	set("X-Tzama-Timestamp", f.Timestamp)
	set("X-Tzama-Target", q.target)
	set("X-Tzama-PR", q.pr)
	set("X-Tzama-SHA", q.sha)
	set("X-Tzama-Tag", q.tag)
	sig := q.sig
	if sig == "" {
		sig = sign(q.key, f, q.body)
	}
	r.Header.Set("X-Tzama-Signature", sig)
	if q.tamper != nil {
		q.tamper(r)
	}
	return r
}

func newHandler(t *testing.T, d Deployer) (*Handler, string) {
	t.Helper()
	dir := t.TempDir()
	return New(Config{Secret: secret, MaxBytes: 1 << 10, TempDir: dir, Now: func() time.Time { return now }}, d), dir
}

func do(t *testing.T, h http.Handler, r *http.Request) (int, map[string]any) {
	t.Helper()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("réponse non JSON : %q", w.Body.String())
	}
	return w.Code, out
}

func TestCanonical(t *testing.T) {
	f := Fields{Timestamp: "1700000000", Method: "POST", Target: "preview", PR: "12", SHA: shaA}
	if got, want := f.Canonical(), "1700000000\nPOST\npreview\n12\n"+shaA+"\n\n"; got != want {
		t.Fatalf("chaîne canonique %q, attendu %q", got, want)
	}
	// Vecteur fixe partagé avec le script client et la doc.
	if got := sign([]byte("secret-de-test"), f, "corps"); got != "sha256="+knownVector {
		t.Fatalf("signature %s", got)
	}
}

// Calculé avec openssl (voir README) : la Go et le script client signent la même chaîne.
const knownVector = "6f96e336e45115e20c15cee6ee6696eb133144a63065fd6cfa0f13b1e06efe2d"

func TestValidRequests(t *testing.T) {
	cases := []struct {
		name string
		q    req
		want call
		resp map[string]any
	}{
		{name: "prod", q: req{target: "prod", sha: shaA, body: "archive"},
			want: call{op: "prod", sha: shaA, body: "archive"},
			resp: map[string]any{"target": "prod", "sha": shaA, "previous": "prev", "reused": false}},
		{name: "preview", q: req{target: "preview", pr: "42", sha: shaA, body: "archive"},
			want: call{op: "preview", pr: 42, sha: shaA, body: "archive"},
			resp: map[string]any{"target": "preview", "pr": float64(42), "path": "/pr-42/"}},
		{name: "suppression", q: req{method: http.MethodDelete, target: "preview", pr: "42"},
			want: call{op: "delete", pr: 42}, resp: map[string]any{"deleted": true}},
		{name: "service", q: req{target: "service", tag: "v1.4.2", sha: shaA},
			want: call{op: "service", tag: "v1.4.2"}, resp: map[string]any{"target": "service", "tag": "v1.4.2"}},
		{name: "horodatage limite", q: req{target: "prod", sha: shaA, body: "x", ts: now.Add(-299 * time.Second)},
			want: call{op: "prod", sha: shaA, body: "x"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := &fakeDeployer{}
			h, tmp := newHandler(t, d)
			code, out := do(t, h, tc.q.build())
			if code != http.StatusOK || out["ok"] != true {
				t.Fatalf("statut %d, réponse %v", code, out)
			}
			if len(d.calls) != 1 || d.calls[0] != tc.want {
				t.Fatalf("appels %+v, attendu %+v", d.calls, tc.want)
			}
			for k, v := range tc.resp {
				if out[k] != v {
					t.Errorf("%s = %v, attendu %v", k, out[k], v)
				}
			}
			entries, _ := os.ReadDir(tmp)
			if len(entries) != 0 {
				t.Fatalf("fichier d'envoi laissé : %v", entries)
			}
		})
	}
}

func TestRejectedRequests(t *testing.T) {
	good := req{target: "prod", sha: shaA, body: "archive"}
	with := func(f func(q *req)) req { q := good; f(&q); return q }
	cases := []struct {
		name   string
		q      req
		status int
	}{
		{"mauvais secret", with(func(q *req) { q.key = []byte("autre-secret-autre-secret-autre!!") }), 401},
		{"signature absente", with(func(q *req) { q.sig = "x"; q.tamper = func(r *http.Request) { r.Header.Del("X-Tzama-Signature") } }), 401},
		{"signature mal formée", with(func(q *req) { q.sig = "sha1=abcd" }), 401},
		{"signature tronquée", with(func(q *req) { q.sig = "sha256=" + strings.Repeat("0", 63) }), 401},
		{"horodatage périmé", with(func(q *req) { q.ts = now.Add(-301 * time.Second) }), 401},
		{"horodatage futur", with(func(q *req) { q.ts = now.Add(301 * time.Second) }), 401},
		{"corps modifié", with(func(q *req) {
			q.tamper = func(r *http.Request) { r.Body = io.NopCloser(strings.NewReader("archivf")) }
		}), 401},
		{"sha modifié après signature", with(func(q *req) {
			q.tamper = func(r *http.Request) { r.Header.Set("X-Tzama-SHA", strings.Repeat("b", 40)) }
		}), 401},
		{"cible modifiée après signature", with(func(q *req) {
			q.pr = "3"
			q.target = "preview"
			q.tamper = func(r *http.Request) { r.Header.Set("X-Tzama-PR", "4") }
		}), 401},
		{"méthode modifiée", req{method: http.MethodDelete, target: "preview", pr: "3",
			tamper: func(r *http.Request) { r.Method = http.MethodPost }}, 400},
		{"cible inconnue", with(func(q *req) { q.target = "staging" }), 400},
		{"sha court", with(func(q *req) { q.sha = "abc" }), 400},
		{"sha majuscules", with(func(q *req) { q.sha = strings.Repeat("A", 40) }), 400},
		{"prod avec PR", with(func(q *req) { q.pr = "3" }), 400},
		{"preview sans PR", with(func(q *req) { q.target = "preview" }), 400},
		{"PR non numérique", req{target: "preview", pr: "3a", sha: shaA, body: "x"}, 400},
		{"PR zéro", req{target: "preview", pr: "0", sha: shaA, body: "x"}, 400},
		{"tag invalide", req{target: "service", tag: "v1;rm"}, 400},
		{"service sans tag", req{target: "service"}, 400},
		{"suppression prod", req{method: http.MethodDelete, target: "prod", sha: shaA}, 400},
		{"suppression avec sha", req{method: http.MethodDelete, target: "preview", pr: "3", sha: shaA}, 400},
		{"prod sans corps", with(func(q *req) { q.body = "" }), 400},
		{"service avec corps", req{target: "service", tag: "v1", body: "x"}, 400},
		{"suppression avec corps", req{method: http.MethodDelete, target: "preview", pr: "3", body: "x"}, 400},
		{"horodatage non numérique", with(func(q *req) {
			q.tamper = func(r *http.Request) { r.Header.Set("X-Tzama-Timestamp", "demain") }
		}), 400},
		{"corps trop gros", with(func(q *req) { q.body = strings.Repeat("x", 2<<10) }), 413},
		{"corps trop gros sans longueur", with(func(q *req) {
			q.body = strings.Repeat("x", 2<<10)
			q.tamper = func(r *http.Request) { r.ContentLength = -1 }
		}), 413},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := &fakeDeployer{}
			h, _ := newHandler(t, d)
			code, out := do(t, h, tc.q.build())
			if code != tc.status || out["ok"] != false {
				t.Fatalf("statut %d (attendu %d), réponse %v", code, tc.status, out)
			}
			if len(d.calls) != 0 {
				t.Fatalf("requête refusée mais transmise : %+v", d.calls)
			}
		})
	}
}

func TestReplay(t *testing.T) {
	d := &fakeDeployer{}
	h, _ := newHandler(t, d)
	q := req{target: "prod", sha: shaA, body: "archive"}
	if code, _ := do(t, h, q.build()); code != 200 {
		t.Fatalf("premier envoi : %d", code)
	}
	if code, _ := do(t, h, q.build()); code != http.StatusConflict {
		t.Fatalf("rejeu : %d", code)
	}
	if len(d.calls) != 1 {
		t.Fatalf("appels %d", len(d.calls))
	}
	// Même contenu une seconde plus tard : nouvelle signature, accepté.
	q.ts = now.Add(time.Second)
	if code, _ := do(t, h, q.build()); code != 200 {
		t.Fatalf("nouvel envoi : %d", code)
	}
}

func TestNoSecretDisablesHook(t *testing.T) {
	d := &fakeDeployer{}
	h := New(Config{MaxBytes: 1 << 10, TempDir: t.TempDir(), Now: func() time.Time { return now }}, d)
	// Avec un secret vide, une signature calculée avec la clé vide ne doit pas passer.
	code, _ := do(t, h, req{target: "prod", sha: shaA, body: "x", key: []byte{}}.build())
	if code != http.StatusServiceUnavailable || len(d.calls) != 0 {
		t.Fatalf("statut %d, appels %d", code, len(d.calls))
	}
}

func TestDeployerErrors(t *testing.T) {
	cases := []struct {
		err    error
		status int
	}{
		{fmt.Errorf("x: %w", sites.ErrUnsafeArchive), 400},
		{fmt.Errorf("x: %w", sites.ErrNotFound), 404},
		{fmt.Errorf("disque plein"), 500},
	}
	for _, tc := range cases {
		h, _ := newHandler(t, &fakeDeployer{err: tc.err})
		if code, _ := do(t, h, req{target: "prod", sha: shaA, body: "x"}.build()); code != tc.status {
			t.Errorf("%v : statut %d, attendu %d", tc.err, code, tc.status)
		}
	}
}

func TestWithRealStore(t *testing.T) {
	root := t.TempDir()
	sitesDir, stateDir := root+"/sites", root+"/state"
	for _, d := range []string{sitesDir, stateDir} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	store := sites.New(sites.Config{SitesDir: sitesDir, StateDir: stateDir, Keep: 5,
		Limits: sites.Limits{MaxFiles: 100, MaxBytes: 1 << 20}})
	if err := store.Init(); err != nil {
		t.Fatal(err)
	}
	h := New(Config{Secret: secret, MaxBytes: 1 << 20, TempDir: sitesDir + "/.tmp",
		Now: func() time.Time { return now }}, store)

	// Archive signée mais dangereuse : refusée, prod n'existe toujours pas.
	evil := tarGz(t, map[string]string{"index.html": "x", "../evil": "y"})
	if code, _ := do(t, h, req{target: "prod", sha: shaA, body: evil}.build()); code != 400 {
		t.Fatalf("archive dangereuse : %d", code)
	}
	if _, err := os.Lstat(root + "/evil"); err == nil {
		t.Fatal("fichier écrit hors du dossier")
	}
	ok := tarGz(t, map[string]string{"index.html": "<h1>ok</h1>"})
	if code, out := do(t, h, req{target: "prod", sha: shaA, body: ok, ts: now.Add(time.Second)}.build()); code != 200 {
		t.Fatalf("déploiement : %d %v", code, out)
	}
	b, err := os.ReadFile(sitesDir + "/prod/index.html")
	if err != nil || string(b) != "<h1>ok</h1>" {
		t.Fatalf("prod : %q %v", b, err)
	}
	entries, _ := os.ReadDir(sitesDir + "/.tmp")
	if len(entries) != 0 {
		t.Fatalf(".tmp non vide : %v", entries)
	}
}

func TestSignatureNotLogged(t *testing.T) {
	var logs bytes.Buffer
	d := &fakeDeployer{}
	h, _ := newHandler(t, d)
	h.cfg.Logger = newJSONLogger(&logs)
	do(t, h, req{target: "prod", sha: shaA, body: "x"}.build())
	do(t, h, req{target: "prod", sha: shaA, body: "x", key: []byte("mauvais-secret-mauvais-secret-xx")}.build())
	if strings.Contains(logs.String(), string(secret)) || strings.Contains(logs.String(), "sha256=") {
		t.Fatalf("journal trop bavard : %s", logs.String())
	}
	if !strings.Contains(logs.String(), "signature invalide") {
		t.Fatalf("refus non journalisé : %s", logs.String())
	}
}
