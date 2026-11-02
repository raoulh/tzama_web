package contact

import (
	"net/mail"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Codes d'erreur stables, repris par le formulaire du site pour afficher ses messages.
const (
	CodeRequired      = "required"
	CodeTooLong       = "too_long"
	CodeInvalid       = "invalid"
	CodeInvalidEmail  = "invalid_email"
	CodeInvalidPhone  = "invalid_phone"
	CodeInvalidChoice = "invalid_choice"
)

var subjects = map[string]string{
	"visite":   "Demande de visite",
	"question": "Question",
	"autre":    "Autre demande",
}

type field struct {
	name      string
	max       int
	required  bool
	multiline bool
}

var fields = []field{
	{"name", 100, true, false},
	{"email", 254, true, false},
	{"phone", 30, false, false},
	{"child_age", 60, true, false},
	{"school_year", 60, true, false},
	{"subject", 20, true, false},
	{"message", 5000, true, true},
}

type Form struct {
	Name, Email, Phone, ChildAge, SchoolYear, Subject, Message string
	// Honeypot : champ caché que seuls les robots remplissent.
	Website string
}

func (f *Form) ptr(name string) *string {
	switch name {
	case "name":
		return &f.Name
	case "email":
		return &f.Email
	case "phone":
		return &f.Phone
	case "child_age":
		return &f.ChildAge
	case "school_year":
		return &f.SchoolYear
	case "subject":
		return &f.Subject
	case "message":
		return &f.Message
	}
	return nil
}

// validChars refuse l'UTF-8 invalide et les caractères de contrôle ; CR/LF ne sont admis que
// dans le message, seul champ qui ne finit jamais dans un en-tête.
func validChars(s string, multiline bool) bool {
	if !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if multiline && (r == '\n' || r == '\r' || r == '\t') {
			continue
		}
		if unicode.IsControl(r) || r == '\u2028' || r == '\u2029' {
			return false
		}
	}
	return true
}

// Validate normalise le formulaire (espaces de bord) et renvoie les erreurs par champ.
func (f *Form) Validate() map[string]string {
	errs := map[string]string{}
	for _, fd := range fields {
		p := f.ptr(fd.name)
		*p = strings.TrimSpace(*p)
		v := *p
		switch {
		case !validChars(v, fd.multiline):
			errs[fd.name] = CodeInvalid
		case v == "":
			if fd.required {
				errs[fd.name] = CodeRequired
			}
		case utf8.RuneCountInString(v) > fd.max:
			errs[fd.name] = CodeTooLong
		}
	}
	if _, ok := errs["email"]; !ok {
		a, err := mail.ParseAddress(f.Email)
		if err != nil || a.Address != f.Email || a.Name != "" || strings.ContainsAny(f.Email, " <>\"") ||
			!strings.Contains(f.Email[strings.LastIndexByte(f.Email, '@')+1:], ".") {
			errs["email"] = CodeInvalidEmail
		}
	}
	if _, ok := errs["phone"]; !ok && f.Phone != "" {
		if strings.Trim(f.Phone, "0123456789 +().-") != "" || strings.IndexFunc(f.Phone, unicode.IsDigit) < 0 {
			errs["phone"] = CodeInvalidPhone
		}
	}
	if _, ok := errs["subject"]; !ok {
		if _, known := subjects[f.Subject]; !known {
			errs["subject"] = CodeInvalidChoice
		}
	}
	return errs
}
