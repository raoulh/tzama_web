// deployd : déploiement du site, connexion du CMS et formulaire de contact.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
	_ "time/tzdata" // l'image distroless n'a pas de base de fuseaux

	"github.com/raoulh/tzama_web/deploy/deployd/internal/contact"
	"github.com/raoulh/tzama_web/deploy/deployd/internal/hook"
	"github.com/raoulh/tzama_web/deploy/deployd/internal/oauth"
	"github.com/raoulh/tzama_web/deploy/deployd/internal/sites"
)

const usage = `usage : deployd [serve | rollback [sha] | releases | healthcheck]
  serve           lance le service (défaut)
  rollback [sha]  remet la prod sur sha, ou sur la release précédente
  releases        liste les releases de prod (* = en ligne)
  healthcheck     interroge /healthz, code de sortie 0 si le service répond
`

func main() {
	args := os.Args[1:]
	cmd := "serve"
	if len(args) > 0 {
		cmd, args = args[0], args[1:]
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg, err := loadConfig(os.Getenv)
	if err != nil {
		fmt.Fprintln(os.Stderr, "configuration invalide :", err)
		os.Exit(2)
	}
	var code int
	switch {
	case cmd == "serve" && len(args) == 0:
		code = serve(cfg, logger)
	case cmd == "rollback" && len(args) <= 1:
		sha := ""
		if len(args) == 1 {
			sha = args[0]
		}
		code = rollback(cfg, logger, sha, os.Stdout)
	case cmd == "releases" && len(args) == 0:
		code = listReleases(cfg, logger, os.Stdout)
	case cmd == "healthcheck" && len(args) == 0:
		code = healthcheck(cfg)
	default:
		fmt.Fprint(os.Stderr, usage)
		code = 2
	}
	os.Exit(code)
}

func newStore(cfg config, logger *slog.Logger) *sites.Store {
	loc, err := time.LoadLocation("Europe/Paris")
	if err != nil {
		loc = time.UTC
	}
	return sites.New(sites.Config{
		SitesDir: cfg.SitesDir,
		StateDir: cfg.StateDir,
		Keep:     cfg.ReleasesKeep,
		Limits:   sites.Limits{MaxFiles: cfg.HookMaxFiles, MaxBytes: cfg.HookMaxUnpacked},
		Location: loc,
		Logger:   logger,
	})
}

// securityHeaders : valeurs par défaut pour les réponses JSON et texte ; la page OAuth pose sa
// propre CSP avant d'écrire.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

func newMux(cfg config, logger *slog.Logger, store *sites.Store, limiter *contact.Limiter) http.Handler {
	hk := hook.New(hook.Config{
		Secret:     cfg.HookSecret,
		MaxBytes:   cfg.HookMaxBytes,
		TempDir:    filepath.Join(cfg.SitesDir, ".tmp"),
		StatePath:  filepath.Join(cfg.StateDir, "hook-last.json"),
		UploadTime: cfg.HookUploadTimeout,
		TrustProxy: cfg.TrustProxy,
		Logger:     logger.With("component", "hook"),
	}, store)
	oa := oauth.New(oauth.Config{
		ClientID:       cfg.GitHubClientID,
		ClientSecret:   cfg.GitHubClientSecret,
		GitHubURL:      cfg.GitHubURL,
		RedirectURL:    cfg.OAuthRedirectURL,
		AllowedOrigins: cfg.OAuthOrigins,
		Logger:         logger.With("component", "oauth"),
	})
	var mailer contact.Mailer
	if cfg.SMTPEnabled {
		mailer = contact.NewSMTPMailer(cfg.SMTP)
	}
	ct := contact.New(contact.Config{
		AllowedOrigins: cfg.ContactOrigins,
		OKPath:         cfg.ContactOK,
		ErrorPath:      cfg.ContactError,
		TrustProxy:     cfg.TrustProxy,
		Logger:         logger.With("component", "contact"),
	}, mailer, limiter)

	mux := http.NewServeMux()
	mux.Handle("POST /hooks/deploy", hk)
	mux.Handle("DELETE /hooks/deploy", hk)
	mux.HandleFunc("GET /auth", oa.Auth)
	mux.HandleFunc("GET /callback", oa.Callback)
	mux.Handle("POST /api/contact", ct)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, "ok")
	})
	return securityHeaders(mux)
}

func serve(cfg config, logger *slog.Logger) int {
	// Les fichiers publiés doivent rester lisibles par Caddy quel que soit l'umask hérité.
	syscall.Umask(0o022)
	store := newStore(cfg, logger.With("component", "sites"))
	if err := store.Init(); err != nil {
		// Le formulaire et la connexion CMS restent utiles même si le volume des sites est cassé.
		logger.Error("initialisation des dossiers de sites", "err", err)
	}
	if len(cfg.HookSecret) == 0 {
		logger.Error("HOOK_SECRET absent : déploiements désactivés")
	}
	if cfg.GitHubClientID == "" || cfg.GitHubClientSecret == "" {
		logger.Error("GITHUB_CLIENT_ID ou GITHUB_CLIENT_SECRET absent : connexion du CMS désactivée")
	}
	if !cfg.SMTPEnabled {
		logger.Error("SMTP_HOST absent : formulaire de contact désactivé (503)")
	} else if cfg.SMTP.SkipVerify {
		logger.Warn("SMTP_TLS_SKIP_VERIFY=true : certificat du serveur SMTP non vérifié (chiffré, mais sans protection contre l'usurpation)",
			"smtp_host", cfg.SMTP.Host)
	}
	if len(cfg.OAuthOrigins) == 0 {
		logger.Error("OAUTH_ALLOWED_ORIGINS vide : connexion du CMS impossible")
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	limiter := contact.NewLimiter(cfg.ContactLimit, cfg.ContactWindow, nil)
	go func() {
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				limiter.Cleanup()
			}
		}
	}()

	// Délais courts pour tout le monde ; le hook les allonge pour ses propres envois.
	srv := &http.Server{
		Addr:              cfg.listenAddr(),
		Handler:           newMux(cfg, logger, store, limiter),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       90 * time.Second,
		MaxHeaderBytes:    32 << 10,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	logger.Info("deployd démarré", "addr", srv.Addr)
	select {
	case err := <-errc:
		logger.Error("serveur arrêté", "err", err)
		return 1
	case <-ctx.Done():
	}
	logger.Info("arrêt demandé, fin des requêtes en cours")
	shutCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := srv.Shutdown(shutCtx); err != nil {
		logger.Error("arrêt", "err", err)
		return 1
	}
	if err := <-errc; err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("serveur", "err", err)
		return 1
	}
	return 0
}

func rollback(cfg config, logger *slog.Logger, sha string, out io.Writer) int {
	store := newStore(cfg, logger.With("component", "sites"))
	from, to, err := store.Rollback(sha)
	if err != nil {
		fmt.Fprintln(os.Stderr, "rollback impossible :", err)
		return 1
	}
	logger.Info("rollback de la prod", "from", from, "to", to)
	fmt.Fprintf(out, "prod : %s -> %s\n", from, to)
	return 0
}

func listReleases(cfg config, logger *slog.Logger, out io.Writer) int {
	store := newStore(cfg, logger.With("component", "sites"))
	rels, err := store.Releases()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	cur, _ := store.Current()
	for _, r := range rels {
		mark := " "
		if r == cur {
			mark = "*"
		}
		fmt.Fprintf(out, "%s %s\n", mark, r)
	}
	return 0
}

func healthcheck(cfg config) int {
	c := http.Client{Timeout: 3 * time.Second}
	resp, err := c.Get(fmt.Sprintf("http://127.0.0.1:%d/healthz", cfg.Port))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintln(os.Stderr, "statut", resp.StatusCode)
		return 1
	}
	return 0
}
