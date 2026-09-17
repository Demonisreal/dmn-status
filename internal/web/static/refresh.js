(() => {
  const zones = [...document.querySelectorAll('[data-live]')];
  let last = Date.now();

  // wer gerade per tastatur in der zone steht oder text darin markiert hat, wuerde
  // fokus bzw. auswahl verlieren
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
      // ziel dazugekommen oder weg: dann passt die zuordnung nicht mehr, der naechste
      // seitenaufruf holt es
      if (next.length !== zones.length) return;
      zones.forEach((zone, i) => {
        if (zone.innerHTML !== next[i].innerHTML) zone.replaceChildren(...next[i].childNodes);
      });
    } catch {}
  }

  setInterval(load, 60000);

  // nach laengerer zeit im hintergrund nicht bis zum naechsten takt mit alten zahlen dastehen
  document.addEventListener('visibilitychange', () => {
    if (Date.now() - last >= 60000) load();
  });
})();
