(() => {
  const main = document.querySelector('main');
  let last = Date.now();

  async function load() {
    // wer gerade per tastatur in der liste steht, wuerde den fokus verlieren
    if (document.visibilityState !== 'visible' || (main.contains(document.activeElement) && document.activeElement !== main)) return;
    last = Date.now();
    try {
      const res = await fetch(location.href, { cache: 'no-store' });
      if (!res.ok) return;
      const next = new DOMParser().parseFromString(await res.text(), 'text/html').querySelector('main');
      if (next) main.replaceChildren(...next.childNodes);
    } catch {}
  }

  setInterval(load, 60000);

  // nach laengerer zeit im hintergrund nicht bis zum naechsten takt mit alten zahlen dastehen
  document.addEventListener('visibilitychange', () => {
    if (Date.now() - last >= 60000) load();
  });
})();
