import QRCode from 'qrcode';
declare global {
  interface Window {
    remoteCodexDesktop?: {
      ensureCloudflared(): Promise<void>;
      getAutoStart(): Promise<boolean>;
      setAutoStart(enabled: boolean): Promise<void>;
    };
  }
}
const $ = (id: string) => document.getElementById(id) as any;
const platformChip = document.querySelector<HTMLElement>('header .chip')!;
platformChip.textContent = '本机';
const autostart = $('autostart');
autostart.parentElement.replaceChildren(
  autostart,
  document.createTextNode('登录系统后自动启动'),
);
const listenerPanel = document.createElement('article');
listenerPanel.className = 'panel';
listenerPanel.innerHTML =
  '<h2>访问入口与监听端口</h2><p>保存后使用新的本机管理地址。设备会话将吊销，隧道规则需要重新预览。</p><form id="listeners-form"><label>公开访问 Origin<input id="listener-origin" type="url" required></label><label>数据面端口<input id="data-port" type="number" min="1024" max="65535" required></label><label>本地管理端口<input id="admin-port" type="number" min="1024" max="65535" required></label><button class="primary">保存访问设置</button></form><button id="change-auth">设置登录方式</button>';
document.querySelector('[data-panel="security"]').append(listenerPanel);
let csrf = '',
  current: any = {},
  preview: any,
  diagnostics: any = {},
  logs: any[] = [],
  lastQRAt = 0,
  loggedIn = false,
  polling = false;
function message(text: string) {
  $('notice').textContent = text;
}
async function api(path: string, body?: any) {
  const response = await fetch('/api/' + path, {
    method: body === undefined ? 'GET' : 'POST',
    headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrf },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (!response.ok)
    throw new Error(
      response.status === 401
        ? '请使用启动器打开管理会话。'
        : '操作未完成，请检查配置与诊断结果。',
    );
  return response.json();
}
function view(name: string) {
  for (const panel of document.querySelectorAll<HTMLElement>('[data-panel]'))
    panel.hidden = panel.dataset.panel !== name;
  for (const button of document.querySelectorAll('[data-view]'))
    button.classList.toggle(
      'selected',
      (button as HTMLElement).dataset.view === name,
    );
  $('title').textContent = (
    {
      overview: '连接总览',
      setup: '配置向导',
      security: '安全与会话',
      doctor: '诊断与日志',
    } as any
  )[name];
  window.scrollTo(0, 0);
}
function step(number: number) {
  for (let i = 1; i <= 3; i++) $('step-' + i).hidden = i !== number;
}
function checks(id: string, items: any[]) {
  const box = $(id);
  box.replaceChildren();
  for (const item of items) {
    const row = document.createElement('div');
    row.className = 'diagnostic ' + (item.ok ? '' : 'warn');
    const icon = document.createElement('span');
    icon.textContent = item.ok ? '✓' : '●';
    const text = document.createElement('div');
    text.textContent = item.label;
    const detail = document.createElement('small');
    detail.textContent = item.detail;
    text.append(detail);
    row.append(icon, text);
    box.append(row);
  }
}
function renderStatus(status: any) {
  $('tunnel-state').textContent =
    status.tunnelMode === 'quick'
      ? '一键临时通道 · ' +
        (
          {
            starting: '正在连接',
            running: '正在运行',
            error: '连接失败',
            disabled: '等待启动',
          } as any
        )[status.quickTunnelState]
      : '固定域名模式';
  $('tunnel-detail').textContent =
    (status.tunnelMode === 'quick'
      ? status.cloudflaredFound
        ? '已检测到 cloudflared · 重新启动后临时地址会更新'
        : 'CLOUDFLARED_NOT_FOUND：请安装 cloudflared'
      : '使用自有域名与原生 TLS 或 Cloudflare Named Tunnel') +
    (status.quickTunnelError ? ' · ' + status.quickTunnelError : '');
  $('restart-tunnel').hidden = status.tunnelMode !== 'quick';
  platformChip.textContent =
    (status.platform === 'windows' ? 'Windows' : 'macOS') + ' · 本机';
  $('gateway-state').textContent = '正在运行 · 独立数据与管理端口';
  $('gateway-lamp').className = 'good';
  $('host-state').textContent = status.shimConnected
    ? '已连接 · ' + status.appVersion
    : '等待连接 · 请启动 ChatGPT';
  $('host-lamp').className = status.shimConnected ? 'good' : '';
  $('controller-state').textContent = status.controllerActive
    ? (status.controllerDevice ?? '浏览器') +
      ' · 第 ' +
      status.activeControllerGeneration +
      ' 代 · ' +
      (status.controllerConnectedSec ?? 0) +
      ' 秒'
    : '空闲 · 等待设备连接';
  $('controller-lamp').className = status.controllerActive ? 'good' : '';
  $('build-id').textContent = status.assetBuildId
    ? 'assetBuildId · ' + status.assetBuildId.slice(0, 20)
    : '资源将在连接宿主后绑定';
  $('session-count').textContent = '活跃登录会话：' + status.sessions;
}
async function renderLogs() {
  logs = await api('logs');
  const log = $('logs'),
    atBottom = log.scrollTop + log.clientHeight >= log.scrollHeight - 20;
  log.textContent = logs.map((item) => JSON.stringify(item)).join('\n');
  if (atBottom) log.scrollTop = log.scrollHeight;
}
async function issueAccessURL() {
  if (current.authMode === 'password' && current.configured) {
    const result = await api('auth/ticket', { ttlSeconds: 300 });
    return result.url as string;
  }
  return current.publicOrigin as string;
}
async function refreshQR(target?: string) {
  if (!target && !current.publicOrigin) {
    $('qr').hidden = true;
    return;
  }
  $('qr').hidden = false;
  const url = target ?? (await issueAccessURL());
  await QRCode.toCanvas($('qr'), url, {
    width: 220,
    margin: 1,
    color: { dark: '#204d3f', light: '#ffffff' },
  });
  lastQRAt = Date.now();
}
async function refresh() {
  current = await api('config');
  renderStatus(await api('status'));
  entry();
  $('origin').value =
    current.tunnelMode === 'custom' ? current.publicOrigin : '';
  $('tunnel-mode').value = current.tunnelMode;
  $('quick-protocol').value = current.quickTunnelProtocol || 'http2';
  $('tls-port').value = current.tlsPort || 0;
  $('tls-autocert').checked = current.tlsAutocert;
  $('tunnel-mode').onchange();
  $('copy-permanent-link').disabled =
    current.authMode !== 'password' || !current.configured;
  $('listener-origin').value = current.publicOrigin;
  $('listener-origin').readOnly = current.tunnelMode === 'quick';
  $('data-port').value = current.dataPort;
  $('admin-port').value = current.adminPort;
  $('auth-mode').value = current.authMode;
  $('team-domain').value = current.teamDomain;
  $('audience').value = current.audience;
  $('auth-mode').onchange();
  await refreshQR();
  diagnostics = await api('doctor');
  checks('doctor-checks', diagnostics.checks);
  checks('setup-checks', diagnostics.checks);
  await renderLogs();
}
function entry() {
  $('public-link').textContent = current.publicOrigin || '等待分配临时公网地址';
  if (current.publicOrigin) $('public-link').href = current.publicOrigin;
  else $('public-link').removeAttribute('href');
  for (const id of ['copy-link', 'open-link', 'copy-permanent-link'])
    $(id).disabled = !current.publicOrigin;
}
async function authenticated() {
  const session = await api('session');
  csrf = session.csrf;
  loggedIn = true;
  $('gate').hidden = true;
  if (window.remoteCodexDesktop)
    $('autostart').checked = await window.remoteCodexDesktop.getAutoStart();
  await refresh();
  view(current.configured ? 'overview' : 'setup');
  step(1);
}
async function exchange(code: string) {
  const pre = await api('bootstrap/csrf');
  csrf = pre.csrf;
  await api('bootstrap', { code });
  $('bootstrap-code').value = '';
  await authenticated();
}
function handle(id: string, fn: () => any) {
  const element = $(id);
  const run = async (event: any) => {
    event.preventDefault();
    const target = event.submitter ?? element;
    target.disabled = true;
    try {
      await fn();
    } catch (error) {
      message((error as Error).message);
    } finally {
      target.disabled = false;
    }
  };
  if (element instanceof HTMLFormElement) element.onsubmit = run;
  else element.onclick = run;
}
for (const button of document.querySelectorAll<HTMLElement>('[data-view]'))
  button.onclick = () => view(button.dataset.view!);
handle('bootstrap-form', () => exchange($('bootstrap-code').value));
handle('next-1', () => step(2));
handle('generate-password', () => {
  const bytes = crypto.getRandomValues(new Uint8Array(32));
  $('owner-password').value = Array.from(bytes, (b) =>
    b.toString(16).padStart(2, '0'),
  ).join('');
  message('随机密码已生成。');
});
handle('copy-password', () =>
  navigator.clipboard.writeText($('owner-password').value),
);
$('auth-mode').onchange = () => {
  $('password-fields').hidden = $('auth-mode').value !== 'password';
  $('access-fields').hidden = $('auth-mode').value !== 'cloudflare-access';
};
$('tunnel-mode').onchange = () => {
  const quick = $('tunnel-mode').value === 'quick';
  $('quick-fields').hidden = !quick;
  $('custom-fields').hidden = quick;
  $('origin').required = !quick;
  $('auth-mode').options[1].disabled = quick;
  if (quick) $('auth-mode').value = 'password';
  $('auth-mode').onchange();
  for (const id of [
    'preview-tunnel',
    'copy-tunnel',
    'apply-tunnel',
    'tunnel-diff',
    'dns-command',
  ])
    $(id).hidden = quick;
};
handle('setup-form', async () => {
  if (window.remoteCodexDesktop && $('tunnel-mode').value === 'quick') {
    await window.remoteCodexDesktop.ensureCloudflared();
  }
  await api('setup', {
    tunnelMode: $('tunnel-mode').value,
    quickTunnelProtocol: $('quick-protocol').value,
    publicOrigin: $('origin').value,
    mode: $('auth-mode').value,
    password: $('owner-password').value,
    teamDomain: $('team-domain').value,
    audience: $('audience').value,
    tlsPort: Number($('tls-port').value),
    tlsAutocert: $('tls-autocert').checked,
  });
  $('owner-password').value = '';
  await refresh();
  step(3);
  message('配置已保存。');
});
handle('change-mode', () => {
  view('setup');
  step(2);
});
handle('restart-tunnel', async () => {
  await api('tunnel/restart', {});
  await refresh();
  message('临时通道已重新启动，请使用新二维码。');
});
handle('preview-tunnel', async () => {
  preview = await api('tunnel/preview', {
    hostname: new URL(current.publicOrigin).hostname,
  });
  $('tunnel-diff').textContent = preview.diff;
  $('dns-command').textContent = preview.dnsCommand;
});
handle('copy-tunnel', () => navigator.clipboard.writeText(preview?.diff ?? ''));
handle('apply-tunnel', async () => {
  if (!preview) throw new Error('请先生成 Diff 预览。');
  await api('tunnel/apply', {
    hostname: new URL(current.publicOrigin).hostname,
    digest: preview.digest,
    confirm: true,
  });
  message('隧道配置已写入，原配置已备份。');
});
$('autostart').onchange = async () => {
  try {
    if (window.remoteCodexDesktop)
      await window.remoteCodexDesktop.setAutoStart($('autostart').checked);
    else await api('autostart', { enabled: $('autostart').checked });
    message('自动启动设置已保存。');
  } catch (error) {
    message((error as Error).message);
  }
};
handle('finish-setup', async () => {
  view('overview');
  await refresh();
});
for (const action of ['start', 'stop', 'reconnect'])
  handle(action, async () => {
    await api('host/' + action, {});
    await refresh();
    message('宿主连接状态已更新。');
  });
handle('copy-link', async () => {
  const url = await issueAccessURL();
  await navigator.clipboard.writeText(url);
  await refreshQR(url);
  message(
    current.authMode === 'password' && current.configured
      ? '已复制一次性免密登录链接（5 分钟有效，用完即废，不会写入代理日志）。'
      : '已复制访问链接。',
  );
});
handle('open-link', async () => {
  const url = await issueAccessURL();
  window.open(url, '_blank', 'noopener');
});
handle('copy-permanent-link', async () => {
  const result = await api('auth/ticket', { permanent: true });
  await navigator.clipboard.writeText(result.url);
  await refreshQR(result.url);
  message('永久直登链接已复制。可收藏书签，点吊销全部会话或改密码会立即作废。');
});
handle('password-form', async () => {
  await api('password', { password: $('new-password').value });
  $('new-password').value = '';
  await refresh();
  message('新密码已保存，全部设备会话和全部永久直登链接已作废。');
});
handle('revoke', async () => {
  await api('auth/revoke-all', {});
  await refresh();
  message('全部设备会话和全部永久直登链接已作废。');
});
handle('disconnect', async () => {
  await api('controller/disconnect', {});
  await refresh();
  message('控制端已断开。');
});
handle('listeners-form', async () => {
  const result = await api('listeners', {
    publicOrigin: $('listener-origin').value,
    dataPort: Number($('data-port').value),
    adminPort: Number($('admin-port').value),
  });
  if (result.adminOrigin !== location.origin) {
    location.replace(result.adminOrigin);
    return;
  }
  await refresh();
  message('访问设置已保存，请重新连接宿主并预览隧道规则。');
});
handle('change-auth', () => {
  view('setup');
  step(2);
});
handle('run-doctor', refresh);
handle('copy-diagnostics', () =>
  navigator.clipboard.writeText(JSON.stringify({ diagnostics, logs }, null, 2)),
);
const fragment = new URLSearchParams(location.hash.slice(1));
const bootstrap = fragment.get('bootstrap');
history.replaceState(null, '', location.pathname);
(bootstrap ? exchange(bootstrap) : authenticated()).catch(() =>
  message('请使用启动器打开控制面板，或输入一次性引导码。'),
);
async function syncStatusAndLogs(refreshExpiredQR = false) {
  if (document.hidden) return;
  if (!loggedIn || polling) return;
  polling = true;
  try {
    const status = await api('status');
    renderStatus(status);
    if (status.publicOrigin !== current.publicOrigin) {
      current = await api('config');
      entry();
      await refreshQR();
    }
    await renderLogs();
    if (refreshExpiredQR && Date.now() - lastQRAt >= 240000) await refreshQR();
  } catch (error) {
    message((error as Error).message);
  } finally {
    polling = false;
  }
}
setInterval(syncStatusAndLogs, 5000);
setInterval(async () => {
  if (document.hidden) return;
  if (loggedIn && current.publicOrigin)
    try {
      await refreshQR();
    } catch (error) {
      message((error as Error).message);
    }
}, 240000);
document.addEventListener('visibilitychange', () => {
  if (!document.hidden && loggedIn) void syncStatusAndLogs(true);
});
