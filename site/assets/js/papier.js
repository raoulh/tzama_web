// Arrivée des éléments scotchés (.reveal, .titre) et brise des photos, comme sur l'accueil.
// Décoratif : sans ce script la page reste complète et immobile ; prefers-reduced-motion est géré en CSS.
export function papier(photoSelector) {
  if (!('IntersectionObserver' in window)) return;
  var replay = CSS.supports('animation-timeline: view()');
  var rnd = function (a, b) { return a + Math.random() * (b - a); };

  // Repli quand view() manque : is-in lance la même animation, une seule fois.
  if (!replay) {
    var rio = new IntersectionObserver(function (entries) {
      entries.forEach(function (e) { if (e.isIntersecting) { e.target.classList.add('is-in'); rio.unobserve(e.target); } });
    }, { rootMargin: '0px 0px -12% 0px' });
    document.querySelectorAll('.reveal, .titre').forEach(function (el) { rio.observe(el); });
  }

  // Brise posée quand la photo est collée (55 % visible avec view(), fin de « coller » sinon) ;
  // portraits : à peine perceptible et départs décalés, pour ne pas bouger en rangée.
  var photos = [].slice.call(document.querySelectorAll(photoSelector));
  if (!photos.length) return;
  function start(el) {
    el.classList.add('is-swaying');
    clearTimeout(el.__swayTimer); el.__swayTimer = setTimeout(function () { el.classList.remove('is-swaying'); }, 4000);
  }
  photos.forEach(function (el) {
    var portrait = !!el.querySelector('.portrait');
    el.dataset.sway = portrait ? 1 : 1 + Math.floor(Math.random() * 3);
    el.style.setProperty('--sway-amp', (portrait ? rnd(.4, .6) : rnd(1, 3)).toFixed(2) + 'deg');
    el.style.setProperty('--sway-dur', (portrait ? rnd(1.3, 1.7) : rnd(1.2, 2)).toFixed(2) + 's');
    el.style.setProperty('--sway-delay', (portrait ? rnd(0, .6) : rnd(0, .3)).toFixed(2) + 's');
    el.style.setProperty('--sway-dir', Math.random() < .5 ? -1 : 1);
    el.__swayArmed = true;
    if (!replay) el.addEventListener('animationend', function (e) { if (e.target === el && e.animationName === 'coller' && el.__swayArmed) { el.__swayArmed = false; start(el); } });
  });
  document.addEventListener('animationend', function (e) {
    if (!/^sway-/.test(e.animationName)) return;
    var el = e.target.closest('.is-swaying');
    if (el) { clearTimeout(el.__swayTimer); el.classList.remove('is-swaying'); }
  });
  if (!replay) return;
  var sio = new IntersectionObserver(function (es) {
    es.forEach(function (e) {
      var el = e.target;
      if (e.isIntersecting && e.intersectionRatio >= .55 && el.__swayArmed) { el.__swayArmed = false; start(el); }
      else if (!e.isIntersecting) {
        // sortie par le bas : réarmée ; par le haut : pas de brise en revenant
        el.__swayArmed = e.boundingClientRect.top > 0;
        if (el.__swayArmed) { clearTimeout(el.__swayTimer); el.classList.remove('is-swaying'); }
      }
    });
  }, { threshold: [0, .55] });
  photos.forEach(function (el) { sio.observe(el); });
}
