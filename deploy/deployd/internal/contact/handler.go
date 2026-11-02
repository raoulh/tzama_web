// Package contact reçoit le formulaire de contact du site et l'envoie par mail à l'école.
package contact

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"mime"
	"net/http"
	"net/mail"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/raoulh/tzama_web/deploy/deployd/internal/clientip"
)

const maxBody = 32 << 10

type Config struct {
	AllowedOrigins []string
	OKPath         string
	ErrorPath      string
	TrustProxy     bool
	Now            func() time.Time
	Logger         *slog.Logger
}

type Handler struct {
	cfg     Config
	mailer  Mailer
	limiter *Limiter
}

// New : mailer nil = SMTP non configuré, le formulaire répond 503.
func New(cfg Config, mailer Mailer, limiter *Limiter) *Handler {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}
	return &Handler{cfg: cfg, mailer: mailer, limiter: limiter}
}

type reply struct {
	status int
	code   string            // erreur globale, "" si succès ou erreurs de champs
	fields map[string]string // erreurs par champ
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	isJSON := mt == "application/json"
	if !isJSON && mt != "application/x-www-form-urlencoded" {
		// Un formulaire HTML n'envoie que de l'urlencoded (pas d'enctype multipart côté site) :
		// ce cas vient d'un client programmatique, qui lit un statut plutôt qu'une redirection.
		h.respond(w, r, true, reply{status: http.StatusUnsupportedMediaType, code: "unsupported_media_type"})
		return
	}
	h.respond(w, r, isJSON, h.process(w, r, isJSON))
}

func (h *Handler) process(w http.ResponseWriter, r *http.Request, isJSON bool) reply {
	log := h.cfg.Logger.With("ip", clientip.From(r, h.cfg.TrustProxy))
	if !h.originOK(r) {
		log.Warn("contact : origine refusée", "origin", r.Header.Get("Origin"), "referer", r.Header.Get("Referer"))
		return reply{status: http.StatusForbidden, code: "forbidden_origin"}
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	var f Form
	if isJSON {
		var in map[string]any
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			return reply{status: http.StatusBadRequest, code: "bad_request"}
		}
		for _, fd := range append(slices.Clone(fields), field{name: "website"}) {
			v, _ := in[fd.name].(string)
			if fd.name == "website" {
				f.Website = v
			} else {
				*f.ptr(fd.name) = v
			}
		}
	} else {
		if err := r.ParseForm(); err != nil {
			return reply{status: http.StatusBadRequest, code: "bad_request"}
		}
		for _, fd := range fields {
			*f.ptr(fd.name) = r.PostForm.Get(fd.name)
		}
		f.Website = r.PostForm.Get("website")
	}

	if errs := f.Validate(); len(errs) > 0 {
		return reply{status: http.StatusUnprocessableEntity, code: "validation", fields: errs}
	}
	// Compté après validation : seuls les messages qui partiraient consomment le quota.
	if ok, retry := h.limiter.Allow(clientip.From(r, h.cfg.TrustProxy)); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(int(retry.Seconds())+1))
		log.Warn("contact : limite d'envois atteinte")
		return reply{status: http.StatusTooManyRequests, code: "rate_limited"}
	}
	if strings.TrimSpace(f.Website) != "" {
		// Même réponse qu'un succès : le robot n'apprend rien.
		log.Info("contact : honeypot rempli, message ignoré")
		return reply{status: http.StatusOK}
	}
	if h.mailer == nil {
		log.Error("contact : SMTP non configuré, message perdu")
		return reply{status: http.StatusServiceUnavailable, code: "unavailable"}
	}
	if err := h.mailer.Send(r.Context(), h.compose(f)); err != nil {
		log.Error("contact : envoi du mail", "err", err)
		return reply{status: http.StatusBadGateway, code: "send_failed"}
	}
	log.Info("contact : message envoyé", "subject", f.Subject)
	return reply{status: http.StatusOK}
}

func (h *Handler) originOK(r *http.Request) bool {
	if o := r.Header.Get("Origin"); o != "" {
		return slices.Contains(h.cfg.AllowedOrigins, o)
	}
	if ref := r.Header.Get("Referer"); ref != "" {
		u, err := url.Parse(ref)
		return err == nil && slices.Contains(h.cfg.AllowedOrigins, u.Scheme+"://"+u.Host)
	}
	return true
}

func (h *Handler) compose(f Form) Message {
	label := subjects[f.Subject]
	phone := f.Phone
	if phone == "" {
		phone = "non renseigné"
	}
	body := fmt.Sprintf(`Nouveau message envoyé depuis le formulaire de contact du site.

Objet : %s
Nom : %s
Email : %s
Téléphone : %s
Âge de l'enfant : %s
Rentrée visée : %s

Message :
%s

--
Reçu le %s. Répondre à ce mail écrit directement à %s.
`, label, f.Name, f.Email, phone, f.ChildAge, f.SchoolYear, f.Message,
		h.cfg.Now().Format("02/01/2006 à 15:04"), f.Email)
	return Message{
		ReplyTo: &mail.Address{Name: f.Name, Address: f.Email},
		Subject: "[Site] " + label + " – " + f.Name,
		Body:    body,
	}
}

func (h *Handler) respond(w http.ResponseWriter, r *http.Request, isJSON bool, rep reply) {
	if isJSON {
		out := map[string]any{"ok": rep.code == "" && rep.status < 300}
		if rep.fields != nil {
			out["errors"] = rep.fields
		} else if rep.code != "" {
			out["error"] = rep.code
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(rep.status)
		_ = json.NewEncoder(w).Encode(out)
		return
	}
	// Sans JS : retour sur une page du site, qui affiche le résultat.
	target := h.cfg.OKPath
	if rep.status >= 300 {
		q := url.Values{"erreur": {rep.code}}
		if rep.fields != nil {
			q.Set("champs", strings.Join(slices.Sorted(maps.Keys(rep.fields)), ","))
		}
		sep := "?"
		if strings.Contains(h.cfg.ErrorPath, "?") {
			sep = "&"
		}
		target = h.cfg.ErrorPath + sep + q.Encode()
	}
	w.Header().Set("Location", target)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusSeeOther)
}
