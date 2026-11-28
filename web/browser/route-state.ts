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

export function visibleConversationRoute(document: Document) {
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

export function clickedConversationRoute(target: Element) {
  const chat = target.closest('[data-sidebar-chatgpt-conversation-key]');
  const key = chat?.getAttribute('data-sidebar-chatgpt-conversation-key');
  if (key && key.length <= 256) return routeForThreadKey(key);
  const row = target.closest('[data-app-action-sidebar-thread-row]');
  const id = row?.getAttribute('data-app-action-sidebar-thread-id');
  if (id && id.length <= 256)
    return routeForThreadKey(
      id,
      row?.getAttribute('data-app-action-sidebar-thread-kind'),
    );
  return null;
}

export function rememberConversationRoute(route: string) {
  if (
    typeof route !== 'string' ||
    !route.startsWith('/') ||
    route.startsWith('//') ||
    route.length > 2048
  )
    throw new Error('ROUTE_INVALID');
  const conversation = route.match(
    /^\/(?:g\/[^/?#]+\/)?c\/([a-zA-Z0-9_-]{16,256})\/?$/,
  );
  if (conversation) route = '/c/' + conversation[1];
  const url = new URL(location.href);
  if (url.searchParams.get('initialRoute') === route) return;
  url.searchParams.set('initialRoute', route);
  history.replaceState(history.state, '', url);
}
