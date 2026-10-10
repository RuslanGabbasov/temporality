(() => {
  const root = document.documentElement;
  const langToggle = document.getElementById('langToggle');
  const setLanguage = (lang) => {
    root.lang = lang;
    document.querySelectorAll('[data-ru][data-en]').forEach((el) => {
      const value = el.dataset[lang];
      if (value !== undefined) el.innerHTML = value;
    });
    langToggle.textContent = lang === 'ru' ? 'EN' : 'RU';
    document.title = lang === 'ru' ? 'Temporality — Опыт, который накапливается' : 'Temporality — Experience that compounds';
    try { localStorage.setItem('temporality-site-language', lang); } catch (_) {}
  };
  let lang = 'ru';
  try { lang = localStorage.getItem('temporality-site-language') || 'ru'; } catch (_) {}
  setLanguage(lang);
  langToggle.addEventListener('click', () => setLanguage(root.lang === 'ru' ? 'en' : 'ru'));

  document.querySelectorAll('.demo-tab').forEach((tab) => tab.addEventListener('click', () => {
    document.querySelectorAll('.demo-tab').forEach((t) => t.classList.toggle('active', t === tab));
    document.querySelectorAll('.demo-screen').forEach((screen) => screen.classList.toggle('active', screen.id === `screen-${tab.dataset.screen}`));
  }));

  const toast = document.getElementById('toast');
  let toastTimer;
  document.querySelectorAll('[data-copy]').forEach((button) => button.addEventListener('click', async () => {
    const text = button.dataset.copy.replace(/&#10;/g, '\n');
    try {
      await navigator.clipboard.writeText(text);
      toast.textContent = root.lang === 'ru' ? 'Скопировано' : 'Copied';
      toast.classList.add('visible');
      clearTimeout(toastTimer);
      toastTimer = setTimeout(() => toast.classList.remove('visible'), 1600);
    } catch (_) {
      toast.textContent = root.lang === 'ru' ? 'Выделите и скопируйте команду' : 'Select and copy the command';
      toast.classList.add('visible');
      clearTimeout(toastTimer);
      toastTimer = setTimeout(() => toast.classList.remove('visible'), 2200);
    }
  }));


  const viewer = document.getElementById('imageViewer');
  const viewerImage = document.getElementById('viewerImage');
  const closeViewer = () => { viewer.classList.remove('open'); viewer.setAttribute('aria-hidden', 'true'); viewerImage.removeAttribute('src'); document.body.style.overflow = ''; };
  document.querySelectorAll('[data-lightbox="true"]').forEach((link) => link.addEventListener('click', (event) => {
    event.preventDefault(); viewerImage.src = link.getAttribute('href'); viewerImage.alt = link.querySelector('img')?.alt || 'Temporality interface screenshot';
    viewer.classList.add('open'); viewer.setAttribute('aria-hidden', 'false'); document.body.style.overflow = 'hidden';
  }));
  closeViewer.addEventListener('click', closeViewer);
  viewer.addEventListener('click', (event) => { if (event.target === viewer) closeViewer(); });
  document.addEventListener('keydown', (event) => { if (event.key === 'Escape') closeViewer(); });

  const menuToggle = document.getElementById('menuToggle');
  menuToggle.addEventListener('click', () => document.querySelector('.nav').classList.toggle('nav-open'));
  document.querySelectorAll('.nav a').forEach((a) => a.addEventListener('click', () => document.querySelector('.nav').classList.remove('nav-open')));
})();
