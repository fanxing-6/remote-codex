import assert from 'node:assert/strict';
import { EventEmitter } from 'node:events';
import { test } from 'node:test';
import vm from 'node:vm';
import { build } from '../../web/node_modules/esbuild/lib/main.js';

const bundle = await build({
  stdin: {
    contents: `export { install } from './shim/main-shim'; export { syncMethods } from './shared';`,
    resolveDir: new URL('../../web/', import.meta.url).pathname,
    loader: 'ts',
  },
  bundle: true,
  platform: 'node',
  format: 'cjs',
  external: ['node:http', 'node:crypto', 'ws'],
  define: {
    EARLY_CAPTURE: JSON.stringify('window.captureInstalled = true;'),
    HOST_TRANSPORT: JSON.stringify(
      'window.transportInstalled = true; window.__cwbHost = {};',
    ),
  },
  write: false,
});
const shim = new vm.Script(bundle.outputFiles[0].text);
const initialURL = 'app://-/index.html';
const hostURL = initialURL + '?cwbHost=memory-test';

async function flush() {
  await new Promise((resolve) => setImmediate(resolve));
}

function createEnvironment(t, { attachError, blockingError } = {}) {
  const events = [];
  const commands = [];
  const timers = new Set();
  const renderer = vm.createContext({});
  renderer.window = renderer;
  const app = new EventEmitter();
  app.getVersion = () => 'test';
  let loading = false;
  let url = '';
  let stopCalls = 0;
  let resolveNetwork;
  const networkEnabled = new Promise((resolve) => {
    resolveNetwork = resolve;
  });
  const debuggerAPI = new EventEmitter();
  debuggerAPI.attached = false;
  debuggerAPI.attach = (version) => {
    assert.equal(version, '1.3');
    if (attachError) throw attachError;
    debuggerAPI.attached = true;
    events.push('attach');
  };
  debuggerAPI.isAttached = () => debuggerAPI.attached;
  debuggerAPI.detach = () => {
    debuggerAPI.attached = false;
    debuggerAPI.emit('detach');
  };
  debuggerAPI.sendCommand = (method, params) => {
    commands.push({ method, params });
    events.push(method);
    if (method === 'Network.enable') return networkEnabled;
    if (method === 'Network.setBlockedURLs' && blockingError)
      return Promise.reject(blockingError);
    return Promise.resolve({});
  };
  const wc = new EventEmitter();
  wc.debugger = debuggerAPI;
  wc.setBackgroundThrottling = (value) => {
    assert.equal(value, false);
    events.push('disable-throttling');
  };
  wc.stop = () => {
    stopCalls++;
    throw new Error('INITIAL_LOAD_ABORTED');
  };
  wc.isLoading = () => loading;
  wc.getURL = () => url;
  wc.executeJavaScript = async (source) => {
    events.push(source.startsWith('(()=>') ? 'configuration' : 'execute');
    return vm.runInContext(source, renderer);
  };
  wc.loadURL = async (value) => {
    events.push('host-load');
    assert.equal(value, hostURL);
    assert.equal(loading, false);
    assert.equal(url, initialURL);
    loading = true;
    url = value;
    delete renderer.electronBridge;
    vm.runInContext('window.captureInstalled = true;', renderer);
  };
  const target = new EventEmitter();
  target.id = 2;
  target.webContents = wc;
  target.destroyed = false;
  target.getOpacity = () => envValue('setOpacity');
  target.isVisible = () => !events.includes('hide');
  function envValue(method) {
    return events.findLast(event => Array.isArray(event) && event[0] === method)?.[1];
  }
  target.hide = function () {
    assert.equal(this, target);
    events.push('hide');
  };
  for (const method of [
    'setOpacity',
    'setFocusable',
    'setSkipTaskbar',
    'setIgnoreMouseEvents',
    'setBounds',
  ])
    target[method] = (value) =>
      events.push([method, JSON.parse(JSON.stringify(value))]);
  for (const method of ['show', 'showInactive', 'focus', 'restore', 'maximize'])
    target[method] = () => events.push('target-' + method);
  target.isDestroyed = () => target.destroyed;
  target.destroy = () => {
    target.destroyed = true;
    target.emit('destroyed');
  };
  const original = {
    id: 1,
    isDestroyed: () => false,
    focus: () => events.push('focus'),
  };
  const electron = {
    app,
    BrowserWindow: {
      getAllWindows: () => [original],
      getFocusedWindow: () => original,
    },
    Menu: {
      getApplicationMenu: () => ({
        items: [
          {
            label: 'New Window',
            enabled: true,
            click() {
              app.emit('browser-window-created', {}, target);
              target.restore();
              target.show();
              target.focus();
              events.push('initial-load');
              loading = true;
              url = initialURL;
            },
          },
        ],
      }),
    },
  };
  const server = new EventEmitter();
  server.listen = (port, address, callback) => {
    assert.equal(port, 0);
    assert.equal(address, '127.0.0.1');
    callback();
  };
  server.close = () => events.push('server-close');
  class MemoryWebSocketServer extends EventEmitter {
    clients = new Set();
    handleUpgrade(req, socket, head, callback) {
      this.clients.add(socket);
      callback(socket);
    }
    close() {}
  }
  const modules = {
    'node:http': { createServer: () => server },
    'node:crypto': { timingSafeEqual: (a, b) => a.equals(b) },
    ws: { WebSocketServer: MemoryWebSocketServer, WebSocket: { OPEN: 1 } },
  };
  const context = vm.createContext({
    module: { exports: {} },
    Buffer,
    process: {
      pid: 1,
      mainModule: {
        require(name) {
          assert.equal(name, 'electron');
          return electron;
        },
      },
    },
    require(name) {
      assert.ok(Object.hasOwn(modules, name), name);
      return modules[name];
    },
    setTimeout(callback, delay) {
      const timer = { callback, delay };
      timers.add(timer);
      return timer;
    },
    clearTimeout: (timer) => timers.delete(timer),
    setImmediate,
    setInterval: () => ({}),
    clearInterval: () => {},
  });
  shim.runInContext(context);
  const bridge = Object.fromEntries(
    context.module.exports.syncMethods.map((name) => [name, () => name]),
  );
  t.after(() => context.__cwbShimV1?.stop());
  context.module.exports.install({
    appVersion: 'test',
    token: 'a'.repeat(64),
    instance: 'memory-test',
  });
  return {
    context,
    events,
    commands,
    renderer,
    target,
    wc,
    stopCalls: () => stopCalls,
    enableNetwork: resolveNetwork,
    connect() {
      const ws = new EventEmitter();
      ws.readyState = 1;
      ws.bufferedAmount = 0;
      ws.messages = [];
      ws.send = (message) => ws.messages.push(JSON.parse(message));
      ws.close = (code, reason) => {
        ws.readyState = 3;
        ws.closeCode = code;
        ws.closeReason = reason;
        ws.emit('close');
      };
      ws.request = async (request) => {
        ws.emit('message', Buffer.from(JSON.stringify(request)), false);
        await flush();
      };
      server.emit(
        'upgrade',
        {
          url: '/internal',
          headers: { authorization: 'Bearer ' + 'a'.repeat(64) },
        },
        ws,
        Buffer.alloc(0),
      );
      return ws;
    },
    completeLoad({ withBridge = true, atURL = url } = {}) {
      loading = false;
      url = atURL;
      if (withBridge) renderer.electronBridge = bridge;
    },
    async poll() {
      for (const timer of [...timers]) {
        if (timer.delay !== 20) continue;
        timers.delete(timer);
        timer.callback();
      }
      await flush();
    },
  };
}

test('hidden window blocks assets before host navigation and waits for the bridge', async (t) => {
  const env = createEnvironment(t);
  await flush();
  await flush();
  assert.deepEqual(env.events, [
    ['setOpacity', 0],
    ['setFocusable', false],
    ['setSkipTaskbar', true],
    ['setIgnoreMouseEvents', true],
    ['setBounds', { x: -32000, y: -32000, width: 1, height: 1 }],
    'hide',
    'disable-throttling',
    'attach',
    'Network.enable',
    'initial-load',
    'focus',
    'hide',
  ]);
  assert.deepEqual(JSON.parse(JSON.stringify(env.commands)), [
    {
      method: 'Network.enable',
    },
  ]);
  env.enableNetwork();
  await flush();
  assert.deepEqual(JSON.parse(JSON.stringify(env.commands)), [
    { method: 'Network.enable' },
    {
      method: 'Network.setBlockedURLs',
      params: {
        urls: [
          'app://-/assets/*',
          '*/assets/*',
          '*.js',
          '*.mjs',
          '*.css',
          '*.wasm',
        ],
      },
    },
    { method: 'Debugger.enable' },
    { method: 'Page.enable' },
    {
      method: 'Page.addScriptToEvaluateOnNewDocument',
      params: { source: 'window.captureInstalled = true;' },
    },
  ]);
  assert.equal(env.events.includes('host-load'), false);
  env.completeLoad({ withBridge: false });
  await env.poll();
  assert.equal(env.events.includes('host-load'), false);
  env.completeLoad({ atURL: 'about:blank' });
  await env.poll();
  assert.equal(env.events.includes('host-load'), false);
  env.completeLoad({ atURL: initialURL });
  await env.poll();
  assert.ok(
    env.events.indexOf('configuration') < env.events.indexOf('host-load'),
  );
  assert.equal(env.commands.length, 5);
  assert.equal(
    env.commands.at(-1).params.source,
    'window.captureInstalled = true;',
  );
  assert.equal(env.renderer.transportInstalled, undefined);
  env.completeLoad({ withBridge: false });
  await env.poll();
  assert.equal(env.renderer.transportInstalled, undefined);
  env.completeLoad();
  await env.poll();
  assert.equal(env.renderer.transportInstalled, true);
  assert.deepEqual(
    env.commands.slice(-3).map(({ method, params }) => [method, params?.name]),
    [
      ['Runtime.enable', undefined],
      ['Runtime.addBinding', '__cwbSend'],
      ['Runtime.addBinding', '__cwbFatal'],
    ],
  );
  assert.equal(env.stopCalls(), 0);
  assert.equal(
    env.commands.some(({ method }) => method.startsWith('Fetch.')),
    false,
  );
  assert.equal(env.target.destroyed, false);
  const ws = env.connect();
  await ws.request({
    kind: 'hello',
    id: 'hello',
    pid: 1,
    shimInstanceId: 'memory-test',
    protocolVersion: 1,
  });
  assert.equal(ws.messages.at(-1).value.ready, true);
  await ws.request({ kind: 'probe', id: 'probe-ready' });
  assert.equal(ws.messages.at(-1).id, 'probe-ready');
  assert.equal(ws.messages.at(-1).value.ready, true);
  vm.runInContext('window.__cwbHost = undefined;', env.renderer);
  await ws.request({ kind: 'probe', id: 'probe-missing' });
  assert.deepEqual(ws.messages.at(-1), {
    kind: 'host-offline',
    errorCode: 'HOST_NOT_READY',
  });
  assert.equal(ws.closeCode, 4010);
  assert.equal(ws.closeReason, 'HOST_NOT_READY');
});

test('hidden window hides on visibility events and suppresses display methods', async (t) => {
  const env = createEnvironment(t);
  await flush();
  env.target.hide = () => {
    throw new Error('BOUND_HIDE_REQUIRED');
  };
  for (const event of [
    'show',
    'ready-to-show',
    'restore',
    'focus',
    'maximize',
  ]) {
    const before = env.events.length;
    env.target.emit(event);
    assert.deepEqual(env.events.slice(before), ['hide'], event);
  }
  for (const method of [
    'show',
    'showInactive',
    'focus',
    'restore',
    'maximize',
  ]) {
    const before = env.events.length;
    assert.equal(env.target[method](), undefined);
    assert.equal(env.events.length, before, method);
  }
  env.context.__cwbShimV1.stop();
  assert.equal(env.target.destroyed, true);
  assert.equal(env.wc.debugger.isAttached(), false);
  assert.equal(env.context.__cwbShimV1, undefined);
  env.enableNetwork();
  await flush();
});

test('capture errors stay inside the shim and let the application continue loading', async (t) => {
  const env = createEnvironment(t, { attachError: new Error('ATTACH_FAILED') });
  await flush();
  assert.ok(env.events.includes('initial-load'));
  assert.equal(env.events.includes('host-load'), false);
  assert.equal(env.stopCalls(), 0);
  assert.equal(env.target.destroyed, true);
  assert.equal(env.context.__cwbShimV1, undefined);
});

test('asset blocking errors release the hidden window', async (t) => {
  const env = createEnvironment(t, {
    blockingError: new Error('BLOCKING_FAILED'),
  });
  await flush();
  env.enableNetwork();
  await flush();
  assert.equal(env.events.includes('host-load'), false);
  assert.equal(env.target.destroyed, true);
  assert.equal(env.context.__cwbShimV1, undefined);
});
