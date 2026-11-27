import { app, dialog, ipcMain, Menu } from 'electron';
import { mkdir, readFile } from 'node:fs/promises';
import path from 'node:path';
import { CloudflaredManager } from './cloudflared.js';
import { SidecarManager } from './sidecar.js';
import { TrayManager } from './tray.js';
import { WindowManager } from './window.js';

// 自动化验收通过独立 userData 使用与用户配置相同的启动路径。
if (process.env.REMOTE_CODEX_USER_DATA)
  app.setPath('userData', path.resolve(process.env.REMOTE_CODEX_USER_DATA));

let sidecar: SidecarManager | undefined;
let windows: WindowManager | undefined;
let tray: TrayManager | undefined;
let exiting = false;
let stopped = false;

function report(error: unknown): void {
  dialog.showErrorBox(
    'Remote Codex',
    error instanceof Error ? error.message : String(error),
  );
}

async function start(): Promise<void> {
  const userData = app.getPath('userData');
  await mkdir(userData, { recursive: true, mode: 0o700 });
  const cloudflared = new CloudflaredManager(userData);
  const config = await readFile(
    path.join(userData, 'config.json'),
    'utf8',
  ).then(
    (value) =>
      JSON.parse(value) as { tunnelMode?: string; publicOrigin?: string },
    (error: NodeJS.ErrnoException) => {
      if (error.code !== 'ENOENT') throw error;
      return undefined;
    },
  );
  let cloudflaredPath = await cloudflared.findBinary();
  let downloadError: unknown;
  if (
    !cloudflaredPath &&
    (config?.tunnelMode ?? (config?.publicOrigin ? 'custom' : 'quick')) ===
      'quick'
  ) {
    try {
      cloudflaredPath = await cloudflared.ensureBinary();
    } catch (error) {
      downloadError = error;
    }
  }
  sidecar = new SidecarManager({
    userData,
    cloudflaredPath: cloudflaredPath ?? cloudflared.binaryPath,
    manualHost: process.env.REMOTE_CODEX_MANUAL_HOST === '1',
  });
  sidecar.onFailure((error) => {
    if (!exiting) {
      report(error);
      app.quit();
    }
  });
  await sidecar.start();
  windows = new WindowManager(sidecar, report);
  const authorize = (event: Electron.IpcMainInvokeEvent) => {
    if (
      event.senderFrame?.url &&
      new URL(event.senderFrame.url).origin === sidecar!.origin &&
      event.senderFrame === event.sender.mainFrame
    )
      return;
    throw new Error('IPC_ORIGIN_REJECTED');
  };
  ipcMain.handle('desktop:autostart:get', (event) => {
    authorize(event);
    return app.getLoginItemSettings().openAtLogin;
  });
  ipcMain.handle('desktop:cloudflared:ensure', async (event) => {
    authorize(event);
    await cloudflared.ensureBinary();
  });
  ipcMain.handle('desktop:autostart:set', (event, enabled: unknown) => {
    authorize(event);
    if (typeof enabled !== 'boolean')
      throw new Error('AUTOSTART_VALUE_INVALID');
    app.setLoginItemSettings({ openAtLogin: enabled });
    tray?.refresh();
  });
  tray = new TrayManager(sidecar, windows, cloudflared, report);
  Menu.setApplicationMenu(
    Menu.buildFromTemplate([
      {
        label: 'Remote Codex',
        submenu: [
          {
            label: '打开控制面板',
            click: () => {
              void windows!.open().catch(report);
            },
          },
          { role: 'quit' },
        ],
      },
      { role: 'editMenu' },
      { role: 'viewMenu' },
    ]),
  );
  await windows.open();
  if (downloadError) report(downloadError);
}

if (!app.requestSingleInstanceLock()) app.exit(0);
else {
  app.on('second-instance', () => {
    if (windows) void windows.open().catch(report);
  });
  app.on('activate', () => {
    if (windows) void windows.open().catch(report);
  });
  app.on('window-all-closed', () => {});
  app.on('before-quit', (event) => {
    if (stopped) return;
    event.preventDefault();
    if (exiting) return;
    exiting = true;
    windows?.allowQuit();
    void (async () => {
      try {
        await sidecar?.stop();
      } catch (error) {
        report(error);
      } finally {
        tray?.destroy();
        stopped = true;
        app.quit();
      }
    })();
  });
  for (const signal of ['SIGINT', 'SIGTERM'] as const)
    process.on(signal, () => app.quit());
  void app
    .whenReady()
    .then(start)
    .catch((error) => {
      report(error);
      app.quit();
    });
}
