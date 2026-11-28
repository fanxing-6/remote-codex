export function routeForThreadKey(value: string, kind?: string | null) {
  let key = value;
  if (key.startsWith('codex:thread:')) key = key.slice('codex:thread:'.length);
  if (key.startsWith('local:'))
    return '/local/' + encodeURIComponent(key.slice('local:'.length));
  if (key.startsWith('chatgpt:conversation:'))
    return (
      '/c/' + encodeURIComponent(key.slice('chatgpt:conversation:'.length))
    );
  if (kind === 'local') return '/local/' + encodeURIComponent(key);
  if (kind === 'chatgpt' && !key.includes(':'))
    return '/c/' + encodeURIComponent(key);
  return null;
}

export function visibleConversationRoute(
  document: Document,
  currentRoute: string | null,
) {
  const activeRow = document.querySelector(
    '[data-app-action-sidebar-thread-active="true"][data-app-action-sidebar-thread-id]',
  );
  const activeId = activeRow?.getAttribute('data-app-action-sidebar-thread-id');
  if (activeId && activeId.length <= 256) {
    const route = routeForThreadKey(
      activeId,
      activeRow?.getAttribute('data-app-action-sidebar-thread-kind'),
    );
    if (route) return route;
  }
  if (currentRoute?.startsWith('/local/')) return null;
  for (const attribute of [
    'data-chatgpt-selection-conversation-id',
    'data-above-composer-conversation-id',
  ]) {
    for (const selected of document.querySelectorAll('[' + attribute + ']')) {
      if (!selected.getClientRects().length) continue;
      const id = selected.getAttribute(attribute);
      if (!id || id.length > 256) continue;
      const route = routeForThreadKey(id);
      if (route) return route;
      if (/^[a-zA-Z0-9][a-zA-Z0-9_-]{15,255}$/.test(id))
        return '/c/' + encodeURIComponent(id);
    }
  }
  const row = document.querySelector(
    '[data-app-action-sidebar-thread-active="true"], [data-app-action-sidebar-thread-selected="true"]',
  );
  const chat = row
    ?.closest('[data-sidebar-chatgpt-conversation-key]')
    ?.getAttribute('data-sidebar-chatgpt-conversation-key');
  if (chat?.startsWith('chatgpt:conversation:')) return routeForThreadKey(chat);
  return null;
}
