// Trait de feutre des titres (trait.css) : tracé une seule fois, quand le titre entre dans l'écran.
// Sans IntersectionObserver ou en mouvement réduit, tous les traits sont posés d'emblée.
export function traits() {
  var titres = document.querySelectorAll('.titre');
  if (!('IntersectionObserver' in window) || matchMedia('(prefers-reduced-motion: reduce)').matches) {
    titres.forEach(function (t) { t.classList.add('is-in'); });
    return;
  }
  var io = new IntersectionObserver(function (entries) {
    entries.forEach(function (e) {
      if (e.isIntersecting) { e.target.classList.add('is-in'); io.unobserve(e.target); }
    });
  }, { rootMargin: '0px 0px -12% 0px' });
  titres.forEach(function (t) { io.observe(t); });
}
