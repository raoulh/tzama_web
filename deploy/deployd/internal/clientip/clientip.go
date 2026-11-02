// Package clientip détermine l'adresse du visiteur derrière Caddy.
package clientip

import (
	"net"
	"net/http"
	"strings"
)

// From renvoie l'IP du client. X-Real-IP n'est lu que si trustProxy : sans proxy de confiance
// devant le service, n'importe qui pourrait le forger pour contourner le rate-limit.
func From(r *http.Request, trustProxy bool) string {
	if trustProxy {
		if v := strings.TrimSpace(r.Header.Get("X-Real-IP")); v != "" {
			if ip := net.ParseIP(v); ip != nil {
				return ip.String()
			}
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.String()
	}
	return host
}
