import { contextBridge, ipcRenderer } from 'electron';

contextBridge.exposeInMainWorld('remoteCodexDesktop', {
  ensureCloudflared: (): Promise<void> =>
    ipcRenderer.invoke('desktop:cloudflared:ensure'),
  getAutoStart: (): Promise<boolean> =>
    ipcRenderer.invoke('desktop:autostart:get'),
  setAutoStart: (enabled: boolean): Promise<void> =>
    ipcRenderer.invoke('desktop:autostart:set', enabled),
});
