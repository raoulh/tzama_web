// Package hook reçoit les déploiements signés envoyés par la CI.
package hook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"hash"
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
}

// Canonical est la chaîne signée avant le corps brut ; le script client la reproduit à
// l'identique.
func (f Fields) Canonical() string {
	return strings.Join([]string{f.Timestamp, f.Method, f.Target, f.PR, f.SHA, f.Tag}, "\n") + "\n"
}

// NewMAC prépare le HMAC-SHA256 d'une requête : il reste à y écrire le corps.
func NewMAC(secret []byte, f Fields) hash.Hash {
	m := hmac.New(sha256.New, secret)
	m.Write([]byte(f.Canonical()))
	return m
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
