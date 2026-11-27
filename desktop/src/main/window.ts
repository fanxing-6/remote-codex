import { BrowserWindow, shell } from 'electron';
import { fileURLToPath } from 'node:url';
import type { SidecarManager } from './sidecar.js';
import { isAdminURL, isExternalURL } from './navigation.js';

export class WindowManager {
  private window?: BrowserWindow;
  private quitting = false;

  constructor(
    private sidecar: SidecarManager,
    private report: (error: unknown) => void,
  ) {}

  allowQuit(): void {
    this.quitting = true;
  }

  private external(url: string): void {
    if (isExternalURL(url)) {
      void shell.openExternal(url).catch(this.report);
    }
  }

  async open(): Promise<BrowserWindow> {
    if (this.window && !this.window.isDestroyed()) {
      this.window.show();
      this.window.focus();
      return this.window;
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
    window.webContents.session.setPermissionRequestHandler(
      (contents, permission, callback) => {
        callback(
          contents === window.webContents &&
            local(contents.getURL()) &&
            permission === 'clipboard-sanitized-write',
        );
      },
    );
    window.webContents.session.webRequest.onBeforeRequest(
      (details, callback) => {
        callback({ cancel: !local(details.url) });
      },
    );
    window.on('close', (event) => {
      if (!this.quitting) {
        event.preventDefault();
        window.hide();
      }
    });
    window.on('closed', () => {
      this.window = undefined;
    });
    window.once('ready-to-show', () => window.show());
    try {
      await window.loadURL(await this.sidecar.bootstrapURL());
      return window;
    } catch (error) {
      window.destroy();
      throw error;
    }
  }
}
