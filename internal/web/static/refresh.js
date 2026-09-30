(() => {
  const zones = [...document.querySelectorAll('[data-live]')];
  let last = Date.now();

  function busy(zone) {
    const sel = getSelection();
    return zone.contains(document.activeElement) || (!sel.isCollapsed && zone.contains(sel.anchorNode));
  }

  async function load() {
    if (document.visibilityState !== 'visible' || zones.some(busy)) return;
    last = Date.now();
    try {
      const res = await fetch(location.href, { cache: 'no-store' });
      if (!res.ok) return;
      const doc = new DOMParser().parseFromString(await res.text(), 'text/html');
      const next = doc.querySelectorAll('[data-live]');
      if (next.length !== zones.length) return;
      zones.forEach((zone, i) => {
        if (zone.innerHTML !== next[i].innerHTML) zone.replaceChildren(...next[i].childNodes);
      });
    } catch {}
  }

  setInterval(load, 60000);

  document.addEventListener('visibilitychange', () => {
    if (Date.now() - last >= 60000) load();
  });
})();
