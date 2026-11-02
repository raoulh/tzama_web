// Package hook reçoit les déploiements signés envoyés par la CI.
package hook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// Fields : en-têtes signés d'une requête. Les champs inutilisés pour une cible sont vides
// mais restent dans la chaîne canonique, qui garde ainsi une forme fixe.
type Fields struct {
	Timestamp string
	Method    string
	Target    string
	PR        string
	SHA       string
	Tag       string
	// Length (Content-Length en décimal) et BodySHA256 (hex) engagent le corps sans qu'il
	// soit lu : la signature se vérifie avant d'accepter le moindre octet.
	Length     string
	BodySHA256 string
}

// Canonical est la chaîne signée ; le script client la reproduit à l'identique.
func (f Fields) Canonical() string {
	return strings.Join([]string{f.Timestamp, f.Method, f.Target, f.PR, f.SHA, f.Tag,
		f.Length, f.BodySHA256}, "\n") + "\n"
}

// Sign renvoie le HMAC-SHA256 de la chaîne canonique.
func Sign(secret []byte, f Fields) []byte {
	m := hmac.New(sha256.New, secret)
	m.Write([]byte(f.Canonical()))
	return m.Sum(nil)
}

// parseSignature décode « sha256=<64 hex> ».
func parseSignature(h string) ([]byte, bool) {
	hexSig, ok := strings.CutPrefix(h, "sha256=")
	if !ok || len(hexSig) != 2*sha256.Size {
		return nil, false
	}
	sig, err := hex.DecodeString(hexSig)
	return sig, err == nil
}
