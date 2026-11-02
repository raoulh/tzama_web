(function () {
  var root = document.documentElement;
  var btn = document.querySelector('[data-menu-toggle]');
  var nav = document.getElementById('menu');

  if (btn && nav) {
    var label = btn.querySelector('.menu-btn__label');
    var setOpen = function (open) {
      btn.setAttribute('aria-expanded', String(open));
      nav.classList.toggle('is-open', open);
      root.classList.toggle('menu-open', open);
      label.textContent = open ? btn.dataset.labelClose : btn.dataset.labelOpen;
      document.querySelectorAll('main, footer, .thumb-bar').forEach(function (el) { el.inert = open; });
    };
    btn.addEventListener('click', function () { setOpen(btn.getAttribute('aria-expanded') !== 'true'); });
    document.addEventListener('keydown', function (e) {
      if (e.key === 'Escape' && btn.getAttribute('aria-expanded') === 'true') { setOpen(false); btn.focus(); }
    });
    // repassage en desktop menu ouvert : la page resterait inerte et bloquée
    matchMedia('(min-width: 60em)').addEventListener('change', function (e) { if (e.matches) setOpen(false); });
  }

  var header = document.querySelector('.header');
  var heroCta = document.querySelector('[data-hero-cta]');
  if (!('IntersectionObserver' in window)) { root.classList.add('hero-cta-away'); return; }
  new IntersectionObserver(function (entries) {
    header.classList.toggle('is-compact', !entries[0].isIntersecting);
  }).observe(document.querySelector('.scroll-sentinel'));
  if (heroCta) {
    new IntersectionObserver(function (entries) {
      root.classList.toggle('hero-cta-visible', entries[0].isIntersecting);
      root.classList.toggle('hero-cta-away', !entries[0].isIntersecting);
    }).observe(heroCta);
  } else {
    root.classList.add('hero-cta-away');
  }
})();
