export const channels=['connect-app-host','codex_desktop:connect-app-server-manager','codex_desktop:git-worker-port'];
export const syncMethods=['getInitialSidebarBootstrap','getSentryInitOptions','getDesktopUserAgent','getAppSessionId','getBuildFlavor','isDeviceCheckSupported','isIntelMacBuild','getSystemThemeVariant'];
export const callMethods=['acknowledgeChunkedMessage','sendMessageFromView','connectGitWorker','sendWorkerMessageFromView','getFastModeRolloutMetrics','finishStartupScreen'];
export const maxFrame=16*1024*1024, maxQueue=32*1024*1024,maxPending=256,maxPorts=64;
export function assertSerializable(value:any,seen=new WeakSet<object>()) {
 if(value===null||value===undefined||['string','boolean','number','bigint'].includes(typeof value))return;
 if(typeof value!=='object')throw new Error('UNSUPPORTED_VALUE');
 if(seen.has(value))return;seen.add(value);
 if(value instanceof ArrayBuffer||ArrayBuffer.isView(value)||value instanceof Date||value instanceof RegExp)return;
 if(value instanceof Map){for(const [key,item] of value){assertSerializable(key,seen);assertSerializable(item,seen)};return;}
 if(value instanceof Set){for(const item of value)assertSerializable(item,seen);return;}
 if(Array.isArray(value)||Object.getPrototypeOf(value)===Object.prototype){for(const item of Object.values(value))assertSerializable(item,seen);return;}
 throw new Error('UNSUPPORTED_TRANSFERABLE');
}
