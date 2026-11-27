const root = window as any;
// reset 之前没有控制端，preload 广播随接收随释放。
root.__cwbCapture = (event: MessageEvent) => {
  if (event.source !== null && event.source !== window) return;
  for (const port of event.ports) port.close();
};
window.addEventListener('message', root.__cwbCapture);
