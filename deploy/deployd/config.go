package main

import (
	"errors"
	"fmt"
	"net/mail"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/raoulh/tzama_web/deploy/deployd/internal/contact"
)

type config struct {
	Port       int
	SitesDir   string
	StateDir   string
	TrustProxy bool

	HookSecret        []byte
	HookMaxBytes      int64
	HookMaxFiles      int
	HookMaxUnpacked   int64
	ReleasesKeep      int
	HookUploadTimeout time.Duration

	GitHubClientID     string
	GitHubClientSecret string
	GitHubURL          string
	OAuthRedirectURL   string
	OAuthOrigins       []string
	ContactOrigins     []string

	SMTP          contact.SMTPConfig
	SMTPEnabled   bool
	ContactOK     string
	ContactError  string
	ContactLimit  int
	ContactWindow time.Duration
}

type envFunc func(string) string

func loadConfig(getenv envFunc) (config, error) {
	var errs []error
	get := func(k, def string) string {
		if v := strings.TrimSpace(getenv(k)); v != "" {
			return v
		}
		return def
	}
	num := func(k string, def int64, min int64) int64 {
		v := get(k, "")
		if v == "" {
			return def
		}
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < min {
			errs = append(errs, fmt.Errorf("%s : entier ≥ %d attendu", k, min))
			return def
		}
		return n
	}
	dur := func(k string, def time.Duration) time.Duration {
		v := get(k, "")
		if v == "" {
			return def
		}
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			errs = append(errs, fmt.Errorf("%s : durée attendue (ex. 10m)", k))
			return def
		}
		return d
	}
	boolean := func(k string) bool {
		switch strings.ToLower(get(k, "false")) {
		case "1", "true", "yes", "oui":
			return true
		case "0", "false", "no", "non":
			return false
		}
		errs = append(errs, fmt.Errorf("%s : true ou false attendu", k))
		return false
	}

	c := config{
		Port:              int(num("PORT", 8080, 1)),
		SitesDir:          get("SITES_DIR", "/srv/sites"),
		StateDir:          get("STATE_DIR", "/state"),
		TrustProxy:        boolean("TRUST_PROXY"),
		HookSecret:        []byte(getenv("HOOK_SECRET")),
		HookMaxBytes:      num("HOOK_MAX_BYTES", 512<<20, 1),
		HookMaxFiles:      int(num("HOOK_MAX_FILES", 50_000, 1)),
		HookMaxUnpacked:   num("HOOK_MAX_UNPACKED_BYTES", 2<<30, 1),
		ReleasesKeep:      int(num("RELEASES_KEEP", 5, 1)),
		HookUploadTimeout: dur("HOOK_UPLOAD_TIMEOUT", 5*time.Minute),

		GitHubClientID:     get("GITHUB_CLIENT_ID", ""),
		GitHubClientSecret: getenv("GITHUB_CLIENT_SECRET"),
		GitHubURL:          get("GITHUB_URL", "https://github.com"),
		OAuthRedirectURL:   get("OAUTH_REDIRECT_URL", ""),

		ContactOK:     get("CONTACT_OK_PATH", "/contact/merci/"),
		ContactError:  get("CONTACT_ERROR_PATH", "/contact/"),
		ContactLimit:  int(num("CONTACT_RATE_LIMIT", 5, 1)),
		ContactWindow: dur("CONTACT_RATE_WINDOW", 10*time.Minute),
	}
	if c.Port > 65535 {
		errs = append(errs, errors.New("PORT invalide"))
	}
	if n := len(c.HookSecret); n > 0 && n < 32 {
		errs = append(errs, errors.New("HOOK_SECRET : 32 caractères minimum (openssl rand -hex 32)"))
	}
	// Deux listes : test.tzama.fr sert toutes les PR sur une seule origine, une preview ne doit
	// donc jamais pouvoir recevoir le jeton GitHub du CMS.
	if getenv("ALLOWED_ORIGINS") != "" {
		errs = append(errs, errors.New("ALLOWED_ORIGINS remplacée par OAUTH_ALLOWED_ORIGINS et CONTACT_ALLOWED_ORIGINS"))
	}
	origins := func(k, def string) []string {
		var out []string
		for o := range strings.SplitSeq(get(k, def), ",") {
			o = strings.TrimSpace(o)
			if o == "" {
				continue
			}
			u, err := url.Parse(o)
			if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.Path != "" || u.RawQuery != "" {
				errs = append(errs, fmt.Errorf("%s : origine invalide %q (forme https://hote)", k, o))
				continue
			}
			out = append(out, o)
		}
		return out
	}
	c.OAuthOrigins = origins("OAUTH_ALLOWED_ORIGINS", "https://v2.tzama.fr")
	c.ContactOrigins = origins("CONTACT_ALLOWED_ORIGINS", "https://v2.tzama.fr,https://test.tzama.fr")
	for k, p := range map[string]string{"CONTACT_OK_PATH": c.ContactOK, "CONTACT_ERROR_PATH": c.ContactError} {
		// Chemin local uniquement : « //hote » ou « /\hote » seraient des redirections ouvertes.
		if !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") || strings.ContainsAny(p, "\\\r\n") {
			errs = append(errs, fmt.Errorf("%s : chemin commençant par un seul / attendu", k))
		}
	}

	c.SMTP = contact.SMTPConfig{
		Host:     get("SMTP_HOST", ""),
		Username: get("SMTP_USERNAME", ""),
		Password: getenv("SMTP_PASSWORD"),
		From:     get("SMTP_FROM", ""),
		FromName: get("SMTP_FROM_NAME", "Formulaire Tzama"),
		To:       get("CONTACT_TO", ""),
	}
	port := int(num("SMTP_PORT", 587, 1))
	c.SMTP.Port = port
	switch mode := strings.ToLower(get("SMTP_TLS", "")); mode {
	case "":
		c.SMTP.TLS = contact.TLSStartTLS
		if port == 465 {
			c.SMTP.TLS = contact.TLSImplicit
		}
	case "starttls", "tls", "none":
		c.SMTP.TLS = contact.TLSMode(mode)
	default:
		errs = append(errs, errors.New("SMTP_TLS : starttls, tls ou none"))
	}
	c.SMTP.SkipVerify = boolean("SMTP_TLS_SKIP_VERIFY")
	if c.SMTP.SkipVerify && c.SMTP.TLS == contact.TLSNone {
		errs = append(errs, errors.New("SMTP_TLS_SKIP_VERIFY sans objet avec SMTP_TLS=none"))
	}
	if strings.ContainsAny(c.SMTP.FromName, "\r\n") {
		errs = append(errs, errors.New("SMTP_FROM_NAME : retour à la ligne interdit"))
	}
	if c.SMTP.Host != "" {
		c.SMTPEnabled = true
		for k, v := range map[string]string{"SMTP_FROM": c.SMTP.From, "CONTACT_TO": c.SMTP.To} {
			if a, err := mail.ParseAddress(v); err != nil || a.Address != v {
				errs = append(errs, fmt.Errorf("%s : adresse email seule attendue", k))
			}
		}
	}
	return c, errors.Join(errs...)
}

func (c config) listenAddr() string { return ":" + strconv.Itoa(c.Port) }
