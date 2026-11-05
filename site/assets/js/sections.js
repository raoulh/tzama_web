import { papier } from './papier.js';
import { traits } from './trait.js';

(function () {
  papier('.photo.reveal, .carte__img.reveal, .trombi li.reveal');

  // Lecteur YouTube chargé au clic seulement : aucune requête vers YouTube avant.
  document.querySelectorAll('[data-video]').forEach(function (btn) {
    btn.addEventListener('click', function () {
      var frame = document.createElement('iframe');
      frame.src = 'https://www.youtube-nocookie.com/embed/' + encodeURIComponent(btn.dataset.video) + '?autoplay=1';
      frame.title = btn.dataset.title;
      frame.allow = 'autoplay; encrypted-media; picture-in-picture; fullscreen';
      frame.allowFullscreen = true;
      frame.className = 'video__frame';
      btn.replaceWith(frame);
      frame.focus();
    });
  });

  var error = document.querySelector('[data-contact-error]');
  if (error && new URLSearchParams(location.search).has('erreur')) error.hidden = false;

  // Une ancre de la page courante, choisie dans le menu mobile, doit refermer le menu
  // (sinon il reste ouvert et la page inerte).
  var toggle = document.querySelector('[data-menu-toggle]');
  var nav = document.getElementById('menu');
  if (toggle && nav) {
    nav.addEventListener('click', function (e) {
      var a = e.target.closest('a[href*="#"]');
      if (a && a.pathname === location.pathname && toggle.getAttribute('aria-expanded') === 'true') toggle.click();
    });
  }

  document.querySelectorAll('[data-trombi]').forEach(trombi);
  traits();
  var toc = document.querySelector('.toc');
  if (toc) sommaire(toc);
})();

// Section courante du sommaire. La section visée par l'adresse (#ancre) l'emporte tant qu'elle
// est en haut de l'écran : une section proche du bas de page n'atteint jamais la bande haute.
// Pendant le défilement doux qui suit un clic sur une ancre, on ne recalcule pas.
function sommaire(nav) {
  var links = [].slice.call(nav.querySelectorAll('a'));
  var idOf = function (a) { return decodeURIComponent(a.hash.slice(1)); };
  var secs = links.map(function (a) { return document.getElementById(idOf(a)); }).filter(Boolean);
  if (!secs.length) return;
  var verrou = 0, attente = false;
  function marquer(id) {
    links.forEach(function (a) {
      if (idOf(a) === id) a.setAttribute('aria-current', 'location');
      else a.removeAttribute('aria-current');
    });
  }
  function courante() {
    var vise = document.getElementById(decodeURIComponent(location.hash.slice(1)));
    if (vise && secs.indexOf(vise) >= 0) {
      var t = vise.getBoundingClientRect().top;
      if (t > -40 && t < innerHeight * 0.5) return vise.id;
    }
    if (innerHeight + scrollY >= document.documentElement.scrollHeight - 2) return secs[secs.length - 1].id;
    var bande = innerHeight * 0.25, id = secs[0].id;
    secs.forEach(function (s) { if (s.getBoundingClientRect().top <= bande) id = s.id; });
    return id;
  }
  function maj() {
    attente = false;
    if (Date.now() >= verrou) marquer(courante());
  }
  function demander() { if (!attente) { attente = true; requestAnimationFrame(maj); } }
  document.addEventListener('click', function (e) {
    var a = e.target.closest('a[href*="#"]');
    if (!a || a.pathname !== location.pathname) return;
    var id = idOf(a);
    if (secs.some(function (s) { return s.id === id; })) { verrou = Date.now() + 900; marquer(id); setTimeout(demander, 950); }
  });
  addEventListener('scroll', demander, { passive: true });
  addEventListener('resize', demander);
  addEventListener('hashchange', demander);
  addEventListener('load', demander);
  maj();
}

// Mini-bios : une seule fiche par liste, insérée après la dernière personne de la rangée
// (la grille garde sa forme). La fiche suit le bouton dans le DOM seulement à la fin de la
// rangée : le focus y est donc déplacé à l'ouverture, Échap ou « Fermer » le rendent au bouton.
function trombi(list, n) {
  var bios = list.querySelectorAll('details[data-bio]');
  if (!bios.length) return;
  var panel = document.createElement('li');
  panel.className = 'trombi__fiche';
  panel.id = 'trombi-fiche-' + n;
  panel.hidden = true;
  panel.tabIndex = -1;
  panel.setAttribute('role', 'region');
  var close = document.createElement('button');
  close.type = 'button';
  close.className = 'trombi__fermer';
  close.innerHTML = '<span aria-hidden="true">×</span><span class="visually-hidden"></span>';
  close.lastChild.textContent = list.dataset.close || '';
  var current = null;

  bios.forEach(function (d, i) {
    var body = d.querySelector('[data-bio-body]');
    var name = body.querySelector('.fiche__nom');
    name.id = panel.id + '-nom-' + i;
    var btn = document.createElement('button');
    btn.type = 'button';
    btn.className = 'trombi__lien';
    btn.innerHTML = d.querySelector('summary').innerHTML;
    btn.setAttribute('aria-expanded', 'false');
    btn.setAttribute('aria-controls', panel.id);
    btn.__body = body;
    btn.addEventListener('click', function () { current === btn ? hide(true) : show(btn); });
    d.replaceWith(btn);
  });

  function person(btn) { return btn.closest('li'); }
  function place() {
    var li = person(current);
    var items = [].slice.call(list.children).filter(function (el) { return el !== panel; });
    var last = li;
    items.forEach(function (el) { if (el.offsetTop === li.offsetTop) last = el; });
    last.after(panel);
    var r = li.getBoundingClientRect();
    panel.style.setProperty('--x', (r.left + r.width / 2 - panel.getBoundingClientRect().left) + 'px');
  }
  function show(btn) {
    if (current) current.setAttribute('aria-expanded', 'false');
    current = btn;
    panel.replaceChildren(close, btn.__body);
    panel.setAttribute('aria-labelledby', btn.__body.querySelector('.fiche__nom').id);
    btn.setAttribute('aria-expanded', 'true');
    panel.hidden = false;
    place();
    panel.focus({ preventScroll: true });
    panel.scrollIntoView({ block: 'nearest', behavior: matchMedia('(prefers-reduced-motion: reduce)').matches ? 'auto' : 'smooth' });
  }
  function hide(refocus) {
    if (!current) return;
    var btn = current;
    current = null;
    btn.setAttribute('aria-expanded', 'false');
    panel.hidden = true;
    if (refocus) btn.focus();
  }
  close.addEventListener('click', function () { hide(true); });
  panel.addEventListener('keydown', function (e) { if (e.key === 'Escape') { e.preventDefault(); hide(true); } });
  list.addEventListener('keydown', function (e) { if (e.key === 'Escape' && current && e.target === current) hide(true); });
  addEventListener('resize', function () { if (current) place(); });
  list.append(panel);
}
