import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { mkdir, writeFile } from 'node:fs/promises';
import net from 'node:net';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { setTimeout as delay } from 'node:timers/promises';
import { _electron } from '../../web/node_modules/playwright/index.mjs';

const root = fileURLToPath(new URL('../../', import.meta.url));
const mode = process.env.REMOTE_CODEX_SMOKE_MODE ?? 'custom';
assert.ok(['custom', 'quick'].includes(mode));
const directory = path.join(
  root,
  '.work',
  mode === 'quick' ? 'desktop-quick-smoke' : 'desktop-smoke',
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
await mkdir(directory, { recursive: true });
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

try {
  for (const port of ports)
    assert.equal(await portOpen(port), false, `port ${port} must be available`);
  application = await _electron.launch({
    executablePath,
    env,
    timeout: 60_000,
  });
  const window = await application.firstWindow();
  await window.waitForURL('http://127.0.0.1:19866/**');
  await window.locator('#gate').waitFor({ state: 'hidden', timeout: 30_000 });
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
  assert.equal(await window.title(), 'remote-codex 控制面板');
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
  assert.ok(
    results.doctor.checks.every((check) => check.ok),
    JSON.stringify(results.doctor),
  );
  report.checks.push(
    'packaged_node_frida_discovery',
    'sidecar_status',
    'doctor',
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
  await application.evaluate(({ BrowserWindow }) =>
    BrowserWindow.getAllWindows()[0].close(),
  );
  assert.equal(
    await application.evaluate(({ BrowserWindow }) =>
      BrowserWindow.getAllWindows()[0].isVisible(),
    ),
    false,
  );
  assert.ok(await portOpen(19866));
  await application.evaluate(({ app }) => app.emit('activate'));
  await window.waitForFunction(() => document.visibilityState === 'visible');
  report.checks.push(
    'close_hides_window',
    'background_service_survives',
    'reopen_window',
  );
  if (mode === 'custom') {
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
    report.checks.push('listener_hot_switch', 'desktop_origin_updated');
  }
  report.success = true;
} finally {
  const ownedPIDs = application
    ? await processTree(application.process().pid)
    : [];
  if (protectedPID) assert.ok(!ownedPIDs.includes(protectedPID));
  if (application) await application.close();
  const deadline = Date.now() + 35_000;
  while (
    ((await Promise.all(ports.map(portOpen))).some(Boolean) ||
      ownedPIDs.some(processExists)) &&
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
