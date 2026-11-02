package contact

import (
	"container/list"
	"net"
	"sync"
	"time"
)

// defaultMaxTracked borne la mémoire du limiteur face à un flot d'adresses différentes.
const defaultMaxTracked = 100_000

// Limiter : au plus N envois par fenêtre glissante et par client.
type Limiter struct {
	n          int
	window     time.Duration
	now        func() time.Time
	maxTracked int

	mu sync.Mutex
	// Clients rangés du moins au plus récemment actif : la tête est la première à évincer
	// quand la table est pleine, et le nettoyage s'arrête au premier client encore actif.
	order *list.List
	byKey map[string]*list.Element
}

type client struct {
	key  string
	hits []time.Time
}

func NewLimiter(n int, window time.Duration, now func() time.Time) *Limiter {
	if now == nil {
		now = time.Now
	}
	return &Limiter{n: n, window: window, now: now, maxTracked: defaultMaxTracked,
		order: list.New(), byKey: map[string]*list.Element{}}
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
	e, ok := l.byKey[k]
	if !ok {
		// Table pleine : on oublie le client resté inactif le plus longtemps plutôt que de
		// refuser tout le monde.
		for l.order.Len() >= l.maxTracked {
			l.remove(l.order.Front())
		}
		e = l.order.PushBack(&client{key: k})
		l.byKey[k] = e
	}
	c := e.Value.(*client)
	c.hits = l.prune(c.hits, now)
	if len(c.hits) >= l.n {
		return false, c.hits[0].Add(l.window).Sub(now)
	}
	c.hits = append(c.hits, now)
	l.order.MoveToBack(e)
	return true, 0
}

func (l *Limiter) remove(e *list.Element) {
	l.order.Remove(e)
	delete(l.byKey, e.Value.(*client).key)
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
	now := l.now()
	for e := l.order.Front(); e != nil; e = l.order.Front() {
		c := e.Value.(*client)
		if len(c.hits) > 0 && now.Sub(c.hits[len(c.hits)-1]) < l.window {
			return
		}
		l.remove(e)
	}
}

func (l *Limiter) size() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.order.Len()
}
