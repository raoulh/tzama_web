// Galerie et témoignage : hors des animations, actifs même sans IntersectionObserver.
(function () {
  var reduce = matchMedia('(prefers-reduced-motion: reduce)');
  var band = document.querySelector('[data-galerie]');
  var nav = band && band.parentNode.querySelector('.galerie__nav');
  if (band && nav) (function () {
    var items = band.querySelector('ul').children;
    var btns = [].slice.call(nav.querySelectorAll('button'));
    function update() {
      var max = band.scrollWidth - band.clientWidth - 2;
      nav.hidden = max <= 0;
      btns.forEach(function (b) { b.setAttribute('aria-disabled', b.dataset.dir < 0 ? band.scrollLeft <= 2 : band.scrollLeft >= max); });
    }
    // aria-disabled plutôt que disabled : un bouton désactivé perdrait le focus clavier en bout de bande
    btns.forEach(function (b) {
      b.addEventListener('click', function () {
        if (b.getAttribute('aria-disabled') === 'true') return;
        var step = items.length > 1 ? items[1].offsetLeft - items[0].offsetLeft : band.clientWidth;
        var n = Math.max(1, Math.floor(band.clientWidth / step));
        band.scrollBy({ left: b.dataset.dir * n * step, behavior: reduce.matches ? 'auto' : 'smooth' });
      });
    });
    band.addEventListener('scroll', update, { passive: true });
    addEventListener('resize', update);
    update();
  })();

  var temoignages = document.querySelector('[data-temoignages]');
  if (temoignages) {
    var figs = temoignages.querySelectorAll('figure'), k = Math.floor(Math.random() * figs.length);
    for (var i = 0; i < figs.length; i++) figs[i].hidden = i !== k;
  }
})();

// Animations de l'accueil. Tout est décoratif : sans ce script, la page reste complète et immobile.
(function () {
  if (!('IntersectionObserver' in window)) return;
  var root = document.documentElement;
  var reduce = matchMedia('(prefers-reduced-motion: reduce)');
  var replay = CSS.supports('animation-timeline: view()');
  var rnd = function (a, b) { return a + Math.random() * (b - a); };

  // Repli quand view() manque : is-in lance la même animation, une seule fois.
  if (!replay) {
    var rio = new IntersectionObserver(function (entries) {
      entries.forEach(function (e) { if (e.isIntersecting) { e.target.classList.add('is-in'); rio.unobserve(e.target); } });
    }, { rootMargin: '0px 0px -12% 0px' });
    document.querySelectorAll('.reveal, .titre').forEach(function (el) { rio.observe(el); });
  }

  // Brise à l'arrivée des photos scotchées, posée quand la photo est collée (55 % visible avec view(), fin de « coller » sinon).
  // Avec view(), l'entrée rejoue quand la photo ressort par le bas puis revient : la brise aussi.
  var photos = [].slice.call(document.querySelectorAll('.actu.reveal, .photo.reveal'));
  if (photos.length) (function () {
    function start(el) {
      el.classList.add('is-swaying');
      clearTimeout(el.__swayTimer); el.__swayTimer = setTimeout(function () { el.classList.remove('is-swaying'); }, 4000);
    }
    photos.forEach(function (el) {
      el.dataset.sway = 1 + Math.floor(Math.random() * 3);
      el.style.setProperty('--sway-amp', rnd(1, 3).toFixed(2) + 'deg');
      el.style.setProperty('--sway-dur', rnd(1.2, 2).toFixed(2) + 's');
      el.style.setProperty('--sway-delay', rnd(0, .3).toFixed(2) + 's');
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
          // sortie par le bas : réarmée ; sortie par le haut : pas de brise en revenant du haut
          el.__swayArmed = e.boundingClientRect.top > 0;
          if (el.__swayArmed) { clearTimeout(el.__swayTimer); el.classList.remove('is-swaying'); }
        }
      });
    }, { threshold: [0, .55] });
    photos.forEach(function (el) { sio.observe(el); });
  })();

  // Vie au repos : toutes les 6 à 20 s, une seule petite animation à la fois, tirée parmi les éléments visibles hors du texte
  // d'ouverture ; pause onglet caché ou menu ouvert. La photo de l'ouverture a son rythme (25 à 45 s) mais partage le verrou.
  var items = [].slice.call(document.querySelectorAll('[data-idle]'));
  var zones = [].slice.call(document.querySelectorAll('[data-idle-text]'));
  // busy : animation au repos en cours ; pbusy : la photo de l'ouverture bouge
  var visible = [], timer = 0, busy = false, pbusy = false;
  function allowed() { return !document.hidden && !reduce.matches && !root.classList.contains('menu-open'); }

  var fig = document.querySelector('[data-hero-photo]');
  var data = document.getElementById('hero-photos');
  if (fig && data) (function () {
    var list = JSON.parse(data.textContent);
    var img = fig.querySelector('.paper__img'), cap = fig.querySelector('.etiq');
    var cur = Number(fig.dataset.photoIndex) || 0, next = -1, ptimer = 0, posed = false;
    var shown = function () { return getComputedStyle(fig).display !== 'none'; };
    // l'animation du papier remonte jusqu'à la figure ; celle du ruban (::before) est émise par la figure
    function onEnd(name, cb) {
      var done = false, h = function (e) { if (e.animationName === name) finish(); };
      var finish = function () { if (done) return; done = true; fig.removeEventListener('animationend', h); cb(); };
      fig.addEventListener('animationend', h);
      setTimeout(finish, 3000);
    }
    function pose(cb) {
      fig.classList.add('is-posing');
      onEnd('tape-presser', function () { fig.classList.remove('is-posing'); fig.classList.add('is-posed'); if (cb) cb(); });
    }
    function preload() {
      if (next >= 0 || list.length < 2) return;
      do { next = Math.floor(Math.random() * list.length); } while (next === cur);
      var im = new Image(); im.sizes = img.sizes; im.srcset = list[next].srcset; im.src = list[next].src;
    }
    function firstPose() {
      if (posed) return; posed = true;
      if (shown() && allowed()) { pbusy = true; pose(function () { pbusy = false; }); } else fig.classList.add('is-posed');
      if (shown()) setTimeout(preload, 5000);
      pschedule();
    }
    function show(i) {
      img.srcset = list[i].srcset; img.src = list[i].src; img.alt = list[i].alt;
      cap.textContent = list[i].caption; fig.dataset.photoIndex = cur = i; next = -1;
    }
    // forced : clic, ignoré seulement pendant une animation de la photo elle-même ; l'échange automatique attend aussi
    // la fin des autres animations et le départ de la souris. Mouvement réduit : remplacement direct.
    function swap(forced) {
      if (!forced) ptimer = 0;
      if (!shown() || pbusy || !posed || (!forced && (busy || !allowed() || fig.matches(':hover')))) { if (!forced) pschedule(allowed() ? 4000 : 0); return; }
      if (next < 0) { preload(); if (next < 0) return; if (!forced) { pschedule(4000); return; } }
      clearTimeout(ptimer); ptimer = 0;
      if (!allowed()) { show(next); preload(); pschedule(); return; }
      pbusy = true;
      fig.classList.remove('is-posed'); fig.classList.add('is-peeling');
      onEnd('polaroid-decoller', function () {
        show(next);
        fig.classList.remove('is-peeling');
        pose(function () { pbusy = false; preload(); pschedule(); });
      });
    }
    function pschedule(delay) { if (!ptimer && list.length > 1) ptimer = setTimeout(swap, delay || rnd(25000, 45000)); }
    document.addEventListener('visibilitychange', function () { if (document.hidden) { clearTimeout(ptimer); ptimer = 0; } else if (posed) pschedule(); });
    if (img.complete && img.naturalWidth) firstPose();
    else { img.addEventListener('load', firstPose); img.addEventListener('error', firstPose); setTimeout(firstPose, 2500); }
    var btn = fig.querySelector('.polaroid__swap');
    if (btn && list.length > 1) { btn.hidden = false; btn.addEventListener('click', function () { swap(true); }); }
  })();

  if (!items.length) return;
  var io = new IntersectionObserver(function (es) {
    es.forEach(function (e) {
      var i = visible.indexOf(e.target);
      if (e.isIntersecting && i < 0) visible.push(e.target);
      if (!e.isIntersecting && i >= 0) visible.splice(i, 1);
    });
  }, { threshold: .5 });
  items.forEach(function (el) { io.observe(el); });
  // une chute descend d'environ 280 px : la zone testée est allongée vers le bas
  function overText(el, kind) {
    var r = el.getBoundingClientRect();
    var bottom = kind === 'tomber' ? r.bottom + 280 : r.bottom, left = r.left - 24, right = r.right + 24;
    return zones.some(function (z) {
      var t = z.getBoundingClientRect();
      return left < t.right && right > t.left && r.top < t.bottom && bottom > t.top;
    });
  }
  function pick() {
    var c = [];
    visible.forEach(function (el) {
      el.getAttribute('data-idle').split(' ').forEach(function (k) { if (!overText(el, k)) c.push([el, k]); });
    });
    return c.length ? c[Math.floor(Math.random() * c.length)] : null;
  }
  function run() {
    timer = 0;
    var p = allowed() && !busy && !pbusy ? pick() : null;
    if (p) {
      var el = p[0], cls = 'idle-' + p[1], ended = false;
      busy = true; el.classList.add(cls);
      var done = function () { if (ended) return; ended = true; el.classList.remove(cls); busy = false; };
      var anims = el.getAnimations ? el.getAnimations({ subtree: true }).filter(function (a) { return /^idle-/.test(a.animationName || ''); }) : [];
      if (anims.length) Promise.all(anims.map(function (a) { return a.finished; })).then(done, done); else done();
      setTimeout(done, 8000);
    }
    schedule();
  }
  function schedule() { if (!timer) timer = setTimeout(run, rnd(6000, 20000)); }
  document.addEventListener('visibilitychange', function () { if (document.hidden) { clearTimeout(timer); timer = 0; } else schedule(); });
  schedule();
})();
