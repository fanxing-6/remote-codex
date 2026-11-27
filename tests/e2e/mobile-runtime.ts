import {installMobileRuntime} from '../../web/browser/mobile-runtime';

(window as any).__originalFocus = HTMLElement.prototype.focus;
window.addEventListener('DOMContentLoaded', installMobileRuntime, {once:true});
