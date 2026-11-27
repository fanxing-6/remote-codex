import { app, BrowserWindow, session, shell } from 'electron';
import { fileURLToPath } from 'node:url';
import type { SidecarManager } from './sidecar.js';
import { isAdminURL, isExternalURL } from './navigation.js';

export class WindowManager {
  private window?: BrowserWindow;

  constructor(
    private sidecar: SidecarManager,
    private report: (error: unknown) => void,
  ) {
    session.defaultSession.setPermissionRequestHandler(
      (contents, permission, callback) => {
        callback(
          !contents.isDestroyed() &&
            contents === this.window?.webContents &&
            isAdminURL(contents.getURL(), this.sidecar.origin) &&
            permission === 'clipboard-sanitized-write',
        );
      },
    );
    session.defaultSession.webRequest.onBeforeRequest((details, callback) => {
      callback({ cancel: !isAdminURL(details.url, this.sidecar.origin) });
    });
  }

  private external(url: string): void {
    if (isExternalURL(url)) {
      void shell.openExternal(url).catch(this.report);
    }
  }

  async open(): Promise<BrowserWindow> {
    const existing = this.window;
    if (existing && !existing.isDestroyed()) {
      await app.dock?.show();
      if (existing.isDestroyed()) return this.open();
      existing.show();
      existing.focus();
      return existing;
    }
    const window = new BrowserWindow({
      width: 1200,
      height: 840,
      minWidth: 760,
      minHeight: 600,
      title: 'Remote Codex',
      show: false,
      backgroundColor: '#f5f7f9',
      webPreferences: {
        contextIsolation: true,
        nodeIntegration: false,
        sandbox: true,
        webSecurity: true,
        preload: fileURLToPath(new URL('./preload.cjs', import.meta.url)),
      },
    });
    this.window = window;
    const local = (url: string) => isAdminURL(url, this.sidecar.origin);
    window.webContents.on('will-navigate', (event, url) => {
      if (local(url)) return;
      event.preventDefault();
      void (async () => {
        await this.sidecar.refreshConfig();
        if (local(url)) await window.loadURL(await this.sidecar.bootstrapURL());
        else this.external(url);
      })().catch(this.report);
    });
    window.webContents.on('will-redirect', (event, url) => {
      if (!local(url)) event.preventDefault();
    });
    window.webContents.on('will-attach-webview', (event) =>
      event.preventDefault(),
    );
    window.webContents.setWindowOpenHandler(({ url }) => {
      if (local(url)) void window.loadURL(url).catch(this.report);
      else this.external(url);
      return { action: 'deny' };
    });
    window.on('closed', () => {
      if (this.window === window) this.window = undefined;
    });
    window.once('ready-to-show', () => window.show());
    try {
      await app.dock?.show();
      const url = await this.sidecar.bootstrapURL();
      if (!window.isDestroyed()) await window.loadURL(url);
      return window;
    } catch (error) {
      if (!window.isDestroyed()) window.destroy();
      throw error;
    }
  }
}
