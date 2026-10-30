(() => {
  const root = document.documentElement;
  const langToggle = document.getElementById('langToggle');
  const setLanguage = (lang) => {
    root.lang = lang;
    document.querySelectorAll('[data-ru][data-en]').forEach((el) => {
      const value = el.dataset[lang];
      if (value !== undefined) el.innerHTML = value;
    });
    // Documentation links follow the site language: RU tree at /docs/, EN mirror at /docs/en/.
    document.querySelectorAll('a[data-docs-link-ru]').forEach((el) => {
      el.href = lang === 'en' ? el.dataset.docsLinkEn : el.dataset.docsLinkRu;
    });
    langToggle.textContent = lang === 'ru' ? 'EN' : 'RU';
    document.title = lang === 'ru' ? 'Temporality — среда исполнения и контур управления для агентских систем' : 'Temporality — a runtime and control plane for agent systems';
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
  const viewerVideo = document.getElementById('viewerVideo');
  const viewerClose = document.getElementById('viewerClose');
  const hideViewer = () => {
    viewer.classList.remove('open'); viewer.setAttribute('aria-hidden', 'true');
    viewerImage.removeAttribute('src');
    if (viewerVideo) { viewerVideo.pause(); viewerVideo.removeAttribute('src'); viewerVideo.load(); }
    document.body.style.overflow = '';
  };
  const openViewer = (imageSrc, alt, videoSrc) => {
    if (videoSrc) {
      viewerImage.style.display = 'none';
      if (viewerVideo) { viewerVideo.style.display = 'block'; viewerVideo.src = videoSrc; }
    } else {
      viewerImage.style.display = '';
      viewerImage.src = imageSrc; viewerImage.alt = alt || 'Temporality interface screenshot';
      if (viewerVideo) { viewerVideo.style.display = 'none'; viewerVideo.removeAttribute('src'); viewerVideo.load(); }
    }
    viewer.classList.add('open'); viewer.setAttribute('aria-hidden', 'false'); document.body.style.overflow = 'hidden';
  };
  document.querySelectorAll('[data-lightbox="true"]').forEach((link) => link.addEventListener('click', (event) => {
    event.preventDefault();
    openViewer(link.getAttribute('href'), link.querySelector('img')?.alt || 'Temporality interface screenshot');
  }));
  // Hero video card: the lightbox plays the recording with controls, full size.
  document.querySelectorAll('[data-video-lightbox]').forEach((card) => card.addEventListener('click', (event) => {
    event.preventDefault();
    openViewer('', card.getAttribute('aria-label') || 'Temporality knowledge map', card.getAttribute('data-video-lightbox'));
  }));
  viewerClose.addEventListener('click', hideViewer);
  viewer.addEventListener('click', (event) => { if (event.target === viewer) hideViewer(); });
  document.addEventListener('keydown', (event) => { if (event.key === 'Escape') hideViewer(); });

  const menuToggle = document.getElementById('menuToggle');
  menuToggle.addEventListener('click', () => document.querySelector('.nav').classList.toggle('nav-open'));
  document.querySelectorAll('.nav a').forEach((a) => a.addEventListener('click', () => document.querySelector('.nav').classList.remove('nav-open')));

  // Latest release chip: every [data-release-slot] carries a build-time
  // fallback; on load the GitHub API tells the real newest release and the
  // slots get rewritten. Fails quietly — the static value stays on timeouts
  // and rate limits.
  const releaseSlots = document.querySelectorAll('[data-release-slot]');
  if (releaseSlots.length) {
    fetch('https://api.github.com/repos/RuslanGabbasov/temporality/releases/latest', { headers: { Accept: 'application/vnd.github+json' } })
      .then((response) => (response.ok ? response.json() : null))
      .then((data) => {
        const version = String(data?.tag_name || '').replace(/^v/, '');
        if (!version) return;
        releaseSlots.forEach((el) => { el.textContent = version; });
      })
      .catch(() => { /* keep the static fallback */ });
  }
})();

