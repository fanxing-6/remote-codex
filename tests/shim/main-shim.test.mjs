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
    HOST_TRANSPORT: JSON.stringify('window.transportInstalled = true;'),
  },
  write: false,
});
const shim = new vm.Script(bundle.outputFiles[0].text);
const initialURL = 'app://-/index.html';
const hostURL = initialURL + '?cwbHost=memory-test';
const emptyHTML =
  '<!doctype html><html><head><meta charset="utf-8"></head><body></body></html>';

async function flush() {
  await new Promise((resolve) => setImmediate(resolve));
}

function createEnvironment(t, { attachError } = {}) {
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
  let resolveFetch;
  const fetchEnabled = new Promise((resolve) => {
    resolveFetch = resolve;
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
    if (method === 'Fetch.enable' && commands.length === 1) return fetchEnabled;
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
  target.hide = () => events.push('hide');
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
    enableFetch: resolveFetch,
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

test('initial load completes before host navigation and Fetch interception narrows', async (t) => {
  const env = createEnvironment(t);
  await flush();
  assert.deepEqual(env.events, [
    'hide',
    'disable-throttling',
    'attach',
    'Fetch.enable',
    'initial-load',
    'focus',
  ]);
  assert.deepEqual(JSON.parse(JSON.stringify(env.commands)), [
    {
      method: 'Fetch.enable',
      params: {
        patterns: [{ urlPattern: initialURL + '*', resourceType: 'Document' }],
      },
    },
  ]);
  env.wc.debugger.emit('message', {}, 'Fetch.requestPaused', {
    requestId: 'initial-document',
  });
  const fulfillment = env.commands.at(-1);
  assert.equal(fulfillment.method, 'Fetch.fulfillRequest');
  assert.equal(fulfillment.params.requestId, 'initial-document');
  assert.equal(fulfillment.params.responseCode, 200);
  assert.equal(
    Buffer.from(fulfillment.params.body, 'base64').toString(),
    emptyHTML,
  );
  env.enableFetch();
  await flush();
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
  assert.deepEqual(
    env.commands
      .filter(({ method }) => method === 'Fetch.enable')
      .map(({ params }) => params.patterns[0].urlPattern),
    [initialURL + '*', hostURL],
  );
  assert.deepEqual(
    env.commands.slice(-3).map(({ method }) => method),
    ['Page.enable', 'Page.addScriptToEvaluateOnNewDocument', 'Fetch.enable'],
  );
  assert.equal(
    env.commands.at(-2).params.source,
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
  assert.equal(env.target.destroyed, false);
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
