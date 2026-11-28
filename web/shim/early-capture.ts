const root = window as any;
// reset 之前没有控制端，preload 广播随接收随释放。
root.__cwbCapture = (event: MessageEvent) => {
  if (event.source !== null && event.source !== window) return;
  for (const port of event.ports) port.close();
};
window.addEventListener('message', root.__cwbCapture);
const observer = new MutationObserver((records) => {
  if (!root.__cwbCapture) {
    observer.disconnect();
    return;
  }
  for (const record of records) {
    for (const node of record.addedNodes) {
      if (!(node instanceof Element)) continue;
      const elements = node.matches('script,link')
        ? [node, ...node.querySelectorAll('script,link')]
        : node.querySelectorAll('script,link');
      for (const element of elements) {
        element.setAttribute('type', 'blocked/cwb');
        element.remove();
      }
    }
  }
});
observer.observe(document, { childList: true, subtree: true });
root.__cwbCapture.stopObserving = () => observer.disconnect();
document.addEventListener('DOMContentLoaded', () => observer.disconnect(), {
  once: true,
});
