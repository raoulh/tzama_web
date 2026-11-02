(function () {
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
})();
