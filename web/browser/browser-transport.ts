import { Encoder } from 'cbor-x';
import {
  syncMethods,
  callMethods,
  channels,
  maxFrame,
  maxQueue,
  maxPending,
  maxPorts,
  assertSerializable,
} from '../shared';
import { banner } from './overlay-ui';
import { installMobileRuntime, mobileBrowser } from './mobile-runtime';
installMobileRuntime();
const root = window as any,
  configuration = root.__cwbConfiguration;
const initialRoute = document.createElement('meta');
initialRoute.name = 'initial-route';
initialRoute.content =
  new URL(location.href).searchParams.get('initialRoute') ?? '/';
document.head.append(initialRoute);
const codec = new Encoder({ structuredClone: true, useRecords: false });
const ports = new Map<string, MessagePort>(),
  pending = new Map<number, any>(),
  workers = new Map<string, Set<Function>>(),
  themes = new Set<Function>();
const closedPorts = new Set<string>();
const diagnostics: any = { ports: {}, windowTypes: [], calls: {} };
let generation = 0,
  sequence = 0,
  connected = false,
  stopped = false,
  theme = configuration.getSystemThemeVariant,
  socket: WebSocket;
let recovering = false,
  recoveryTimer: any,
  lastClose = 0;
function rememberRoute(route: string) {
  const url = new URL(location.href);
  if (url.searchParams.get('initialRoute') === route) return;
  url.searchParams.set('initialRoute', route);
  history.replaceState(history.state, '', url);
}
function routeForThreadKey(value: string, kind?: string | null) {
  let key = value;
  if (key.startsWith('codex:thread:')) key = key.slice('codex:thread:'.length);
  if (key.startsWith('local:'))
    return '/local/' + encodeURIComponent(key.slice('local:'.length));
  if (key.startsWith('chatgpt:conversation:'))
    return (
      '/c/' + encodeURIComponent(key.slice('chatgpt:conversation:'.length))
    );
  if (kind === 'local') return '/local/' + encodeURIComponent(key);
  if (kind === 'chatgpt') return '/c/' + encodeURIComponent(key);
  return null;
}
function rememberConversation() {
  const activeRow = document.querySelector(
    '[data-app-action-sidebar-thread-active="true"][data-app-action-sidebar-thread-id]',
  );
  const activeId = activeRow?.getAttribute('data-app-action-sidebar-thread-id'),
    kind = activeRow?.getAttribute('data-app-action-sidebar-thread-kind');
  if (activeId && activeId.length <= 256) {
    const route = routeForThreadKey(activeId, kind);
    if (route) {
      rememberRoute(route);
      return;
    }
  }
  if (
    new URL(location.href).searchParams
      .get('initialRoute')
      ?.startsWith('/local/')
  )
    return;
  for (const attribute of [
    'data-chatgpt-selection-conversation-id',
    'data-above-composer-conversation-id',
  ]) {
    for (const selected of document.querySelectorAll('[' + attribute + ']')) {
      const id = selected.getAttribute(attribute);
      if (id && /^[a-zA-Z0-9][a-zA-Z0-9:_-]{15,255}$/.test(id)) {
        const route = routeForThreadKey(id) ?? '/c/' + encodeURIComponent(id);
        rememberRoute(route);
        return;
      }
    }
  }
  const row = document.querySelector(
    '[data-app-action-sidebar-thread-active="true"], [data-app-action-sidebar-thread-selected="true"]',
  );
  const chat = row
    ?.closest('[data-sidebar-chatgpt-conversation-key]')
    ?.getAttribute('data-sidebar-chatgpt-conversation-key');
  if (chat?.startsWith('chatgpt:conversation:'))
    rememberRoute(
      '/c/' + encodeURIComponent(chat.slice('chatgpt:conversation:'.length)),
    );
}
document.addEventListener(
  'click',
  (event) => {
    const row = (event.target as Element)?.closest(
      '[data-app-action-sidebar-thread-row]',
    );
    const kind = row?.getAttribute('data-app-action-sidebar-thread-kind'),
      id = row?.getAttribute('data-app-action-sidebar-thread-id');
    if (id && id.length <= 256) {
      const route = routeForThreadKey(id, kind);
      if (route) rememberRoute(route);
    }
  },
  true,
);
let routeTimer: any;
const routeObserver = new MutationObserver(() => {
  if (routeTimer) return;
  routeTimer = setTimeout(() => {
    routeTimer = undefined;
    rememberConversation();
  }, 100);
});
routeObserver.observe(document.documentElement, {
  subtree: true,
  childList: true,
  attributes: true,
  attributeFilter: [
    'data-chatgpt-selection-conversation-id',
    'data-above-composer-conversation-id',
    'data-conversation-id',
    'data-app-action-sidebar-thread-active',
    'data-app-action-sidebar-thread-selected',
  ],
});
function recover() {
  if (stopped || connected || recovering) return;
  recovering = true;
  recoveryTimer = setTimeout(async () => {
    try {
      const response = await fetch('/healthz');
      if (response.status === 401) {
        stopped = true;
        location.assign('/auth/login');
        return;
      }
      if (!response.ok) throw new Error('HEALTH_UNAVAILABLE');
      const health = await response.json();
      if (health.hostStandby) {
        location.reload();
        return;
      }
      if (!health.shimConnected) {
        banner('宿主暂时不可用，请在控制面板确认连接状态。');
        return;
      }
      if (health.assetBuildId !== configuration.assetBuildId) {
        stopped = true;
        banner('资源版本已变化，请重启 ChatGPT 后刷新。');
        return;
      }
      location.reload();
      return;
    } catch {
      banner('正在等待网络恢复。在途操作状态未知，请确认回读结果。');
    } finally {
      recovering = false;
      if (!stopped && !connected) recover();
    }
  }, 1500);
}
let resolveReady: () => void, rejectReady: (error: Error) => void;
const ready = new Promise<void>((resolve, reject) => {
  resolveReady = resolve;
  rejectReady = reject;
});
async function takeover() {
  const response = await fetch('/api/takeover', {
    method: 'POST',
    headers: { 'X-CSRF-Token': configuration.csrf },
  });
  if (!response.ok) throw new Error('TAKEOVER_FAILED');
  sessionStorage.setItem(
    'cwb-explicit-claim',
    JSON.stringify(await response.json()),
  );
  location.reload();
}
function dispose() {
  connected = false;
  for (const request of pending.values()) {
    clearTimeout(request.timer);
    request.reject(new Error('CONNECTION_LOST_STATE_UNKNOWN'));
  }
  pending.clear();
  const previous = [...ports.values()];
  ports.clear();
  closedPorts.clear();
  for (const port of previous) {
    port.postMessage(null);
    port.close();
  }
  workers.clear();
  themes.clear();
}
function raw(message: any) {
  if (!connected || socket.readyState !== WebSocket.OPEN)
    throw new Error('CONNECTION_UNAVAILABLE');
  assertSerializable(message);
  const bytes = codec.encode({ ...message, generation });
  if (
    bytes.length > maxFrame ||
    socket.bufferedAmount + bytes.length > maxQueue
  )
    throw new Error('QUEUE_LIMIT');
  socket.send(bytes);
}
async function send(message: any) {
  await ready;
  raw(message);
}
function attach(id: string, port: MessagePort, channelType: string) {
  if (ports.has(id) || closedPorts.has(id) || ports.size >= maxPorts)
    throw new Error('PORT_LIMIT');
  ports.set(id, port);
  diagnostics.ports[id] = {
    channelType,
    sent: 0,
    received: 0,
    receivedNull: false,
  };
  port.onmessage = (event) => {
    diagnostics.ports[id].sent++;
    send({
      kind: 'port-data',
      portId: id,
      channelType,
      data: event.data,
    }).catch(() => {
      socket.close(4008, 'PORT_FORWARD_FAILED');
    });
  };
  port.start();
  port.addEventListener('close', () => {
    if (!ports.delete(id)) return;
    closedPorts.add(id);
    if (closedPorts.size > 1024) {
      socket.close(4008, 'PORT_LIMIT');
      return;
    }
    if (connected) raw({ kind: 'port-close', portId: id, channelType });
  });
}
function call(method: string, ...args: any[]) {
  if (
    method === 'sendMessageFromView' &&
    args[0]?.type === 'remote-hosted-pip-active-thread-changed'
  ) {
    const id = args[0].conversationId;
    if (typeof id === 'string' && id.length > 0 && id.length <= 256)
      rememberRoute('/local/' + encodeURIComponent(id));
  }
  diagnostics.calls[method] = (diagnostics.calls[method] ?? 0) + 1;
  if (pending.size >= maxPending)
    return Promise.reject(new Error('PENDING_LIMIT'));
  const id = ++sequence;
  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => {
      pending.delete(id);
      reject(new Error('CALL_TIMEOUT_STATE_UNKNOWN'));
    }, 30000);
    pending.set(id, { resolve, reject, timer });
    send({ kind: 'call', id, method, args }).catch((error) => {
      clearTimeout(timer);
      pending.delete(id);
      reject(error);
    });
  });
}
window.addEventListener('message', (event) => {
  if (
    event.source !== window ||
    !channels.slice(0, 2).includes(event.data?.type)
  )
    return;
  const port = event.ports[0] ?? event.data.port;
  if (!(port instanceof MessagePort)) throw new Error('PORT_REQUIRED');
  const id = 'browser-' + ++sequence,
    channelType = event.data.type;
  attach(id, port, channelType);
  const metadata =
    channelType === 'connect-app-host'
      ? {}
      : { hostId: event.data.hostId, generation: event.data.generation };
  send({ kind: 'port-open', portId: id, channelType, metadata }).catch(() =>
    socket?.close(4008),
  );
});
async function acquire() {
  const reserved = sessionStorage.getItem('cwb-explicit-claim');
  sessionStorage.removeItem('cwb-explicit-claim');
  let claim: any;
  if (reserved) {
    claim = JSON.parse(reserved);
  } else {
    const navigation = performance.getEntriesByType(
      'navigation',
    )[0] as PerformanceNavigationTiming;
    const resume =
      navigation?.type === 'reload'
        ? sessionStorage.getItem('cwb-owned-claim')
        : null;
    const response = await fetch('/api/controller', {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        'X-CSRF-Token': configuration.csrf,
      },
      body: JSON.stringify(resume ? { resume } : {}),
    });
    if (response.status === 409) {
      sessionStorage.removeItem('cwb-owned-claim');
      stopped = true;
      rejectReady(new Error('CONTROLLER_OCCUPIED'));
      banner('其他页面正在控制宿主', { label: '接管控制权', run: takeover });
      return;
    }
    if (!response.ok) throw new Error('CONTROLLER_UNAVAILABLE');
    claim = await response.json();
  }
  generation = claim.generation;
  sessionStorage.setItem('cwb-owned-claim', claim.id);
  socket = new WebSocket(
    `${location.protocol === 'https:' ? 'wss:' : 'ws:'}//${location.host}/bridge`,
    ['cwb.v1', 'claim.' + claim.id],
  );
  socket.binaryType = 'arraybuffer';
  socket.onopen = () => {
    connected = true;
    resolveReady();
  };
  socket.onmessage = (event) => {
    if (typeof event.data === 'string') {
      const control = JSON.parse(event.data);
      if (control.kind === 'pong') return;
      throw new Error('CONTROL_UNKNOWN');
    }
    const message = codec.decode(new Uint8Array(event.data));
    if (message.generation !== generation) return;
    if (message.kind === 'port-data') {
      const port = ports.get(message.portId);
      if (!port) throw new Error('PORT_UNKNOWN');
      diagnostics.ports[message.portId].received++;
      if (message.data === null)
        diagnostics.ports[message.portId].receivedNull = true;
      port.postMessage(message.data);
    } else if (message.kind === 'port-close') {
      const port = ports.get(message.portId);
      if (!port) {
        if (closedPorts.has(message.portId)) return;
        throw new Error('PORT_UNKNOWN');
      }
      ports.delete(message.portId);
      closedPorts.add(message.portId);
      port.postMessage(null);
      port.close();
    } else if (message.kind === 'result' || message.kind === 'error') {
      const request = pending.get(message.id);
      if (!request) throw new Error('REQUEST_UNKNOWN');
      pending.delete(message.id);
      clearTimeout(request.timer);
      message.kind === 'result'
        ? request.resolve(message.value)
        : request.reject(new Error(message.code));
    } else if (message.kind === 'window-message') {
      if (
        typeof message.data?.type === 'string' &&
        !diagnostics.windowTypes.includes(message.data.type)
      )
        diagnostics.windowTypes.push(message.data.type);
      const transferred = (message.ports ?? []).map((id) => {
        const channel = new MessageChannel();
        attach(id, channel.port1, message.channelType);
        return channel.port2;
      });
      window.dispatchEvent(
        new MessageEvent('message', {
          data: message.data,
          origin: location.origin,
          source: window,
          ports: transferred,
        }),
      );
    } else if (message.kind === 'worker-message') {
      for (const listener of workers.get(message.worker) ?? [])
        listener(message.data);
    } else if (message.kind === 'theme') {
      theme = message.value;
      for (const listener of themes) listener();
    } else throw new Error('MESSAGE_UNKNOWN');
  };
  socket.onclose = (event) => {
    lastClose = event.code;
    dispose();
    if (event.code === 4009) {
      stopped = true;
      if (sessionStorage.getItem('cwb-owned-claim') === claim.id)
        sessionStorage.removeItem('cwb-owned-claim');
      banner('控制权已被接管', { label: '重新接管', run: takeover });
      return;
    }
    if (event.code === 4010 || event.code === 4001) {
      stopped = true;
      banner('宿主或登录会话不可用，请确认状态后重新登录。');
      return;
    }
    if (event.code === 4008) {
      stopped = true;
      banner('通信协议检查未通过，请在控制面板运行诊断。');
      return;
    }
    banner('连接中断。在途操作状态未知，请确认回读结果。');
    recover();
  };
  socket.onerror = () => banner('连接中断。在途操作状态未知，请确认回读结果。');
}
root.codexWindowType = configuration.windowType;
root.electronBridge = {
  windowType: configuration.windowType,
  usesStagingWidgetSandbox: configuration.usesStagingWidgetSandbox,
  initialReducedMotionPreference: configuration.initialReducedMotionPreference,
  initialOpaqueWindowSurfaceEnabled:
    configuration.initialOpaqueWindowSurfaceEnabled,
  getPreloadStartedAtMs: () => performance.timeOrigin,
  getSystemThemeVariant: () => theme,
  subscribeToSystemThemeVariant: (listener: Function) => {
    themes.add(listener);
    return () => themes.delete(listener);
  },
  subscribeToWorkerMessages: (worker: string, listener: Function) => {
    if (worker !== 'git') throw new Error('WORKER_UNSUPPORTED');
    if (!workers.has(worker)) {
      workers.set(worker, new Set());
      send({ kind: 'subscribe-worker', worker });
    }
    workers.get(worker)!.add(listener);
    return () => {
      workers.get(worker)?.delete(listener);
      if (!workers.get(worker)?.size) {
        workers.delete(worker);
        send({ kind: 'unsubscribe-worker', worker });
      }
    };
  },
  getPathForFile: () => {
    if (mobileBrowser) return null;
    throw new Error('FILE_UPLOAD_NOT_SUPPORTED_V1');
  },
  readDraggedImage: () => {
    throw new Error('FILE_UPLOAD_NOT_SUPPORTED_V1');
  },
  startFileDrag: () => {
    throw new Error('NATIVE_DRAG_UNSUPPORTED');
  },
  startLinkDrag: () => {
    throw new Error('NATIVE_DRAG_UNSUPPORTED');
  },
  showContextMenu: () => Promise.reject(new Error('NATIVE_MENU_UNSUPPORTED')),
  triggerSentryTestError: () =>
    Promise.reject(new Error('NATIVE_DIAGNOSTIC_UNSUPPORTED')),
};
for (const name of syncMethods.filter(
  (name) => name !== 'getSystemThemeVariant',
))
  root.electronBridge[name] = () => configuration[name];
for (const name of callMethods)
  root.electronBridge[name] = (...args: any[]) => call(name, ...args);
document.documentElement.dataset.codexWindowType = 'electron';
document.documentElement.dataset.codexOs = 'darwin';
document.documentElement.dataset.theme = theme;
const probe = () => {
  if (document.visibilityState !== 'visible') return;
  if (connected) socket.send(JSON.stringify({ kind: 'probe' }));
  else recover();
};
document.addEventListener('visibilitychange', probe);
window.addEventListener('online', probe);
function offline() {
  if (
    stopped ||
    !socket ||
    (!connected && socket.readyState >= WebSocket.CLOSING)
  )
    return;
  dispose();
  if (socket.readyState < WebSocket.CLOSING)
    socket.close(1000, 'NETWORK_OFFLINE');
  banner('连接中断。在途操作状态未知，请确认回读结果。');
  recover();
}
window.addEventListener('offline', offline);
setInterval(() => {
  if (!navigator.onLine) offline();
}, 2000);
root.__cwbRuntime = {
  ready,
  diagnostics,
  mobile: mobileBrowser,
  get generation() {
    return generation;
  },
  get connected() {
    return connected;
  },
  get lastClose() {
    return lastClose;
  },
  get socketState() {
    return socket?.readyState;
  },
};
const documentReady =
  document.readyState === 'loading'
    ? new Promise<void>((resolve) =>
        document.addEventListener('DOMContentLoaded', () => resolve(), {
          once: true,
        }),
      )
    : Promise.resolve();
Promise.all([ready, documentReady])
  .then(() => {
    for (const original of document.querySelectorAll<HTMLScriptElement>(
      'script[type="application/cwb-module"]',
    )) {
      const script = document.createElement('script');
      for (const attribute of original.attributes) {
        if (attribute.name !== 'type')
          script.setAttribute(attribute.name, attribute.value);
      }
      script.type = 'module';
      script.async = false;
      script.textContent = original.textContent;
      original.replaceWith(script);
    }
  })
  .catch(() => {
    if (stopped) return;
    stopped = true;
    socket?.close(4008, 'APP_INITIALIZATION_FAILED');
    banner('界面初始化未完成，请在控制面板运行诊断。');
  });
acquire().catch(() => {
  banner('宿主暂时不可用，请在控制面板检查连接。');
  recover();
});
