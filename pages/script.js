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
  const viewerClose = document.getElementById('viewerClose');
  const hideViewer = () => { viewer.classList.remove('open'); viewer.setAttribute('aria-hidden', 'true'); viewerImage.removeAttribute('src'); document.body.style.overflow = ''; };
  document.querySelectorAll('[data-lightbox="true"]').forEach((link) => link.addEventListener('click', (event) => {
    event.preventDefault(); viewerImage.src = link.getAttribute('href'); viewerImage.alt = link.querySelector('img')?.alt || 'Temporality interface screenshot';
    viewer.classList.add('open'); viewer.setAttribute('aria-hidden', 'false'); document.body.style.overflow = 'hidden';
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

// Ping-pong playback for hero videos: forward to the end, then reverse to
// the start, forever — a looping demo that never visibly «restarts».
document.querySelectorAll('video[data-pingpong="true"]').forEach((video) => {
  let direction = 1
  const step = () => {
    if (video.paused || video.ended) return
    const t = video.currentTime + direction * 0.033
    if (t >= video.duration) { video.currentTime = video.duration; direction = -1 }
    else if (t <= 0) { video.currentTime = 0; direction = 1 }
    else video.currentTime = t
    requestAnimationFrame(step)
  }
  const start = () => {
    // Manual scrubbing (currentTime stepping) needs the whole file buffered:
    // with preload the browser only fetches what a plain playback would, and
    // every backward seek re-aborts the range request on file:// setups.
    try { video.pause() } catch { /* already paused */ }
    video.load()
    video.addEventListener('canplaythrough', () => {
      direction = 1
      video.currentTime = 0
      requestAnimationFrame(step)
    }, { once: true })
  }
  if (video.readyState >= 4) start()
  else video.addEventListener('canplaythrough', start, { once: true })
})
