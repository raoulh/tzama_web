package contact

import (
	"net"
	"sync"
	"time"
)

// maxTracked borne la mémoire du limiteur face à un flot d'adresses différentes.
const maxTracked = 100_000

// Limiter : au plus N envois par fenêtre glissante et par client.
type Limiter struct {
	n      int
	window time.Duration
	now    func() time.Time

	mu   sync.Mutex
	hits map[string][]time.Time
}

func NewLimiter(n int, window time.Duration, now func() time.Time) *Limiter {
	if now == nil {
		now = time.Now
	}
	return &Limiter{n: n, window: window, now: now, hits: map[string][]time.Time{}}
}

// key regroupe une IPv6 par /64 : un seul client en détient souvent un préfixe entier.
func key(ip string) string {
	p := net.ParseIP(ip)
	if p == nil || p.To4() != nil {
		return ip
	}
	return p.Mask(net.CIDRMask(64, 128)).String() + "/64"
}

// Allow enregistre une tentative ; sinon renvoie false et le délai avant la prochaine.
func (l *Limiter) Allow(ip string) (bool, time.Duration) {
	k := key(ip)
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	recent := l.prune(l.hits[k], now)
	if len(recent) >= l.n {
		l.hits[k] = recent
		return false, recent[0].Add(l.window).Sub(now)
	}
	if _, ok := l.hits[k]; !ok && len(l.hits) >= maxTracked {
		l.cleanupLocked(now)
		if len(l.hits) >= maxTracked {
			return false, l.window
		}
	}
	l.hits[k] = append(recent, now)
	return true, 0
}

func (l *Limiter) prune(ts []time.Time, now time.Time) []time.Time {
	i := 0
	for i < len(ts) && now.Sub(ts[i]) >= l.window {
		i++
	}
	return ts[i:]
}

// Cleanup oublie les clients sans tentative récente ; appelé périodiquement.
func (l *Limiter) Cleanup() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.cleanupLocked(l.now())
}

func (l *Limiter) cleanupLocked(now time.Time) {
	for k, ts := range l.hits {
		if r := l.prune(ts, now); len(r) == 0 {
			delete(l.hits, k)
		} else {
			l.hits[k] = r
		}
	}
}

func (l *Limiter) size() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.hits)
}
