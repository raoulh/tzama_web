// Package oauth relaie la connexion GitHub de Sveltia/Decap CMS : l'échange code → jeton
// exige le client secret, qui ne peut pas vivre dans la page du CMS.
package oauth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

const (
	stateCookie = "tzama_oauth_state"
	stateTTL    = 10 * time.Minute
)

// Scopes demandables par le CMS ; tout autre valeur est ignorée au profit de "repo".
var allowedScopes = []string{"repo", "public_repo", "repo,user", "public_repo,user"}

type Config struct {
	ClientID     string
	ClientSecret string
	// GitHubURL : https://github.com en production, un faux serveur en test.
	GitHubURL      string
	RedirectURL    string
	AllowedOrigins []string
	HTTPClient     *http.Client
	Logger         *slog.Logger
}

type Handler struct {
	cfg Config
}

func New(cfg Config) *Handler {
	if cfg.GitHubURL == "" {
		cfg.GitHubURL = "https://github.com"
	}
	cfg.GitHubURL = strings.TrimSuffix(cfg.GitHubURL, "/")
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 15 * time.Second}
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}
	return &Handler{cfg: cfg}
}

func (h *Handler) configured() bool { return h.cfg.ClientID != "" && h.cfg.ClientSecret != "" }

func (h *Handler) originAllowed(origin string) bool {
	return slices.Contains(h.cfg.AllowedOrigins, origin)
}

// refererOrigin renvoie « scheme://host » d'un en-tête Referer, "" s'il est absent ou illisible.
func refererOrigin(ref string) string {
	u, err := url.Parse(ref)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

// Auth démarre la connexion : GET /auth?provider=github&site_id=…&scope=…
func (h *Handler) Auth(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if p := q.Get("provider"); p != "" && p != "github" {
		http.Error(w, "fournisseur non pris en charge", http.StatusBadRequest)
		return
	}
	if !h.configured() {
		h.cfg.Logger.Error("connexion CMS demandée mais GITHUB_CLIENT_ID/GITHUB_CLIENT_SECRET absents")
		http.Error(w, "connexion indisponible", http.StatusServiceUnavailable)
		return
	}
	// Garde-fou côté serveur ; la vraie protection est la vérification d'origine du postMessage.
	if ref := r.Header.Get("Referer"); ref != "" {
		if o := refererOrigin(ref); o != "" && !h.originAllowed(o) {
			h.cfg.Logger.Warn("connexion CMS depuis une origine refusée", "origin", o)
			http.Error(w, "origine non autorisée", http.StatusForbidden)
			return
		}
	}
	if site := q.Get("site_id"); site != "" && !h.siteAllowed(site) {
		h.cfg.Logger.Warn("connexion CMS pour un site refusé", "site_id", site)
		http.Error(w, "site non autorisé", http.StatusForbidden)
		return
	}
	scope := q.Get("scope")
	if !slices.Contains(allowedScopes, scope) {
		scope = "repo"
	}
	state, err := randomToken(32)
	if err != nil {
		http.Error(w, "erreur interne", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: stateCookie, Value: state, Path: "/callback",
		MaxAge: int(stateTTL.Seconds()), HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode,
	})
	v := url.Values{"client_id": {h.cfg.ClientID}, "scope": {scope}, "state": {state}}
	if h.cfg.RedirectURL != "" {
		v.Set("redirect_uri", h.cfg.RedirectURL)
	}
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, h.cfg.GitHubURL+"/login/oauth/authorize?"+v.Encode(), http.StatusFound)
}

// siteAllowed compare site_id (nom d'hôte envoyé par Decap) aux origines autorisées.
func (h *Handler) siteAllowed(site string) bool {
	for _, o := range h.cfg.AllowedOrigins {
		if u, err := url.Parse(o); err == nil && strings.EqualFold(u.Host, site) {
			return true
		}
	}
	return false
}

// Callback reçoit le retour de GitHub, échange le code et rend la page de poignée de main.
func (h *Handler) Callback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	http.SetCookie(w, &http.Cookie{
		Name: stateCookie, Value: "", Path: "/callback", MaxAge: -1,
		HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode,
	})
	if e := q.Get("error"); e != "" {
		h.cfg.Logger.Warn("connexion CMS refusée par GitHub", "error", e)
		h.render(w, http.StatusForbidden, "", "GitHub a refusé la connexion : "+e)
		return
	}
	c, err := r.Cookie(stateCookie)
	state := q.Get("state")
	if err != nil || c.Value == "" || state == "" || subtle.ConstantTimeCompare([]byte(c.Value), []byte(state)) != 1 {
		h.cfg.Logger.Warn("connexion CMS : state invalide")
		h.render(w, http.StatusBadRequest, "", "Session de connexion invalide ou expirée, recommencez.")
		return
	}
	code := q.Get("code")
	if code == "" || len(code) > 512 {
		h.render(w, http.StatusBadRequest, "", "Code d'autorisation manquant.")
		return
	}
	token, err := h.exchange(r.Context(), code)
	if err != nil {
		h.cfg.Logger.Error("connexion CMS : échange du code", "err", err)
		h.render(w, http.StatusBadGateway, "", "Échange avec GitHub impossible.")
		return
	}
	h.cfg.Logger.Info("connexion CMS réussie")
	h.render(w, http.StatusOK, token, "")
}

func (h *Handler) exchange(ctx context.Context, code string) (string, error) {
	form := url.Values{"client_id": {h.cfg.ClientID}, "client_secret": {h.cfg.ClientSecret}, "code": {code}}
	if h.cfg.RedirectURL != "" {
		form.Set("redirect_uri", h.cfg.RedirectURL)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.cfg.GitHubURL+"/login/oauth/access_token",
		strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := h.cfg.HTTPClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("statut %d", resp.StatusCode)
	}
	var out struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&out); err != nil {
		return "", err
	}
	if out.Error != "" {
		return "", fmt.Errorf("github: %s", out.Error)
	}
	if out.AccessToken == "" {
		return "", fmt.Errorf("réponse sans jeton")
	}
	return out.AccessToken, nil
}

func randomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// La page annonce « authorizing:github » aux origines autorisées, attend la réponse du CMS et
// n'envoie le résultat qu'à l'origine exacte de cette réponse, si elle est autorisée.
var pageTmpl = template.Must(template.New("cb").Parse(`<!DOCTYPE html>
<html lang="fr">
<head><meta charset="utf-8"><meta name="robots" content="noindex"><title>Connexion</title></head>
<body>
<p>{{if .Error}}{{.Error}}{{else}}Connexion réussie, cette fenêtre va se fermer.{{end}}</p>
<script nonce="{{.Nonce}}">
(function () {
  var allowed = {{.Origins}};
  var message = {{.Message}};
  if (!window.opener) { return; }
  window.addEventListener("message", function receive(e) {
    if (allowed.indexOf(e.origin) === -1) { return; }
    window.removeEventListener("message", receive);
    window.opener.postMessage(message, e.origin);
  });
  allowed.forEach(function (o) { window.opener.postMessage("authorizing:github", o); });
})();
</script>
</body>
</html>
`))

func (h *Handler) render(w http.ResponseWriter, status int, token, errMsg string) {
	var msg string
	if errMsg == "" {
		b, _ := json.Marshal(map[string]string{"token": token, "provider": "github"})
		msg = "authorization:github:success:" + string(b)
	} else {
		b, _ := json.Marshal(map[string]string{"message": errMsg, "provider": "github"})
		msg = "authorization:github:error:" + string(b)
	}
	nonce, err := randomToken(18)
	if err != nil {
		http.Error(w, "erreur interne", http.StatusInternalServerError)
		return
	}
	origins := h.cfg.AllowedOrigins
	if origins == nil {
		origins = []string{}
	}
	hd := w.Header()
	hd.Set("Content-Type", "text/html; charset=utf-8")
	hd.Set("Content-Security-Policy", "default-src 'none'; script-src 'nonce-"+nonce+"'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
	hd.Set("Cache-Control", "no-store")
	hd.Set("Referrer-Policy", "no-referrer")
	w.WriteHeader(status)
	_ = pageTmpl.Execute(w, map[string]any{"Nonce": nonce, "Origins": origins, "Message": msg, "Error": errMsg})
}
