export function initializeTabSession() {
  const stored = sessionStorage.getItem('cwb-tab-id');
  if (stored && stored === window.name) return;
  const id = 'cwb-tab-' + crypto.randomUUID();
  window.name = id;
  sessionStorage.setItem('cwb-tab-id', id);
  sessionStorage.removeItem('cwb-owned-claim');
}
