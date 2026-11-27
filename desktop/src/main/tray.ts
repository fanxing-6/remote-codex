import { app, clipboard, Menu, nativeImage, Tray } from 'electron';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import type { CloudflaredManager } from './cloudflared.js';
import type { SidecarManager } from './sidecar.js';
import type { WindowManager } from './window.js';

export class TrayManager {
  private tray: Tray;

  constructor(
    private sidecar: SidecarManager,
    private windows: WindowManager,
    private cloudflared: CloudflaredManager,
    private report: (error: unknown) => void,
  ) {
    const iconPath = app.isPackaged
      ? path.join(process.resourcesPath, 'tray.png')
      : fileURLToPath(new URL('../../build/tray.png', import.meta.url));
    const image = nativeImage
      .createFromPath(iconPath)
      .resize({ width: 20, height: 20 });
    if (image.isEmpty()) throw new Error('TRAY_ICON_MISSING');
    this.tray = new Tray(image);
    this.tray.setToolTip('Remote Codex');
    this.tray.on('double-click', () => {
      void this.windows.open().catch(this.report);
    });
    this.refresh();
  }

  refresh(): void {
    const action = (work: () => Promise<unknown>) => () => {
      void work().catch(this.report);
    };
    this.tray.setContextMenu(
      Menu.buildFromTemplate([
        { label: '打开控制面板', click: action(() => this.windows.open()) },
        { type: 'separator' },
        {
          label: '复制一次性登录链接',
          click: action(async () =>
            clipboard.writeText(await this.sidecar.loginURL(false)),
          ),
        },
        {
          label: '复制永久直登链接',
          click: action(async () =>
            clipboard.writeText(await this.sidecar.loginURL(true)),
          ),
        },
        {
          label: '重新连接宿主',
          click: action(() => this.sidecar.request('host/reconnect', {})),
        },
        {
          label: '下载或检查 Cloudflared',
          click: action(async () => {
            await this.cloudflared.ensureBinary();
            await this.sidecar.request('tunnel/restart', {});
          }),
        },
        { type: 'separator' },
        {
          label: '登录系统后自动启动',
          type: 'checkbox',
          checked: app.getLoginItemSettings().openAtLogin,
          click: (item) => {
            app.setLoginItemSettings({ openAtLogin: item.checked });
            this.refresh();
          },
        },
        { label: '退出 Remote Codex', click: () => app.quit() },
      ]),
    );
  }

  destroy(): void {
    this.tray.destroy();
  }
}
