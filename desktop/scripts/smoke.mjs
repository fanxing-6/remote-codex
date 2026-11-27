import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { randomBytes } from 'node:crypto';
import {
  mkdir,
  mkdtemp,
  readFile,
  realpath,
  writeFile,
} from 'node:fs/promises';
import net from 'node:net';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { setTimeout as delay } from 'node:timers/promises';
import { _electron } from '../../web/node_modules/playwright/index.mjs';

const root = fileURLToPath(new URL('../../', import.meta.url));
const mode = process.env.REMOTE_CODEX_SMOKE_MODE ?? 'custom';
assert.ok(['custom', 'quick'].includes(mode));
await mkdir(path.join(root, '.work', 'tmp'), { recursive: true });
const directory = await mkdtemp(
  path.join(root, '.work', `desktop-${mode}-smoke-`),
);
const executablePath =
  process.env.REMOTE_CODEX_SMOKE_APP ??
  path.join(
    root,
    'desktop',
    'release',
    'mac-arm64',
    'Remote Codex.app',
    'Contents',
    'MacOS',
    'Remote Codex',
  );
assert.ok(
  (await realpath(executablePath)).startsWith(`${path.join(root, 'desktop')}/`),
  'smoke executable must belong to this checkout',
);
const config = {
  tunnelMode: mode,
  quickTunnelProtocol: 'http2',
  publicOrigin: mode === 'quick' ? '' : 'http://127.0.0.1:19865',
  allowedHosts: mode === 'quick' ? [] : ['127.0.0.1:19865'],
  dataPort: 19865,
  adminPort: 19866,
  tlsPort: 0,
  tlsAutocert: false,
  auth: { mode: 'password' },
  allowInsecureLocalhost: mode !== 'quick',
  trustedProxy: ['127.0.0.1/32'],
};
await writeFile(
  path.join(directory, 'config.json'),
  JSON.stringify(config, null, 2),
  { mode: 0o600 },
);
const env = {
  ...process.env,
  REMOTE_CODEX_USER_DATA: directory,
  REMOTE_CODEX_MANUAL_HOST: '1',
  TMPDIR: path.join(root, '.work', 'tmp'),
};
delete env.ELECTRON_RUN_AS_NODE;
const report = { success: false, checks: [] };
const errors = [];
const unauthorized = [];
const bootstrapCodes = new Set();
const requests = new Map();
const ownedPIDs = new Set();
let application;
let sidecarPID;
let tunnelPID;
const protectedPID = process.env.REMOTE_CODEX_PROTECTED_PID
  ? Number(process.env.REMOTE_CODEX_PROTECTED_PID)
  : undefined;
if (protectedPID !== undefined)
  assert.ok(Number.isSafeInteger(protectedPID) && protectedPID > 0);
const production = protectedPID ? await processStart(protectedPID) : undefined;
const ports = [19865, 19866, 19875, 19876];

async function processTree(rootPID) {
  const child = spawn('/bin/ps', ['-axo', 'pid=,ppid=']);
  const output = [];
  child.stdout.on('data', (chunk) => output.push(chunk));
  const code = await new Promise((resolve, reject) => {
    child.once('error', reject);
    child.once('close', resolve);
  });
  assert.equal(code, 0);
  const processes = Buffer.concat(output)
    .toString()
    .trim()
    .split('\n')
    .map((line) => line.trim().split(/\s+/).map(Number));
  const result = new Set([rootPID]);
  let changed;
  do {
    changed = false;
    for (const [pid, parent] of processes) {
      if (result.has(parent) && !result.has(pid)) {
        result.add(pid);
        changed = true;
      }
    }
  } while (changed);
  return [...result];
}

function processExists(pid) {
  try {
    process.kill(pid, 0);
    return true;
  } catch (error) {
    if (error.code === 'ESRCH') return false;
    throw error;
  }
}

async function processStart(pid) {
  const child = spawn('/bin/ps', ['-p', String(pid), '-o', 'pid=,lstart=']);
  const output = [];
  child.stdout.on('data', (chunk) => output.push(chunk));
  const code = await new Promise((resolve, reject) => {
    child.once('error', reject);
    child.once('close', resolve);
  });
  assert.equal(code, 0);
  return Buffer.concat(output).toString().trim();
}

async function portOpen(port) {
  return new Promise((resolve) => {
    const socket = net.connect(port, '127.0.0.1');
    socket.once('connect', () => {
      socket.destroy();
      resolve(true);
    });
    socket.once('error', () => resolve(false));
  });
}

async function waitUntil(check, message, timeout = 15_000) {
  const deadline = Date.now() + timeout;
  do {
    if (await check()) return;
    await delay(100);
  } while (Date.now() < deadline);
  assert.fail(message);
}

async function rememberProcesses() {
  for (const pid of await processTree(application.process().pid))
    ownedPIDs.add(pid);
}

function watchWindow(window) {
  window.on('pageerror', (error) => errors.push(error.message));
}

async function authenticated(window, port) {
  await window.waitForURL(`http://127.0.0.1:${port}/**`);
  await window.locator('#gate').waitFor({ state: 'hidden', timeout: 30_000 });
  await window.waitForFunction(
    () => document.querySelector('#logs').textContent !== '等待读取日志',
  );
  assert.equal(await window.title(), 'remote-codex 控制面板');
  assert.equal(await window.locator('#notice').textContent(), '');
  assert.deepEqual(errors, []);
  assert.deepEqual(unauthorized, []);
  await rememberProcesses();
}

async function closeWindow(window, port) {
  const rendererPID = await application.evaluate(({ BrowserWindow }) =>
    BrowserWindow.getAllWindows()[0].webContents.getOSProcessId(),
  );
  await application.evaluate(({ BrowserWindow }) =>
    BrowserWindow.getAllWindows()[0].close(),
  );
  await waitUntil(
    () => window.isClosed() && !processExists(rendererPID),
    'closed renderer must exit',
  );
  const idle = await application.evaluate(
    ({ app, BrowserWindow, webContents }) => ({
      windows: BrowserWindow.getAllWindows().length,
      contents: webContents.getAllWebContents().length,
      dockVisible: app.dock?.isVisible(),
      metrics: app.getAppMetrics().map(({ pid, type }) => ({ pid, type })),
    }),
  );
  assert.equal(idle.windows, 0);
  assert.equal(idle.contents, 0);
  if (process.platform === 'darwin') assert.equal(idle.dockVisible, false);
  assert.deepEqual(
    idle.metrics.filter(({ type }) => type === 'Tab' || type === 'GPU'),
    [],
    JSON.stringify(idle.metrics),
  );
  report.idle = idle;
  const before = [...requests];
  await delay(5500);
  assert.deepEqual([...requests], before, 'closed panel must stop polling');
  const credentials = JSON.parse(
    await readFile(path.join(directory, 'runtime', 'cli-session.json'), 'utf8'),
  );
  const response = await fetch(`http://127.0.0.1:${port}/api/status`, {
    headers: { Cookie: `cwb_admin=${credentials.id}` },
  });
  assert.equal(response.status, 200);
  assert.equal((await response.json()).pid, sidecarPID);
  assert.ok(processExists(sidecarPID));
}

async function reopenWindow(port, menu = false) {
  await application.context().clearCookies();
  const created = application.waitForEvent('window');
  await application.evaluate(({ app, Menu }, menu) => {
    if (menu) {
      const item = Menu.getApplicationMenu().items[0].submenu.items.find(
        (item) => item.label === '打开控制面板',
      );
      item.click();
    } else {
      // 连续激活必须复用同一个新窗口与引导请求。
      app.emit('activate');
      app.emit('activate');
    }
  }, menu);
  const window = await created;
  await authenticated(window, port);
  assert.equal(
    await application.evaluate(
      ({ BrowserWindow }) => BrowserWindow.getAllWindows().length,
    ),
    1,
  );
  if (process.platform === 'darwin')
    assert.equal(
      await application.evaluate(({ app }) => app.dock.isVisible()),
      true,
    );
  const boundary = await application.evaluate(async ({ session }, port) => {
    const allowed = await session.defaultSession.fetch(
      `http://127.0.0.1:${port}/styles.css`,
    );
    let blocked;
    try {
      await session.defaultSession.fetch('http://127.0.0.1:19865/');
    } catch (error) {
      blocked = error.message;
    }
    return { status: allowed.status, blocked };
  }, port);
  assert.equal(boundary.status, 200);
  assert.match(boundary.blocked, /ERR_BLOCKED_BY_CLIENT/);
  return window;
}

async function verifyVisibility(window) {
  // 在 Playwright 当前页面会话中关闭焦点模拟，使用系统窗口可见性。
  const protocol =
    window._connection.toImpl(window).delegate._mainFrameSession._client;
  await protocol.send('Emulation.setFocusEmulationEnabled', { enabled: false });
  await window.locator('[data-view="overview"]').click();
  await window.locator('#qr').waitFor({ state: 'visible' });
  const qr = await window
    .locator('#qr')
    .evaluate((canvas) => canvas.toDataURL());
  await application.evaluate(({ BrowserWindow }) =>
    BrowserWindow.getAllWindows()[0].hide(),
  );
  await window.waitForFunction(() => document.hidden, undefined, {
    polling: 100,
  });
  // 已发出的请求完成后，覆盖两个定时器的完整周期。
  await delay(1000);
  const before = [...requests];
  const hiddenAt = Date.now();
  for (let elapsed = 0; elapsed < 245_000; elapsed += 5000) await delay(5000);
  assert.deepEqual([...requests], before, 'hidden panel must stop polling');
  const status = requests.get('/api/status');
  const logs = requests.get('/api/logs');
  const tickets = requests.get('/api/auth/ticket');
  const resumedAt = Date.now();
  await application.evaluate(({ BrowserWindow }) =>
    BrowserWindow.getAllWindows()[0].show(),
  );
  await window.waitForFunction(() => !document.hidden);
  await waitUntil(
    () =>
      requests.get('/api/status') > status &&
      requests.get('/api/logs') > logs &&
      requests.get('/api/auth/ticket') > tickets,
    'visible panel must immediately sync and renew expired QR',
    4000,
  );
  await window.waitForFunction(
    (previous) => document.querySelector('#qr').toDataURL() !== previous,
    qr,
  );
  report.visibility = {
    hiddenMilliseconds: resumedAt - hiddenAt,
    resumeMilliseconds: Date.now() - resumedAt,
  };
  report.checks.push(
    'hidden_polling_paused',
    'visible_sync',
    'expired_qr_refreshed',
  );
}

try {
  for (const port of ports)
    assert.equal(await portOpen(port), false, `port ${port} must be available`);
  application = await _electron.launch({
    executablePath,
    args: executablePath.endsWith('/Electron')
      ? [path.join(root, 'desktop')]
      : [],
    env,
    timeout: 60_000,
  });
  application.on('window', watchWindow);
  for (const window of application.windows()) watchWindow(window);
  application.context().on('request', (request) => {
    const pathname = new URL(request.url()).pathname;
    requests.set(pathname, (requests.get(pathname) ?? 0) + 1);
    if (pathname === '/api/bootstrap' && request.method() === 'POST') {
      const { code } = request.postDataJSON();
      if (!code || bootstrapCodes.has(code))
        errors.push('bootstrap code must be fresh');
      bootstrapCodes.add(code);
    }
  });
  application.context().on('response', (response) => {
    if (response.status() === 401)
      unauthorized.push(new URL(response.url()).pathname);
  });
  let window = await application.firstWindow();
  await authenticated(window, 19866);
  if (mode === 'quick') {
    const deadline = Date.now() + 60_000;
    let tunnel;
    do {
      tunnel = await window.evaluate(
        async () => (await (await fetch('/api/status')).json()).tunnel,
      );
      if (tunnel.state === 'running' && tunnel.pid > 0) break;
      await delay(200);
    } while (Date.now() < deadline);
    assert.equal(tunnel.state, 'running', JSON.stringify(tunnel));
    assert.ok(tunnel.pid > 0);
  }
  const results = await window.evaluate(async () => {
    const status = await (await fetch('/api/status')).json();
    const doctor = await (await fetch('/api/doctor')).json();
    return {
      status,
      doctor,
      desktopAPI: typeof window.remoteCodexDesktop?.setAutoStart,
    };
  });
  assert.equal(results.status.bootstrapper, 'Node-Frida');
  assert.equal(results.desktopAPI, 'function');
  assert.equal(results.doctor.bootstrapper, 'Node-Frida');
  for (const label of ['独立管理端口', '宿主连接策略'])
    assert.equal(
      results.doctor.checks.find((check) => check.label === label)?.ok,
      true,
      JSON.stringify(results.doctor),
    );
  report.checks.push(
    'packaged_node_frida_discovery',
    'sidecar_status',
    'doctor_response',
    'bootstrap_login',
    'desktop_preload',
  );
  report.bootstrapper = results.status.bootstrapper;
  report.tunnel = results.status.tunnel;
  report.doctor = results.doctor.checks;
  sidecarPID = results.status.pid;
  if (mode === 'quick') {
    tunnelPID = results.status.tunnel.pid;
    assert.ok(tunnelPID > 0);
    assert.match(
      results.status.publicOrigin,
      /^https:\/\/[a-z0-9-]+\.trycloudflare\.com$/,
    );
    report.checks.push('quick_tunnel_live');
  }
  const preferences = await application.evaluate(({ BrowserWindow }) => {
    const preferences =
      BrowserWindow.getAllWindows()[0].webContents.getLastWebPreferences();
    return {
      contextIsolation: preferences.contextIsolation,
      nodeIntegration: preferences.nodeIntegration,
      sandbox: preferences.sandbox,
    };
  });
  assert.deepEqual(preferences, {
    contextIsolation: true,
    nodeIntegration: false,
    sandbox: true,
  });
  report.checks.push('secure_window_preferences');
  await window.screenshot({ path: path.join(directory, 'window.png') });
  for (let cycle = 0; cycle < 3; cycle++) {
    await closeWindow(window, 19866);
    window = await reopenWindow(19866, cycle === 1);
  }
  report.checks.push(
    'close_destroys_window',
    'idle_zero_renderer_gpu',
    'closed_polling_paused',
    'background_service_survives',
    'repeated_reopen_authenticated',
    'dock_visibility',
    'session_request_boundary',
  );
  if (mode === 'custom') {
    await window.locator('[data-view="security"]').click();
    await window
      .locator('#new-password')
      .fill(randomBytes(24).toString('base64url'));
    await window.locator('#password-form button').click();
    await window.waitForFunction(
      () =>
        document.querySelector('#notice').textContent ===
        '新密码已保存，全部设备会话和全部永久直登链接已作废。',
    );
    await verifyVisibility(window);
    await window.locator('[data-view="security"]').click();
    await window.locator('#listener-origin').fill('http://127.0.0.1:19875');
    await window.locator('#data-port').fill('19875');
    await window.locator('#admin-port').fill('19876');
    await window.locator('#listeners-form button').click();
    await window.waitForURL('http://127.0.0.1:19876/**');
    await window.locator('#gate').waitFor({ state: 'hidden', timeout: 30_000 });
    assert.equal(
      await window.evaluate(
        async () => (await (await fetch('/api/status')).json()).pid,
      ),
      sidecarPID,
    );
    for (const port of [19865, 19866])
      assert.equal(await portOpen(port), false);
    await closeWindow(window, 19876);
    window = await reopenWindow(19876);
    report.checks.push('listener_hot_switch', 'desktop_origin_updated');
  }
  assert.deepEqual(errors, []);
  assert.deepEqual(unauthorized, []);
  report.bootstrapExchanges = bootstrapCodes.size;
  assert.ok(bootstrapCodes.size >= 4);
  report.checks.push('fresh_bootstrap_codes', 'no_401', 'no_javascript_errors');
  report.success = true;
} finally {
  if (application) await rememberProcesses();
  if (protectedPID) assert.ok(!ownedPIDs.has(protectedPID));
  if (application) await application.close();
  const deadline = Date.now() + 35_000;
  while (
    ((await Promise.all(ports.map(portOpen))).some(Boolean) ||
      [...ownedPIDs].some(processExists)) &&
    Date.now() < deadline
  )
    await delay(100);
  for (const port of ports) assert.equal(await portOpen(port), false);
  for (const pid of ownedPIDs)
    assert.equal(processExists(pid), false, `test process ${pid} must exit`);
  for (const pid of [sidecarPID, tunnelPID].filter(Boolean)) {
    const child = spawn('/bin/ps', ['-p', String(pid), '-o', 'pid=']);
    const code = await new Promise((resolve) => child.once('close', resolve));
    assert.equal(code, 1);
  }
  if (protectedPID) {
    assert.equal(await processStart(protectedPID), production);
    report.checks.push('production_process_unchanged');
  }
  report.checks.push(
    'graceful_exit',
    'test_ports_closed',
    'electron_process_tree_reaped',
  );
  await writeFile(
    path.join(directory, 'report.json'),
    JSON.stringify(report, null, 2),
  );
}
console.log(JSON.stringify(report, null, 2));
