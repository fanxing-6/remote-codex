import http from 'node:http';
import { timingSafeEqual } from 'node:crypto';
import { WebSocketServer, WebSocket } from 'ws';
import { syncMethods, maxFrame, maxQueue, maxPending } from '../shared';
declare const HOST_TRANSPORT: string;
declare const EARLY_CAPTURE: string;
const electron = process.mainModule.require('electron');
const root = globalThis as any,
  key = '__cwbShimV1';
export function install(options: any) {
  if (root[key]?.isActive?.()) throw new Error('SHIM_ALREADY_CONNECTED');
  root[key]?.stop();
  if (
    electron.app.getVersion() !== options.appVersion ||
    !/^[a-f0-9]{64}$/.test(options.token)
  )
    throw new Error('IDENTITY_INVALID');
  let target: any,
    connection: any,
    stopped = false,
    disposing = false,
    ready = false,
    generation = 0,
    queue = Promise.resolve(),
    queueBytes = 0,
    queued = 0,
    configuration: any,
    errorCode = '',
    hostURL = '';
  const initialWindows = electron.BrowserWindow.getAllWindows().map(
    (w) => w.id,
  );
  const server = http.createServer((_, res) => res.writeHead(404).end());
  server.maxConnections = 8;
  server.headersTimeout = 5000;
  server.requestTimeout = 5000;
  const sockets = new WebSocketServer({
    noServer: true,
    maxPayload: maxFrame,
    maxFragments: 1024,
  });
  let grace = setTimeout(stop, 60000);
  const endpoint = () => ({
    pid: process.pid,
    startedAt: options.startedAt,
    shimInstanceId: options.instance,
    protocolVersion: 1,
    assetBuildId: options.assetBuildId,
    windowId: target?.id,
    hiddenWindowAlive: !!target && !target.isDestroyed(),
    initialWindows,
    capabilities: [
      'connect-app-host',
      'codex_desktop:connect-app-server-manager',
    ],
    ready,
    errorCode,
    configuration,
  });
  function send(value: any) {
    if (connection?.readyState === WebSocket.OPEN) {
      if (connection.bufferedAmount > maxQueue) throw new Error('QUEUE_LIMIT');
      connection.send(JSON.stringify(value));
    }
  }
  function fatal(code: string) {
    if (stopped || disposing) return;
    errorCode = code;
    ready = false;
    send({ kind: 'host-offline', errorCode: code });
    connection?.close(4010, code);
  }
  function stop() {
    if (stopped) return;
    stopped = true;
    ready = false;
    clearTimeout(grace);
    clearInterval(heartbeat);
    if (target && !target.isDestroyed()) {
      if (target.webContents.debugger.isAttached())
        target.webContents.debugger.detach();
      target.destroy();
    }
    for (const socket of sockets.clients) socket.close(1000);
    sockets.close();
    server.close();
    delete root[key];
  }
  const heartbeat = setInterval(() => {
    if (connection?.readyState === WebSocket.OPEN) connection.ping();
  }, 25000);
  server.on('upgrade', (req, socket, head) => {
    const supplied = Buffer.from(req.headers.authorization ?? ''),
      expected = Buffer.from('Bearer ' + options.token);
    if (
      req.url != '/internal' ||
      req.headers.origin !== undefined ||
      supplied.length !== expected.length ||
      !timingSafeEqual(supplied, expected) ||
      connection?.readyState === WebSocket.OPEN
    ) {
      socket.end('HTTP/1.1 403 Forbidden\r\n\r\n');
      return;
    }
    sockets.handleUpgrade(req, socket, head, (ws) => {
      connection = ws;
      clearTimeout(grace);
      let identified = false,
        lastSeen = Date.now();
      ws.on('pong', () => (lastSeen = Date.now()));
      const timeout = setInterval(() => {
        if (Date.now() - lastSeen > 60000) ws.terminate();
      }, 10000);
      ws.on('close', () => {
        clearInterval(timeout);
        if (connection !== ws) return;
        connection = null;
        grace = setTimeout(stop, 60000);
        queue = queue
          .then(async () => {
            if (
              !stopped &&
              !connection &&
              ready &&
              target &&
              !target.isDestroyed()
            )
              await target.webContents.executeJavaScript(
                'window.__cwbHost.idle()',
              );
          })
          .catch(() => fatal('HOST_PROTOCOL_FAILED'));
      });
      ws.on('error', () => fatal('IPC_SOCKET_ERROR'));
      ws.on('message', (data, binary) => {
        if (!Buffer.isBuffer(data)) {
          fatal('IPC_FRAME_INVALID');
          return;
        }
        if (queueBytes + data.length > maxQueue || queued >= 8192) {
          ws.close(1009, 'QUEUE_LIMIT');
          return;
        }
        queueBytes += data.length;
        queued++;
        queue = queue
          .then(async () => {
            if (stopped || connection !== ws) return;
            if (binary) {
              if (!identified || !ready) throw new Error('HOST_NOT_READY');
              await target.webContents.executeJavaScript(
                `window.__cwbHost.receive(${JSON.stringify(data.toString('base64'))})`,
              );
              return;
            }
            const request = JSON.parse(data.toString());
            if (request.kind === 'hello') {
              if (
                request.pid !== process.pid ||
                request.startedAt !== options.startedAt ||
                request.shimInstanceId !== options.instance ||
                request.protocolVersion !== 1 ||
                request.assetBuildId !== options.assetBuildId
              )
                throw new Error('HANDSHAKE_INVALID');
              identified = true;
            } else if (!identified) throw new Error('HANDSHAKE_REQUIRED');
            if (request.kind === 'reset') {
              if (!ready || request.generation <= generation)
                throw new Error('GENERATION_INVALID');
              await target.webContents.loadURL(hostURL);
              await target.webContents.executeJavaScript(HOST_TRANSPORT);
              await target.webContents.executeJavaScript(
                `window.__cwbHost.reset(${request.generation})`,
              );
              generation = request.generation;
            } else if (request.kind === 'activate') {
              if (request.generation !== generation)
                throw new Error('GENERATION_INVALID');
              await target.webContents.executeJavaScript(
                'window.__cwbHost.activate()',
              );
            } else if (request.kind === 'idle') {
              if (!ready || request.generation <= generation)
                throw new Error('GENERATION_INVALID');
              await target.webContents.executeJavaScript(
                'window.__cwbHost.idle()',
              );
              generation = request.generation;
            } else if (request.kind === 'probe') {
              if (ready) {
                const ok =
                  await target.webContents.executeJavaScript(
                    '!!window.__cwbHost',
                  );
                if (!ok) throw new Error('HOST_NOT_READY');
              }
            } else if (request.kind === 'stop') {
              disposing = true;
              if (target && !target.isDestroyed()) {
                if (target.webContents.debugger.isAttached())
                  target.webContents.debugger.detach();
                target.destroy();
              }
              send({
                kind: 'reply',
                id: request.id,
                value: {
                  stopped: true,
                  windowReleased: !target || target.isDestroyed(),
                  initialWindows,
                  remainingWindows: electron.BrowserWindow.getAllWindows().map(
                    (w) => w.id,
                  ),
                },
              });
              stop();
              return;
            } else if (request.kind !== 'hello')
              throw new Error('CONTROL_KIND_UNKNOWN');
            send({ kind: 'reply', id: request.id, value: endpoint() });
          })
          .catch((error) => {
            const known = [
              'STALE_GENERATION',
              'PORT_UNKNOWN',
              'METHOD_DENIED',
              'FRAME_LIMIT',
              'UNSUPPORTED_VALUE',
              'UNSUPPORTED_TRANSFERABLE',
              'CHANNEL_UNKNOWN',
              'HOST_NOT_READY',
              'GENERATION_INVALID',
              'PORT_LIMIT_OR_TYPE',
            ];
            fatal(
              known.find((code) => error.message.includes(code)) ??
                'HOST_PROTOCOL_FAILED',
            );
          })
          .finally(() => {
            queueBytes -= data.length;
            queued--;
          });
      });
    });
  });
  root[key] = {
    stop,
    isActive: () => connection?.readyState === WebSocket.OPEN,
  };
  server.listen(0, '127.0.0.1', () => {
    Promise.resolve()
      .then(async () => {
        const flatten = (items: any[]): any[] =>
          items.flatMap((item) => [
            item,
            ...flatten(item.submenu?.items ?? []),
          ]);
        const candidates = flatten(
          electron.Menu.getApplicationMenu().items,
        ).filter(
          (item) =>
            ['新窗口', '新建窗口', 'New Window'].includes(item.label) &&
            item.enabled,
        );
        if (candidates.length !== 1)
          throw new Error('WINDOW_ACTION_NOT_UNIQUE');
        const original = electron.BrowserWindow.getFocusedWindow(),
          created: any[] = [];
        let captureError: any, blockingSetup: Promise<any>;
        const capture = (_, window) => {
          try {
            created.push(window);
            if (created.length !== 1) return;
            target = window;
            const rawHide = target.hide.bind(target);
            target.setOpacity?.(0);
            target.setFocusable?.(false);
            target.setSkipTaskbar?.(true);
            target.setIgnoreMouseEvents?.(true);
            target.setBounds?.({ x: -32000, y: -32000, width: 1, height: 1 });
            rawHide();
            setImmediate(() => {
              if (target && !target.isDestroyed()) rawHide();
            });
            for (const event of [
              'show',
              'ready-to-show',
              'restore',
              'focus',
              'maximize',
            ])
              target.on(event, () => rawHide());
            for (const method of [
              'show',
              'showInactive',
              'focus',
              'restore',
              'maximize',
            ])
              target[method] = () => {};
            const wc = target.webContents;
            wc.setBackgroundThrottling(false);
            wc.debugger.attach('1.3');
            blockingSetup = (async () => {
              await wc.debugger.sendCommand('Network.enable');
              await wc.debugger.sendCommand('Network.setBlockedURLs', {
                urls: [
                  'app://-/assets/*',
                  '*/assets/*',
                  '*.js',
                  '*.mjs',
                  '*.css',
                  '*.wasm',
                ],
              });
              await wc.debugger.sendCommand('Page.enable');
              await wc.debugger.sendCommand(
                'Page.addScriptToEvaluateOnNewDocument',
                { source: EARLY_CAPTURE },
              );
            })();
          } catch (error) {
            captureError = error;
          }
        };
        electron.app.on('browser-window-created', capture);
        try {
          candidates[0].click(candidates[0], original, {});
        } finally {
          electron.app.removeListener('browser-window-created', capture);
        }
        if (captureError) throw captureError;
        if (created.length !== 1) throw new Error('WINDOW_CREATION_NOT_UNIQUE');
        if (original && !original.isDestroyed()) original.focus();
        const wc = target.webContents;
        await blockingSetup;
        const bridgeReady = `!!window.electronBridge&&${JSON.stringify(syncMethods)}.every(name=>typeof window.electronBridge[name]==='function')`;
        const initialDeadline = Date.now() + 45000;
        while (
          wc.isLoading() ||
          !wc.getURL().startsWith('app://-/index.html') ||
          !(await wc.executeJavaScript(bridgeReady))
        ) {
          if (Date.now() > initialDeadline)
            throw new Error('WINDOW_LOAD_TIMEOUT');
          await new Promise((resolve) => setTimeout(resolve, 20));
        }
        configuration = await wc.executeJavaScript(
          `(()=>{const b=window.electronBridge;return {windowType:b.windowType,usesStagingWidgetSandbox:b.usesStagingWidgetSandbox,initialReducedMotionPreference:b.initialReducedMotionPreference,initialOpaqueWindowSurfaceEnabled:b.initialOpaqueWindowSurfaceEnabled,...Object.fromEntries(${JSON.stringify(syncMethods)}.map(name=>[name,b[name]()]))}})()`,
        );
        hostURL = 'app://-/index.html?cwbHost=' + options.instance;
        await wc.loadURL(hostURL);
        const deadline = Date.now() + 45000;
        while (
          wc.isLoading() ||
          wc.getURL() !== hostURL ||
          !(await wc.executeJavaScript(bridgeReady))
        ) {
          if (Date.now() > deadline) throw new Error('WINDOW_LOAD_TIMEOUT');
          await new Promise((resolve) => setTimeout(resolve, 20));
        }
        await wc.debugger.sendCommand('Runtime.enable');
        for (const name of ['__cwbSend', '__cwbFatal'])
          await wc.debugger.sendCommand('Runtime.addBinding', { name });
        wc.debugger.on('message', (_, method, params) => {
          if (method !== 'Runtime.bindingCalled') return;
          if (params.name === '__cwbFatal') {
            fatal('HOST_TRANSFER_FAILED');
            return;
          }
          if (
            params.name !== '__cwbSend' ||
            connection?.readyState !== WebSocket.OPEN
          )
            return;
          const bytes = Buffer.from(params.payload, 'base64');
          if (
            bytes.length > maxFrame ||
            connection.bufferedAmount + bytes.length > maxQueue
          ) {
            fatal('QUEUE_LIMIT');
            return;
          }
          connection.send(bytes, { binary: true });
        });
        wc.on('render-process-gone', () => fatal('RENDERER_GONE'));
        wc.debugger.on('detach', () => {
          if (!stopped) fatal('DEBUGGER_DETACHED');
        });
        target.on('destroyed', () => {
          if (!stopped) fatal('WINDOW_DESTROYED');
        });
        await wc.executeJavaScript(HOST_TRANSPORT);
        ready = true;
      })
      .catch(() => {
        fatal('SHIM_INITIALIZATION_FAILED');
        stop();
      });
  });
  return true;
}
