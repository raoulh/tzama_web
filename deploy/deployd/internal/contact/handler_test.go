package contact

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

const origin = "https://v2.tzama.fr"

type fakeMailer struct {
	mu   sync.Mutex
	sent []Message
	err  error
}

func (f *fakeMailer) Send(_ context.Context, m Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.sent = append(f.sent, m)
	return nil
}

func validForm() map[string]string {
	return map[string]string{
		"name": "Zoé Lévêque", "email": "parent@famille.example", "phone": "+33 6 12 34 56 78",
		"child_age": "4 ans", "school_year": "Septembre 2027", "subject": "visite",
		"message": "Bonjour,\nnous aimerions visiter l'école.",
	}
}

type env struct {
	h       *Handler
	mailer  *fakeMailer
	limiter *Limiter
	clock   *time.Time
}

func newEnv(t *testing.T, trustProxy bool) *env {
	t.Helper()
	now := time.Date(2026, 11, 2, 9, 30, 0, 0, time.UTC)
	e := &env{mailer: &fakeMailer{}, clock: &now}
	e.limiter = NewLimiter(5, 10*time.Minute, func() time.Time { return *e.clock })
	e.h = New(Config{AllowedOrigins: []string{origin, "https://test.tzama.fr"}, OKPath: "/contact/merci/",
		ErrorPath: "/contact/", TrustProxy: trustProxy, Now: func() time.Time { return *e.clock }},
		e.mailer, e.limiter)
	return e
}

func jsonReq(fields map[string]string) *http.Request {
	b, _ := json.Marshal(fields)
	r := httptest.NewRequest(http.MethodPost, "/api/contact", strings.NewReader(string(b)))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", origin)
	r.RemoteAddr = "192.0.2.10:5555"
	return r
}

func formReq(fields map[string]string) *http.Request {
	v := url.Values{}
	for k, val := range fields {
		v.Set(k, val)
	}
	r := httptest.NewRequest(http.MethodPost, "/api/contact", strings.NewReader(v.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Origin", origin)
	r.RemoteAddr = "192.0.2.10:5555"
	return r
}

func serve(h http.Handler, r *http.Request) (*httptest.ResponseRecorder, map[string]any) {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w, out
}

func TestContactJSONSuccess(t *testing.T) {
	e := newEnv(t, false)
	w, out := serve(e.h, jsonReq(validForm()))
	if w.Code != 200 || out["ok"] != true {
		t.Fatalf("statut %d %v", w.Code, out)
	}
	if len(e.mailer.sent) != 1 {
		t.Fatalf("envois %d", len(e.mailer.sent))
	}
	m := e.mailer.sent[0]
	if m.ReplyTo.Address != "parent@famille.example" || m.ReplyTo.Name != "Zoé Lévêque" {
		t.Fatalf("Reply-To %+v", m.ReplyTo)
	}
	if m.Subject != "[Site] Demande de visite – Zoé Lévêque" {
		t.Fatalf("sujet %q", m.Subject)
	}
	for _, want := range []string{"Zoé Lévêque", "parent@famille.example", "+33 6 12 34 56 78", "4 ans",
		"Septembre 2027", "Demande de visite", "nous aimerions visiter l'école.", "02/11/2026"} {
		if !strings.Contains(m.Body, want) {
			t.Errorf("corps sans %q", want)
		}
	}
}

func TestContactFormSuccessRedirects(t *testing.T) {
	e := newEnv(t, false)
	f := validForm()
	delete(f, "phone")
	w, _ := serve(e.h, formReq(f))
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/contact/merci/" {
		t.Fatalf("statut %d vers %q", w.Code, w.Header().Get("Location"))
	}
	if len(e.mailer.sent) != 1 || !strings.Contains(e.mailer.sent[0].Body, "non renseigné") {
		t.Fatal("message sans téléphone non envoyé")
	}
}

func TestContactValidation(t *testing.T) {
	cases := []struct {
		name  string
		edit  func(f map[string]string)
		field string
		code  string
	}{
		{"nom absent", func(f map[string]string) { delete(f, "name") }, "name", CodeRequired},
		{"nom blanc", func(f map[string]string) { f["name"] = "   " }, "name", CodeRequired},
		{"nom trop long", func(f map[string]string) { f["name"] = strings.Repeat("é", 101) }, "name", CodeTooLong},
		{"nom avec retour ligne", func(f map[string]string) { f["name"] = "A\r\nBcc: x@evil.example" }, "name", CodeInvalid},
		{"nom avec séparateur unicode", func(f map[string]string) { f["name"] = "A B" }, "name", CodeInvalid},
		{"email absent", func(f map[string]string) { f["email"] = "" }, "email", CodeRequired},
		{"email sans domaine", func(f map[string]string) { f["email"] = "parent@" }, "email", CodeInvalidEmail},
		{"email sans point", func(f map[string]string) { f["email"] = "parent@localhost" }, "email", CodeInvalidEmail},
		{"email avec nom", func(f map[string]string) { f["email"] = "Zoé <parent@famille.example>" }, "email", CodeInvalidEmail},
		{"deux emails", func(f map[string]string) { f["email"] = "a@b.example, c@d.example" }, "email", CodeInvalidEmail},
		{"téléphone lettres", func(f map[string]string) { f["phone"] = "06 12 AB" }, "phone", CodeInvalidPhone},
		{"téléphone sans chiffre", func(f map[string]string) { f["phone"] = "+()" }, "phone", CodeInvalidPhone},
		{"sujet inconnu", func(f map[string]string) { f["subject"] = "spam" }, "subject", CodeInvalidChoice},
		{"rentrée absente", func(f map[string]string) { delete(f, "school_year") }, "school_year", CodeRequired},
		{"message trop long", func(f map[string]string) { f["message"] = strings.Repeat("a", 5001) }, "message", CodeTooLong},
		{"message caractère de contrôle", func(f map[string]string) { f["message"] = "a\x00b" }, "message", CodeInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t, false)
			f := validForm()
			tc.edit(f)
			w, out := serve(e.h, jsonReq(f))
			errs, _ := out["errors"].(map[string]any)
			if w.Code != http.StatusUnprocessableEntity || out["ok"] != false || errs[tc.field] != tc.code {
				t.Fatalf("statut %d, réponse %v", w.Code, out)
			}
			if len(errs) != 1 {
				t.Fatalf("erreurs en trop : %v", errs)
			}

			w, _ = serve(e.h, formReq(f))
			loc, _ := url.Parse(w.Header().Get("Location"))
			if w.Code != http.StatusSeeOther || loc.Path != "/contact/" || loc.Query().Get("erreur") != "validation" ||
				loc.Query().Get("champs") != tc.field {
				t.Fatalf("formulaire : %d vers %q", w.Code, w.Header().Get("Location"))
			}
			if len(e.mailer.sent) != 0 {
				t.Fatal("message invalide envoyé")
			}
		})
	}
}

// JSON remplace l'UTF-8 invalide par U+FFFD : seul le formulaire transmet les octets bruts.
func TestContactInvalidUTF8(t *testing.T) {
	e := newEnv(t, false)
	f := validForm()
	f["child_age"] = "\xff"
	w, _ := serve(e.h, formReq(f))
	loc, _ := url.Parse(w.Header().Get("Location"))
	if loc.Query().Get("champs") != "child_age" || len(e.mailer.sent) != 0 {
		t.Fatalf("%d vers %q", w.Code, w.Header().Get("Location"))
	}
}

func TestContactMultilineMessageOK(t *testing.T) {
	e := newEnv(t, false)
	f := validForm()
	f["message"] = "ligne 1\r\nligne 2\n\tindentée"
	if w, out := serve(e.h, jsonReq(f)); w.Code != 200 {
		t.Fatalf("statut %d %v", w.Code, out)
	}
}

func TestContactHoneypot(t *testing.T) {
	e := newEnv(t, false)
	f := validForm()
	f["website"] = "http://spam.example"
	w, out := serve(e.h, jsonReq(f))
	if w.Code != 200 || out["ok"] != true {
		t.Fatalf("statut %d %v", w.Code, out)
	}
	w, _ = serve(e.h, formReq(f))
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/contact/merci/" {
		t.Fatalf("formulaire : %d %q", w.Code, w.Header().Get("Location"))
	}
	if len(e.mailer.sent) != 0 {
		t.Fatal("honeypot rempli mais message envoyé")
	}
}

func TestContactRateLimit(t *testing.T) {
	e := newEnv(t, false)
	var ok, limited int
	for range 20 {
		w, out := serve(e.h, jsonReq(validForm()))
		switch w.Code {
		case 200:
			ok++
		case http.StatusTooManyRequests:
			limited++
			if out["error"] != "rate_limited" || w.Header().Get("Retry-After") == "" {
				t.Fatalf("réponse 429 %v, Retry-After %q", out, w.Header().Get("Retry-After"))
			}
		default:
			t.Fatalf("statut %d", w.Code)
		}
	}
	if ok != 5 || limited != 15 || len(e.mailer.sent) != 5 {
		t.Fatalf("%d acceptés, %d limités, %d envoyés", ok, limited, len(e.mailer.sent))
	}
	// Une autre adresse n'est pas touchée.
	r := jsonReq(validForm())
	r.RemoteAddr = "192.0.2.11:5555"
	if w, _ := serve(e.h, r); w.Code != 200 {
		t.Fatalf("autre client : %d", w.Code)
	}
	// Formulaire sans JS : redirection avec le code.
	w, _ := serve(e.h, formReq(validForm()))
	if !strings.Contains(w.Header().Get("Location"), "erreur=rate_limited") {
		t.Fatalf("redirection %q", w.Header().Get("Location"))
	}
	// La fenêtre est glissante : passé 10 minutes, le client peut renvoyer.
	*e.clock = e.clock.Add(10*time.Minute + time.Second)
	if w, _ := serve(e.h, jsonReq(validForm())); w.Code != 200 {
		t.Fatalf("après la fenêtre : %d", w.Code)
	}
}

func TestContactInvalidDoesNotConsumeQuota(t *testing.T) {
	e := newEnv(t, false)
	bad := validForm()
	bad["email"] = "x"
	for range 10 {
		serve(e.h, jsonReq(bad))
	}
	if w, _ := serve(e.h, jsonReq(validForm())); w.Code != 200 {
		t.Fatalf("statut %d", w.Code)
	}
}

func TestContactTrustProxy(t *testing.T) {
	// Sans TRUST_PROXY, X-Real-IP est ignoré : un client ne peut pas changer d'identité.
	e := newEnv(t, false)
	for i := range 6 {
		r := jsonReq(validForm())
		r.Header.Set("X-Real-IP", "198.51.100."+string(rune('1'+i)))
		w, _ := serve(e.h, r)
		if i == 5 && w.Code != http.StatusTooManyRequests {
			t.Fatalf("X-Real-IP pris en compte sans TRUST_PROXY : %d", w.Code)
		}
	}
	// Derrière Caddy, toutes les requêtes viennent du proxy : X-Real-IP distingue les clients.
	e = newEnv(t, true)
	for i := range 6 {
		r := jsonReq(validForm())
		r.Header.Set("X-Real-IP", "198.51.100."+string(rune('1'+i)))
		if w, _ := serve(e.h, r); w.Code != 200 {
			t.Fatalf("client %d limité à tort : %d", i, w.Code)
		}
	}
}

func TestContactOrigin(t *testing.T) {
	cases := map[string]struct {
		hdr  map[string]string
		code int
	}{
		"origine autre":         {map[string]string{"Origin": "https://evil.example"}, 403},
		"origine voisine":       {map[string]string{"Origin": "https://v2.tzama.fr.evil.example"}, 403},
		"origine null":          {map[string]string{"Origin": "null"}, 403},
		"referer autre":         {map[string]string{"Origin": "", "Referer": "https://evil.example/contact/"}, 403},
		"referer autorisé":      {map[string]string{"Origin": "", "Referer": "https://test.tzama.fr/contact/"}, 200},
		"seconde origine":       {map[string]string{"Origin": "https://test.tzama.fr"}, 200},
		"ni origine ni referer": {map[string]string{"Origin": ""}, 200},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t, false)
			r := jsonReq(validForm())
			for k, v := range tc.hdr {
				if v == "" {
					r.Header.Del(k)
				} else {
					r.Header.Set(k, v)
				}
			}
			w, out := serve(e.h, r)
			if w.Code != tc.code {
				t.Fatalf("statut %d, attendu %d", w.Code, tc.code)
			}
			if tc.code == 403 && (out["error"] != "forbidden_origin" || len(e.mailer.sent) != 0) {
				t.Fatalf("réponse %v, envois %d", out, len(e.mailer.sent))
			}
		})
	}
}

func TestContactErrors(t *testing.T) {
	e := newEnv(t, false)
	r := jsonReq(validForm())
	r.Header.Set("Content-Type", "text/plain")
	if w, out := serve(e.h, r); w.Code != http.StatusUnsupportedMediaType || out["error"] != "unsupported_media_type" {
		t.Fatalf("type de contenu : %d %v", w.Code, out)
	}
	r = httptest.NewRequest(http.MethodPost, "/api/contact", strings.NewReader("{"))
	r.Header.Set("Content-Type", "application/json; charset=utf-8")
	if w, _ := serve(e.h, r); w.Code != http.StatusBadRequest {
		t.Fatalf("JSON invalide : %d", w.Code)
	}
	f := validForm()
	f["message"] = strings.Repeat("a", 40<<10)
	if w, _ := serve(e.h, jsonReq(f)); w.Code != http.StatusBadRequest {
		t.Fatalf("corps trop gros : %d", w.Code)
	}

	e.mailer.err = errors.New("smtp en panne")
	w, out := serve(e.h, jsonReq(validForm()))
	if w.Code != http.StatusBadGateway || out["error"] != "send_failed" {
		t.Fatalf("panne SMTP : %d %v", w.Code, out)
	}
	w, _ = serve(e.h, formReq(validForm()))
	if !strings.HasSuffix(w.Header().Get("Location"), "/contact/?erreur=send_failed") {
		t.Fatalf("redirection %q", w.Header().Get("Location"))
	}

	h := New(Config{AllowedOrigins: []string{origin}}, nil, NewLimiter(5, time.Minute, nil))
	if w, out := serve(h, jsonReq(validForm())); w.Code != http.StatusServiceUnavailable || out["error"] != "unavailable" {
		t.Fatalf("sans SMTP : %d %v", w.Code, out)
	}
}

func TestLimiter(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	l := NewLimiter(2, time.Minute, func() time.Time { return now })
	allow := func(ip string) bool { ok, _ := l.Allow(ip); return ok }
	if !allow("2001:db8:1:2::1") || !allow("2001:db8:1:2:ffff::9") {
		t.Fatal("premiers envois refusés")
	}
	ok, retry := l.Allow("2001:db8:1:2:abcd::1")
	if ok || retry <= 0 || retry > time.Minute {
		t.Fatalf("même /64 non limité : %v %v", ok, retry)
	}
	if !allow("2001:db8:1:3::1") {
		t.Fatal("autre /64 limité")
	}
	if !allow("192.0.2.1") || !allow("192.0.2.1") || allow("192.0.2.1") || !allow("192.0.2.2") {
		t.Fatal("IPv4 mal comptée")
	}
	now = now.Add(30 * time.Second)
	if allow("192.0.2.1") {
		t.Fatal("fenêtre non glissante")
	}
	now = now.Add(31 * time.Second)
	if !allow("192.0.2.1") {
		t.Fatal("fenêtre non expirée")
	}
	now = now.Add(2 * time.Minute)
	l.Cleanup()
	if l.size() != 0 {
		t.Fatalf("%d clients gardés après nettoyage", l.size())
	}
}
