import assert from 'node:assert/strict';
import { test } from 'node:test';
import vm from 'node:vm';
import { build } from '../../web/node_modules/esbuild/lib/main.js';
import { Encoder } from '../../web/node_modules/cbor-x/index.js';

const bundle = await build({
  entryPoints: [new URL('../../web/shim/host-transport.ts', import.meta.url).pathname],
  bundle: true,
  platform: 'browser',
  format: 'iife',
  target: 'chrome130',
  write: false,
});
const transport = new vm.Script(bundle.outputFiles[0].text);
const codec = new Encoder({ structuredClone: true, useRecords: false });

function installEnvironment() {
  Uint8Array.prototype.toBase64 = function () {
    return encodeBase64(this);
  };
  Uint8Array.fromBase64 = (value) => Uint8Array.from(decodeBase64(value));
  const listeners = new Set();
  class MemoryPort {
    closed = false;
    started = false;
    onmessage = null;
    onmessageerror = null;
    listeners = new Map();
    messages = [];

    start() {
      this.started = true;
    }

    addEventListener(type, listener) {
      if (!this.listeners.has(type)) this.listeners.set(type, new Set());
      this.listeners.get(type).add(listener);
    }

    close() {
      if (this.closed) return;
      this.closed = true;
      for (const listener of this.listeners.get('close') ?? []) listener();
    }

    postMessage(data) {
      if (this.closed) throw new Error('PORT_CLOSED');
      this.messages.push(data);
      if (this.peer.started && !this.peer.closed)
        this.peer.onmessage?.({ data });
    }
  }

  globalThis.MessageChannel = class {
    constructor() {
      this.port1 = new MemoryPort();
      this.port2 = new MemoryPort();
      this.port1.peer = this.port2;
      this.port2.peer = this.port1;
    }
  };
  globalThis.window = globalThis;
  globalThis.location = { origin: 'http://127.0.0.1' };
  globalThis.addEventListener = (type, listener) => {
    if (type === 'message') listeners.add(listener);
  };
  globalThis.removeEventListener = (type, listener) => {
    if (type === 'message') listeners.delete(listener);
  };
  globalThis.postMessage = (data, origin, ports = []) => {
    if (origin !== location.origin) throw new Error('ORIGIN_INVALID');
    fixture.posted.push({ data, ports });
    fixture.broadcast(data, ports);
  };
  globalThis.fixture = {
    sent: [],
    fatal: [],
    posted: [],
    themeListeners: new Set(),
    themeUnsubscribed: 0,
    theme: 'dark',
    workerListeners: new Map(),
    workerUnsubscribed: 0,
    broadcast(data, ports = [], source = window) {
      for (const listener of listeners) listener({ data, ports, source });
    },
    emitTheme() {
      for (const listener of this.themeListeners) listener();
    },
  };
  globalThis.electronBridge = {
    getSystemThemeVariant: () => fixture.theme,
    subscribeToSystemThemeVariant(listener) {
      fixture.themeListeners.add(listener);
      return () => {
        fixture.themeUnsubscribed++;
        fixture.themeListeners.delete(listener);
      };
    },
    subscribeToWorkerMessages(worker, listener) {
      fixture.workerListeners.set(worker, listener);
      return () => {
        fixture.workerUnsubscribed++;
        fixture.workerListeners.delete(worker);
      };
    },
  };
  globalThis.__cwbSend = (bytes) => fixture.sent.push(bytes);
  globalThis.__cwbFatal = (code) => {
    fixture.fatal.push(code);
    throw new Error(code);
  };
}

function createHost(t) {
  const context = vm.createContext({
    TextEncoder,
    TextDecoder,
    encodeBase64: (value) => Buffer.from(value).toString('base64'),
    decodeBase64: (value) => Buffer.from(value, 'base64'),
  });
  const run = (source) => vm.runInContext(source, context, { timeout: 5000 });
  run(`(${installEnvironment.toString()})()`);
  transport.runInContext(context, { timeout: 5000 });
  const host = context.__cwbHost;
  const fixture = context.fixture;
  t.after(() => {
    host.dispose();
    assert.equal(fixture.fatal.length, 0);
    assert.equal(fixture.themeListeners.size, 0);
    assert.equal(fixture.workerListeners.size, 0);
  });
  return {
    host,
    fixture,
    run,
    frames: () => Array.from(fixture.sent, (bytes) => codec.decode(Buffer.from(bytes, 'base64'))),
    receive: (message) => host.receive(codec.encode(message).toString('base64')),
  };
}

test('idle 连续接收 2500 条广播时保持空队列并关闭端口', (t) => {
  const { host, fixture, run } = createHost(t);
  run(`
    fixture.idlePorts = [];
    for (let index = 0; index < 2500; index++) {
      const channel = new MessageChannel();
      fixture.idlePorts.push(channel.port1);
      fixture.broadcast({ type: 'codex_desktop:git-worker-port', index }, [channel.port1], index % 2 ? window : null);
    }
  `);
  assert.deepEqual({ ...host.status() }, {
    state: 'idle', generation: 0, queued: 0, queuedBytes: 0,
    ports: 0, subscriptions: 0, themeSubscribed: false,
  });
  assert.ok(fixture.idlePorts.every((port) => port.closed && !port.started));
  assert.equal(fixture.sent.length, 0);
});

for (const state of ['buffering', 'active']) {
  test(`${state} 安全忽略无法序列化的数据和非白名单端口`, (t) => {
    const { host, fixture, run, frames } = createHost(t);
    host.reset(1);
    if (state === 'active') host.activate();
    run(`
      fixture.retained = new MessageChannel();
      fixture.broadcast({ type: 'codex_desktop:git-worker-port', handshake: true }, [fixture.retained.port1]);
    `);
    const before = { ...host.status() };
    const sentBefore = fixture.sent.length;
    run(`
      fixture.rejectedPorts = [];
      const values = [() => {}, Symbol('value'), new WeakMap(), Promise.resolve(), new MessageChannel().port1];
      for (const value of values) {
        fixture.broadcast({ value });
        const channel = new MessageChannel();
        fixture.rejectedPorts.push(channel.port1);
        fixture.broadcast({ type: 'codex_desktop:git-worker-port', nested: { value } }, [channel.port1]);
      }
      for (const type of ['unlisted-channel', undefined]) {
        const first = new MessageChannel();
        const second = new MessageChannel();
        fixture.rejectedPorts.push(first.port1, second.port1);
        fixture.broadcast({ type, value: 'broadcast' }, [first.port1, second.port1]);
      }
    `);
    assert.deepEqual({ ...host.status() }, before);
    assert.equal(fixture.sent.length, sentBefore);
    assert.ok(fixture.rejectedPorts.every((port) => port.closed && !port.started));
    assert.equal(fixture.retained.port1.closed, false);
    assert.equal(fixture.retained.port1.started, true);
    if (state === 'buffering') host.activate();
    assert.equal(frames().length, 1);
    assert.equal(frames()[0].data.handshake, true);
  });
}

test('reset、队列淘汰、activate 和 idle 保持顺序并释放资源', async (t) => {
  const { host, fixture, run, frames, receive } = createHost(t);
  host.reset(1);
  assert.equal(host.status().state, 'buffering');
  assert.equal(fixture.themeListeners.size, 1);
  run(`
    fixture.handshakes = [];
    for (let index = 0; index < 2; index++) {
      const channel = new MessageChannel();
      fixture.handshakes.push(channel);
      fixture.broadcast({ type: 'codex_desktop:git-worker-port', handshake: index }, [channel.port1]);
    }
    fixture.handshakes[0].port2.postMessage({ step: 'buffered-port-data' });
    fixture.emitTheme();
    for (let index = 0; index < 1100; index++) fixture.broadcast({ index });
  `);
  assert.equal(host.status().queued, 1024);
  assert.ok(host.status().queuedBytes > 0);
  assert.equal(host.status().ports, 2);
  assert.equal(fixture.sent.length, 0);
  assert.ok(fixture.handshakes.every(({ port1 }) => port1.started && !port1.closed));
  host.activate();
  const forwarded = frames();
  assert.equal(forwarded.length, 1024);
  assert.ok(forwarded.every((message) => message.generation === 1));
  assert.deepEqual(forwarded.slice(0, 2).map((message) => message.data.handshake), [0, 1]);
  assert.deepEqual(forwarded.slice(0, 2).map((message) => message.ports), [['host-1'], ['host-2']]);
  assert.equal(forwarded[2].kind, 'port-data');
  assert.equal(forwarded[2].portId, 'host-1');
  assert.deepEqual(forwarded[2].data, { step: 'buffered-port-data' });
  assert.equal(forwarded[3].kind, 'theme');
  assert.equal(forwarded[3].value, 'dark');
  assert.deepEqual(forwarded.slice(4).map((message) => message.data.index), Array.from({ length: 1020 }, (_, index) => index + 80));
  assert.equal(host.status().queued, 0);
  assert.equal(host.status().queuedBytes, 0);
  run(`fixture.broadcast({ step: 'active' }); fixture.emitTheme();`);
  assert.equal(frames()[1024].data.step, 'active');
  assert.equal(frames()[1025].kind, 'theme');

  await receive({ kind: 'port-open', portId: 'remote-1', channelType: 'connect-app-host', metadata: {}, generation: 1 });
  assert.equal(host.status().ports, 3);
  assert.equal(fixture.posted.length, 1);
  const remotePort = fixture.posted[0].ports[0].peer;
  assert.equal(remotePort.started, true);
  await receive({ kind: 'subscribe-worker', worker: 'git', generation: 1 });
  assert.equal(host.status().subscriptions, 1);
  const sentBeforeIdle = fixture.sent.length;
  host.idle();
  assert.deepEqual({ ...host.status() }, {
    state: 'idle', generation: 1, queued: 0, queuedBytes: 0,
    ports: 0, subscriptions: 0, themeSubscribed: false,
  });
  assert.ok(fixture.handshakes.every(({ port1 }) => port1.closed));
  assert.equal(remotePort.closed, true);
  assert.equal(fixture.themeUnsubscribed, 1);
  assert.equal(fixture.workerUnsubscribed, 1);
  run(`fixture.emitTheme(); fixture.broadcast({ step: 'idle' });`);
  assert.equal(fixture.sent.length, sentBeforeIdle);

  host.reset(2);
  run(`
    fixture.pending = new MessageChannel();
    fixture.broadcast({ type: 'codex_desktop:git-worker-port' }, [fixture.pending.port1]);
    fixture.broadcast({ step: 'pending' });
  `);
  assert.equal(host.status().queued, 2);
  host.idle();
  assert.equal(host.status().queued, 0);
  assert.equal(host.status().queuedBytes, 0);
  assert.equal(fixture.pending.port1.closed, true);
  assert.equal(fixture.themeUnsubscribed, 2);
  host.reset(3);
  host.activate();
  assert.equal(fixture.sent.length, sentBeforeIdle);
});
