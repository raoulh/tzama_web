package oauth

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
)

const site = "https://test.tzama.fr"

type fakeGitHub struct {
	mu     sync.Mutex
	forms  []url.Values
	accept string
	status int
	reply  string
}

func (g *fakeGitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.URL.Path != "/login/oauth/access_token" {
		http.NotFound(w, r)
		return
	}
	_ = r.ParseForm()
	g.mu.Lock()
	g.forms = append(g.forms, r.PostForm)
	g.accept = r.Header.Get("Accept")
	g.mu.Unlock()
	if g.status != 0 {
		w.WriteHeader(g.status)
	}
	_, _ = io.WriteString(w, g.reply)
}

func setup(t *testing.T, gh *fakeGitHub) *Handler {
	t.Helper()
	srv := httptest.NewServer(gh)
	t.Cleanup(srv.Close)
	return New(Config{ClientID: "cid", ClientSecret: "csecret", GitHubURL: srv.URL,
		AllowedOrigins: []string{site, "https://v2.tzama.fr"}, HTTPClient: srv.Client()})
}

func auth(t *testing.T, h *Handler, query string, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/auth?"+query, nil)
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.Auth(w, r)
	return w
}

func callback(t *testing.T, h *Handler, query, cookie string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/callback?"+query, nil)
	if cookie != "" {
		r.AddCookie(&http.Cookie{Name: stateCookie, Value: cookie})
	}
	w := httptest.NewRecorder()
	h.Callback(w, r)
	return w
}

func stateFrom(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	for _, c := range w.Result().Cookies() {
		if c.Name == stateCookie {
			if !c.HttpOnly || !c.Secure || c.Path != "/callback" || c.SameSite != http.SameSiteLaxMode || c.MaxAge <= 0 {
				t.Fatalf("cookie de state mal protégé : %+v", c)
			}
			return c.Value
		}
	}
	t.Fatal("pas de cookie de state")
	return ""
}

func TestAuthRedirect(t *testing.T) {
	h := setup(t, &fakeGitHub{})
	w := auth(t, h, "provider=github&site_id=test.tzama.fr&scope=repo", map[string]string{"Referer": site + "/admin/"})
	if w.Code != http.StatusFound {
		t.Fatalf("statut %d", w.Code)
	}
	loc, err := url.Parse(w.Header().Get("Location"))
	if err != nil || loc.Path != "/login/oauth/authorize" {
		t.Fatalf("redirection %q", w.Header().Get("Location"))
	}
	q := loc.Query()
	state := stateFrom(t, w)
	if q.Get("client_id") != "cid" || q.Get("scope") != "repo" || q.Get("state") != state || len(state) < 40 {
		t.Fatalf("paramètres %v (state cookie %q)", q, state)
	}
	if q.Has("client_secret") {
		t.Fatal("secret dans l'URL")
	}
	if other := stateFrom(t, auth(t, h, "", nil)); other == state {
		t.Fatal("state réutilisé")
	}
}

func TestAuthScope(t *testing.T) {
	h := setup(t, &fakeGitHub{})
	for in, want := range map[string]string{"public_repo": "public_repo", "repo,user": "repo,user",
		"admin:org": "repo", "": "repo", "repo delete_repo": "repo"} {
		loc, _ := url.Parse(auth(t, h, url.Values{"scope": {in}}.Encode(), nil).Header().Get("Location"))
		if got := loc.Query().Get("scope"); got != want {
			t.Errorf("scope %q -> %q, attendu %q", in, got, want)
		}
	}
}

func TestAuthRefused(t *testing.T) {
	h := setup(t, &fakeGitHub{})
	cases := []struct {
		name, query string
		hdr         map[string]string
		status      int
	}{
		{"origine refusée", "provider=github", map[string]string{"Referer": "https://evil.example/admin/"}, 403},
		{"origine voisine", "provider=github", map[string]string{"Referer": "https://test.tzama.fr.evil.example/"}, 403},
		{"http au lieu de https", "provider=github", map[string]string{"Referer": "http://test.tzama.fr/admin/"}, 403},
		{"site refusé", "provider=github&site_id=evil.example", nil, 403},
		{"fournisseur inconnu", "provider=gitlab", nil, 400},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := auth(t, h, tc.query, tc.hdr)
			if w.Code != tc.status {
				t.Fatalf("statut %d, attendu %d", w.Code, tc.status)
			}
			if len(w.Result().Cookies()) != 0 {
				t.Fatal("cookie posé malgré le refus")
			}
		})
	}
}

func TestUnconfigured(t *testing.T) {
	h := New(Config{AllowedOrigins: []string{site}})
	if w := auth(t, h, "", nil); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("statut %d", w.Code)
	}
}

func TestCallbackInvalidState(t *testing.T) {
	gh := &fakeGitHub{reply: `{"access_token":"gho_x"}`}
	h := setup(t, gh)
	cases := map[string]struct{ query, cookie string }{
		"sans cookie":       {"code=c&state=abc", ""},
		"state différent":   {"code=c&state=abc", "abd"},
		"state absent":      {"code=c", "abc"},
		"cookie vide":       {"code=c&state=", ""},
		"state plus long":   {"code=c&state=abcd", "abc"},
		"state sans cookie": {"code=c&state=", "x"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			w := callback(t, h, tc.query, tc.cookie)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("statut %d", w.Code)
			}
			if strings.Contains(w.Body.String(), "gho_x") || !strings.Contains(w.Body.String(), "authorization:github:error:") {
				t.Fatalf("page %s", w.Body.String())
			}
		})
	}
	if len(gh.forms) != 0 {
		t.Fatal("code échangé malgré un state invalide")
	}
}

var nonceRe = regexp.MustCompile(`script-src 'nonce-([A-Za-z0-9_-]+)'`)

func TestCallbackSuccess(t *testing.T) {
	gh := &fakeGitHub{reply: `{"access_token":"gho_tok</script><b>","token_type":"bearer"}`}
	h := setup(t, gh)
	state := stateFrom(t, auth(t, h, "", nil))
	w := callback(t, h, url.Values{"code": {"le-code"}, "state": {state}}.Encode(), state)
	if w.Code != http.StatusOK {
		t.Fatalf("statut %d : %s", w.Code, w.Body.String())
	}
	if len(gh.forms) != 1 {
		t.Fatalf("échanges %d", len(gh.forms))
	}
	f := gh.forms[0]
	if f.Get("client_id") != "cid" || f.Get("client_secret") != "csecret" || f.Get("code") != "le-code" {
		t.Fatalf("formulaire envoyé à GitHub %v", f)
	}
	if gh.accept != "application/json" {
		t.Fatalf("Accept %q", gh.accept)
	}

	body := w.Body.String()
	csp := w.Header().Get("Content-Security-Policy")
	m := nonceRe.FindStringSubmatch(csp)
	if m == nil || !strings.Contains(body, `<script nonce="`+m[1]+`">`) {
		t.Fatalf("nonce absent ou différent : CSP %q", csp)
	}
	if strings.Contains(csp, "unsafe-inline") || !strings.Contains(csp, "frame-ancestors 'none'") {
		t.Fatalf("CSP %q", csp)
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("page avec jeton mise en cache")
	}
	// Le jeton est rendu dans du JS : il ne doit pas pouvoir fermer la balise script.
	if strings.Contains(body, "</script><b>") {
		t.Fatal("jeton injecté tel quel dans la page")
	}
	msg := extractJSString(t, body, "var message = ")
	want, _ := json.Marshal(map[string]string{"provider": "github", "token": "gho_tok</script><b>"})
	if msg != "authorization:github:success:"+string(want) {
		t.Fatalf("message %q", msg)
	}
	origins := extractJSString(t, body, "var allowed = ")
	if origins != `["https://test.tzama.fr","https://v2.tzama.fr"]` {
		t.Fatalf("origines %q", origins)
	}
	if strings.Contains(body, `"*"`) || strings.Contains(body, "'*'") {
		t.Fatal("postMessage vers *")
	}
	if !strings.Contains(body, "allowed.indexOf(e.origin) === -1") {
		t.Fatal("origine du message non vérifiée")
	}
	// Cookie effacé après usage.
	cleared := false
	for _, c := range w.Result().Cookies() {
		cleared = cleared || (c.Name == stateCookie && c.MaxAge < 0)
	}
	if !cleared {
		t.Fatal("cookie de state non effacé")
	}
}

// extractJSString lit la valeur littérale JS après prefix (chaîne ou tableau, jusqu'au « ; »)
// et la décode comme du JSON, ce que html/template produit en contexte script.
func extractJSString(t *testing.T, body, prefix string) string {
	t.Helper()
	i := strings.Index(body, prefix)
	if i < 0 {
		t.Fatalf("%q absent", prefix)
	}
	rest := body[i+len(prefix):]
	lit := rest[:strings.Index(rest, ";\n")]
	var s string
	if err := json.Unmarshal([]byte(lit), &s); err == nil {
		return s
	}
	var arr []string
	if err := json.Unmarshal([]byte(lit), &arr); err != nil {
		t.Fatalf("littéral %q : %v", lit, err)
	}
	b, _ := json.Marshal(arr)
	return string(b)
}

func TestCallbackGitHubErrors(t *testing.T) {
	cases := map[string]*fakeGitHub{
		"code expiré":      {reply: `{"error":"bad_verification_code"}`},
		"statut 500":       {status: 500, reply: "oops"},
		"réponse vide":     {reply: `{}`},
		"réponse non JSON": {reply: `<html>`},
	}
	for name, gh := range cases {
		t.Run(name, func(t *testing.T) {
			h := setup(t, gh)
			state := stateFrom(t, auth(t, h, "", nil))
			w := callback(t, h, url.Values{"code": {"c"}, "state": {state}}.Encode(), state)
			if w.Code != http.StatusBadGateway || !strings.Contains(w.Body.String(), "authorization:github:error:") {
				t.Fatalf("statut %d : %s", w.Code, w.Body.String())
			}
		})
	}
	h := setup(t, &fakeGitHub{})
	w := callback(t, h, "error=access_denied&state=x", "x")
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "access_denied") {
		t.Fatalf("refus GitHub : %d", w.Code)
	}
}
