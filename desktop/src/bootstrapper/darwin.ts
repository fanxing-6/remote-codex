import { access } from 'node:fs/promises';
import { setTimeout as delay } from 'node:timers/promises';
import { once } from 'node:events';
import WebSocket from 'ws';
import {
  agentSource,
  assertOptions,
  installExpression,
  portOpen,
  run,
  waitFor,
  withAgent,
  type AttachOptions,
  type ProcessIdentity,
  type ShimEndpoint,
} from './common.js';

const executablePath = '/Applications/ChatGPT.app/Contents/MacOS/ChatGPT';

export async function discover(): Promise<ProcessIdentity> {
  const output = await run('/bin/ps', [
    '-U',
    String(process.getuid!()),
    '-o',
    'pid=,comm=',
  ]);
  const candidates = output
    .split('\n')
    .map((line) => /^\s*(\d+)\s+(.+)$/.exec(line))
    .filter((match) => match?.[2] === executablePath);
  if (candidates.length !== 1) throw new Error('MAIN_PROCESS_NOT_UNIQUE');
  const pid = Number(candidates[0]![1]);
  const files = await run('/usr/sbin/lsof', [
    '-a',
    '-p',
    String(pid),
    '-d',
    'txt',
    '-Fn',
  ]);
  if (!files.split('\n').includes(`n${executablePath}`))
    throw new Error('EXECUTABLE_IDENTITY_INVALID');
  const started = await run('/bin/ps', ['-p', String(pid), '-o', 'lstart=']);
  const startedAt = Date.parse(`${started.trim()} UTC`) / 1000;
  if (!Number.isFinite(startedAt))
    throw new Error('PROCESS_START_TIME_INVALID');
  const info = JSON.parse(
    await run('/usr/bin/plutil', [
      '-convert',
      'json',
      '-o',
      '-',
      '/Applications/ChatGPT.app/Contents/Info.plist',
    ]),
  );
  if (typeof info.CFBundleShortVersionString !== 'string')
    throw new Error('APP_VERSION_MISSING');
  const asarPath = '/Applications/ChatGPT.app/Contents/Resources/app.asar';
  await access(asarPath);
  return {
    pid,
    startedAt,
    executablePath,
    appVersion: info.CFBundleShortVersionString,
    asarPath,
  };
}

async function listeners(pid: number): Promise<Set<number>> {
  const output = await run(
    '/usr/sbin/lsof',
    ['-nP', '-a', '-p', String(pid), '-iTCP', '-sTCP:LISTEN', '-Fn'],
    [0, 1],
  );
  return new Set(
    output
      .split('\n')
      .filter((line) => line.startsWith('n'))
      .map((line) => {
        const match = /:(\d+)$/.exec(line);
        if (!match) throw new Error('LISTENER_ADDRESS_INVALID');
        if (Number(match[1]) === 9229 && line !== 'n127.0.0.1:9229')
          throw new Error('INSPECTOR_BIND_INVALID');
        return Number(match[1]);
      }),
  );
}

class Inspector {
  private sequence = 0;
  private pending = new Map<
    number,
    {
      resolve: (value: unknown) => void;
      reject: (error: Error) => void;
      timer: NodeJS.Timeout;
    }
  >();

  private constructor(private socket: WebSocket) {
    socket.on('message', (data) => {
      const response = JSON.parse(data.toString());
      const pending = this.pending.get(response.id);
      if (!pending) return;
      this.pending.delete(response.id);
      clearTimeout(pending.timer);
      if (response.error || response.result?.exceptionDetails)
        pending.reject(new Error('INSPECTOR_EVALUATION_FAILED'));
      else pending.resolve(response.result?.result?.value);
    });
    const rejectPending = () => {
      for (const item of this.pending.values()) {
        clearTimeout(item.timer);
        item.reject(new Error('INSPECTOR_DISCONNECTED'));
      }
      this.pending.clear();
    };
    socket.on('error', rejectPending);
    socket.on('close', rejectPending);
  }

  static async connect(pid: number): Promise<Inspector> {
    if (!(await listeners(pid)).has(9229))
      throw new Error('INSPECTOR_NOT_OWNED');
    const response = await fetch('http://127.0.0.1:9229/json/list', {
      signal: AbortSignal.timeout(5000),
    });
    if (!response.ok) throw new Error('INSPECTOR_DISCOVERY_FAILED');
    const targets = (await response.json()) as {
      title: string;
      webSocketDebuggerUrl: string;
    }[];
    if (
      targets.length !== 1 ||
      targets[0].title !== 'electron/js2c/browser_init'
    )
      throw new Error('INSPECTOR_TARGET_INVALID');
    const url = new URL(targets[0].webSocketDebuggerUrl);
    if (url.protocol !== 'ws:' || url.host !== '127.0.0.1:9229')
      throw new Error('INSPECTOR_ENDPOINT_INVALID');
    const socket = new WebSocket(url, { handshakeTimeout: 5000 });
    await once(socket, 'open');
    const inspector = new Inspector(socket);
    if ((await inspector.evaluate('process.pid')) !== pid) {
      await inspector.close();
      throw new Error('INSPECTOR_PID_INVALID');
    }
    return inspector;
  }

  evaluate(expression: string): Promise<unknown> {
    const id = ++this.sequence;
    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => {
        this.pending.delete(id);
        reject(new Error('INSPECTOR_EVALUATION_TIMEOUT'));
      }, 20_000);
      this.pending.set(id, { resolve, reject, timer });
      this.socket.send(
        JSON.stringify({
          id,
          method: 'Runtime.evaluate',
          params: { expression, awaitPromise: true, returnByValue: true },
        }),
      );
    });
  }

  async close(): Promise<void> {
    if (this.socket.readyState === WebSocket.CLOSED) return;
    const closed = once(this.socket, 'close');
    this.socket.close();
    await closed;
  }
}

export async function attach(
  identity: ProcessIdentity,
  bundle: string,
  options: AttachOptions,
): Promise<ShimEndpoint> {
  assertOptions(identity, options);
  if (await portOpen(9229)) throw new Error('INSPECTOR_ALREADY_OPEN');
  const before = await listeners(identity.pid);
  const begin = performance.now();
  let firstOpen: number | undefined;
  let firstClosed: number | undefined;
  let watching = true;
  const watcher = (async () => {
    while (watching) {
      const open = await portOpen(9229);
      if (open && firstOpen === undefined) firstOpen = performance.now();
      if (!open && firstOpen !== undefined && firstClosed === undefined)
        firstClosed = performance.now();
      await delay(5);
    }
  })();
  let inspector: Inspector | undefined;
  try {
    await withAgent(
      identity.pid,
      await agentSource('darwin_inspector.js'),
      async (script) => {
        await script.load();
        const result = (await script.exports.enable()) as {
          pid: number;
          enabled: boolean;
          alreadyOpen: boolean;
        };
        if (
          result.pid !== identity.pid ||
          !result.enabled ||
          result.alreadyOpen
        )
          throw new Error('INSPECTOR_IDENTITY_INVALID');
      },
    );
    await waitFor(
      async () =>
        (await listeners(identity.pid)).has(9229) ? true : undefined,
      10_000,
      'INSPECTOR_START_TIMEOUT',
    );
    inspector = await Inspector.connect(identity.pid);
    if ((await inspector.evaluate(installExpression(bundle, options))) !== true)
      throw new Error('INSTALL_REJECTED');
  } finally {
    try {
      if (!inspector && (await listeners(identity.pid)).has(9229))
        inspector = await Inspector.connect(identity.pid);
      if (inspector) {
        try {
          await inspector.evaluate(
            "setTimeout(() => process.getBuiltinModule('inspector').close(), 0); true",
          );
        } finally {
          await inspector.close();
        }
      }
      await waitFor(
        async () => (!(await portOpen(9229)) ? true : undefined),
        5000,
        'INSPECTOR_NOT_CLOSED',
      );
      await delay(20);
    } finally {
      watching = false;
      await watcher;
    }
  }
  if (firstOpen === undefined || firstClosed === undefined)
    throw new Error('INSPECTOR_MEASUREMENT_MISSING');
  const port = await waitFor(
    async () => {
      const ports = [...(await listeners(identity.pid))].filter(
        (value) => !before.has(value),
      );
      return ports.length === 1 ? ports[0] : undefined;
    },
    5000,
    'IPC_LISTENER_NOT_UNIQUE',
  );
  const current = await discover();
  if (current.pid !== identity.pid || current.startedAt !== identity.startedAt)
    throw new Error('PROCESS_CHANGED');
  return {
    ...identity,
    url: `ws://127.0.0.1:${port}/internal`,
    inspectorClosed: true,
    inspectorOpenMs: firstClosed - firstOpen,
    bootstrapElapsedMs: performance.now() - begin,
  };
}
