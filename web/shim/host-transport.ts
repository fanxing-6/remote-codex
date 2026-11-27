import { Encoder } from 'cbor-x';
import {
  channels,
  callMethods,
  maxFrame,
  maxQueue,
  maxPorts,
  maxPending,
  assertSerializable,
} from '../shared';

const root = globalThis as any;
const codec = new Encoder({ structuredClone: true, useRecords: false });
const ports = new Map<string, MessagePort>();
const subscriptions = new Map<string, () => void>();
const closedPorts = new Set<string>();
let generation = 0,
  sequence = 0,
  pending = 0,
  epoch = 0,
  initialBytes = 0;
let state: 'idle' | 'buffering' | 'active' = 'idle';
let unsubscribeTheme: (() => void) | undefined;
const initial: { bytes: Uint8Array; droppable: boolean }[] = [];

const encode = (message: any) => {
  assertSerializable(message);
  const bytes = codec.encode({ ...message, generation });
  if (bytes.byteLength > maxFrame) throw new Error('FRAME_LIMIT');
  return bytes;
};

const send = (message: any) => {
  if (state === 'idle') return false;
  const bytes = encode(message);
  if (state === 'active') {
    root.__cwbSend(bytes.toBase64());
    return true;
  }
  // 带端口的握手及端口数据保持顺序；普通广播可以在队列达到上限时释放。
  while (initial.length >= 1024 || initialBytes + bytes.byteLength > maxQueue) {
    const index = initial.findIndex((item) => item.droppable);
    if (index < 0) return false;
    initialBytes -= initial[index].bytes.byteLength;
    initial.splice(index, 1);
  }
  initial.push({
    bytes: bytes.slice(),
    droppable: message.kind === 'window-message' && !message.ports.length,
  });
  initialBytes += bytes.byteLength;
  return true;
};

function attach(id: string, port: MessagePort, channelType: string) {
  if (
    ports.has(id) ||
    closedPorts.has(id) ||
    ports.size >= maxPorts ||
    !channels.includes(channelType)
  )
    throw new Error('PORT_LIMIT_OR_TYPE');
  ports.set(id, port);
  port.onmessage = (event) => {
    if (ports.get(id) !== port || state === 'idle') return;
    try {
      send({ kind: 'port-data', portId: id, channelType, data: event.data });
    } catch {
      root.__cwbFatal('UNSUPPORTED_PORT_DATA');
    }
  };
  port.onmessageerror = () => {
    if (ports.get(id) === port && state !== 'idle')
      root.__cwbFatal('PORT_MESSAGE_ERROR');
  };
  port.addEventListener('close', () => {
    if (ports.get(id) !== port) return;
    ports.delete(id);
    closedPorts.add(id);
    if (closedPorts.size > 1024) {
      root.__cwbFatal('PORT_LIMIT_OR_TYPE');
      return;
    }
    send({ kind: 'port-close', portId: id, channelType });
  });
  port.start();
}

function idle() {
  state = 'idle';
  epoch++;
  initial.length = 0;
  initialBytes = 0;
  const previous = [...ports.values()];
  ports.clear();
  closedPorts.clear();
  for (const port of previous) port.close();
  for (const unsubscribe of subscriptions.values()) unsubscribe();
  subscriptions.clear();
  unsubscribeTheme?.();
  unsubscribeTheme = undefined;
}

const forward = (event: MessageEvent) => {
  if (event.source !== null && event.source !== window) return;
  const close = () => {
    for (const port of event.ports) port.close();
  };
  if (state === 'idle') {
    close();
    return;
  }
  const channelType = event.data?.type;
  // 这两个通道由 receive 创建，交给 preload 接收转移的端口。
  if (channels.slice(0, 2).includes(channelType)) return;
  if (
    event.ports.length &&
    (!channels.includes(channelType) ||
      ports.size + event.ports.length > maxPorts)
  ) {
    close();
    return;
  }
  const ids = event.ports.map(() => 'host-' + ++sequence);
  const message = {
    kind: 'window-message',
    data: event.data,
    channelType,
    ports: ids,
  };
  // 宿主内部对象可能无法通过 CBOR 传输，验证成功后才接管端口。
  try {
    encode(message);
  } catch {
    close();
    return;
  }
  for (const [index, port] of event.ports.entries())
    attach(ids[index], port, channelType);
  if (!send(message)) {
    for (const id of ids) ports.delete(id);
    close();
  }
};

window.addEventListener('message', forward);
root.__cwbHost = {
  status() {
    return {
      state,
      generation,
      queued: initial.length,
      queuedBytes: initialBytes,
      ports: ports.size,
      subscriptions: subscriptions.size,
      themeSubscribed: !!unsubscribeTheme,
    };
  },
  reset(next: number) {
    idle();
    generation = next;
    state = 'buffering';
    unsubscribeTheme = root.electronBridge.subscribeToSystemThemeVariant(() =>
      send({
        kind: 'theme',
        value: root.electronBridge.getSystemThemeVariant(),
      }),
    );
  },
  idle,
  activate() {
    if (state !== 'buffering') throw new Error('HOST_NOT_BUFFERING');
    state = 'active';
    const messages = initial.splice(0);
    initialBytes = 0;
    for (const { bytes } of messages) root.__cwbSend(bytes.toBase64());
  },
  async receive(base64: string) {
    if (state === 'idle') return;
    const bytes = Uint8Array.fromBase64(base64);
    if (bytes.byteLength > maxFrame) throw new Error('FRAME_LIMIT');
    const message = codec.decode(bytes);
    if (message.generation !== generation) throw new Error('STALE_GENERATION');
    if (message.kind === 'port-open') {
      if (!channels.slice(0, 2).includes(message.channelType))
        throw new Error('CHANNEL_UNKNOWN');
      const channel = new MessageChannel();
      attach(message.portId, channel.port1, message.channelType);
      const data = {
        ...message.metadata,
        type: message.channelType,
        ...(message.channelType === 'connect-app-host'
          ? { port: channel.port2 }
          : {}),
      };
      window.postMessage(data, location.origin, [channel.port2]);
      return;
    }
    if (message.kind === 'port-data') {
      const port = ports.get(message.portId);
      if (!port) throw new Error('PORT_UNKNOWN');
      port.postMessage(message.data);
      return;
    }
    if (message.kind === 'port-close') {
      const port = ports.get(message.portId);
      if (!port) {
        if (closedPorts.has(message.portId)) return;
        throw new Error('PORT_UNKNOWN');
      }
      ports.delete(message.portId);
      closedPorts.add(message.portId);
      port.close();
      return;
    }
    if (message.kind === 'call') {
      if (!callMethods.includes(message.method) || pending >= maxPending)
        throw new Error('METHOD_DENIED');
      pending++;
      const current = epoch;
      let timer: ReturnType<typeof setTimeout>;
      Promise.race([
        Promise.resolve().then(() =>
          root.electronBridge[message.method](...message.args),
        ),
        new Promise((_, reject) => {
          timer = setTimeout(() => reject(new Error('CALL_TIMEOUT')), 30000);
        }),
      ])
        .then((value) => {
          if (current === epoch)
            send({ kind: 'result', id: message.id, value });
        })
        .catch(() => {
          if (current === epoch)
            send({ kind: 'error', id: message.id, code: 'HOST_CALL_FAILED' });
        })
        .finally(() => {
          clearTimeout(timer);
          pending--;
        });
      return;
    }
    if (message.kind === 'subscribe-worker') {
      if (message.worker !== 'git' || subscriptions.has(message.worker))
        throw new Error('WORKER_DENIED');
      subscriptions.set(
        message.worker,
        root.electronBridge.subscribeToWorkerMessages(message.worker, (data) =>
          send({ kind: 'worker-message', worker: message.worker, data }),
        ),
      );
      return;
    }
    if (message.kind === 'unsubscribe-worker') {
      subscriptions.get(message.worker)?.();
      subscriptions.delete(message.worker);
      return;
    }
    throw new Error('MESSAGE_KIND_UNKNOWN');
  },
  dispose() {
    idle();
    window.removeEventListener('message', forward);
  },
};
if (root.__cwbCapture) {
  window.removeEventListener('message', root.__cwbCapture);
  delete root.__cwbCapture;
}
