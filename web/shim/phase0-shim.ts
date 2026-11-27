import http from 'node:http';
import { timingSafeEqual } from 'node:crypto';
import { WebSocketServer } from 'ws';
import {syncMethods,callMethods} from '../shared';

const electron = process.mainModule.require('electron');
const key = '__cwbPhase0';
const instrumentation = `(() => {
 const Native = window.MessageChannel, pairs = new WeakMap();
 const channels = {};
 window.__cwbProbe = channels;
 window.addEventListener('message',event=>{
  if(event.source!==window||event.data?.type!=='codex_desktop:git-worker-port'||!event.ports[0])return;
  const port=event.ports[0],state=channels[event.data.type]={sent:0,received:0,portTransferred:true};
  const send=port.postMessage.bind(port);port.postMessage=(...args)=>{state.sent++;return send(...args)};
  port.addEventListener('message',()=>state.received++);port.start();
 });
 window.MessageChannel = class extends Native {
  constructor() { super(); pairs.set(this.port2,this.port1); }
 };
 const post = window.postMessage.bind(window);
 window.postMessage = function(data, origin, transfer) {
  if (['connect-app-host','codex_desktop:connect-app-server-manager'].includes(data?.type)) {
   const port = pairs.get(transfer?.[0]);
   if (!port) throw new Error('CHANNEL_PAIR_MISSING');
   const state = channels[data.type] = {sent:0,received:0};
   const send = port.postMessage.bind(port);
   port.postMessage = (...args) => { state.sent++; return send(...args); };
   port.addEventListener('message',()=>state.received++);
   port.start();
  }
  return post(data,origin,transfer);
 };
})();`;

export function install(options: {token:string,instance:string,startedAt:number}) {
 globalThis[key]?.stop();
 if (!/^[a-f0-9]{64}$/.test(options.token)) throw new Error('TOKEN_INVALID');
 let window:any, stopped=false, failure='', result:any, menuSummary:any;
 const before = electron.BrowserWindow.getAllWindows().map(w=>w.id);
 const server=http.createServer((_,res)=>res.writeHead(404).end());
 server.maxConnections=8;server.headersTimeout=5000;server.requestTimeout=5000;
 const sockets=new WebSocketServer({noServer:true,maxPayload:16*1024*1024});
 const stop=()=>{
  if(stopped)return;
  stopped=true;
  clearTimeout(expiry);
  if(window&&!window.isDestroyed()) {
   if(window.webContents.debugger.isAttached())window.webContents.debugger.detach();
   window.destroy();
  }
  for(const client of sockets.clients)client.close(1000);
  sockets.close();server.close();delete globalThis[key];
 };
 const expiry=setTimeout(stop,120000);
 server.on('upgrade',(request,socket,head)=>{
  const supplied=Buffer.from(request.headers.authorization??'');
  const expected=Buffer.from('Bearer '+options.token);
  if(request.url!='/internal'||request.headers.origin!==undefined||supplied.length!==expected.length||!timingSafeEqual(supplied,expected)){
   socket.end('HTTP/1.1 403 Forbidden\r\n\r\n');return;
  }
  sockets.handleUpgrade(request,socket,head,connection=>{
   connection.on('message',async(data,binary)=>{
    if(binary){connection.send(data,{binary:true});return;}
    const command=JSON.parse(data.toString());
    if(command.kind==='stop'){
     if(window&&!window.isDestroyed()){if(window.webContents.debugger.isAttached())window.webContents.debugger.detach();window.destroy()}
     const remaining=electron.BrowserWindow.getAllWindows().map(w=>w.id);
     connection.send(JSON.stringify({kind:'stopped',windowReleased:!window||window.isDestroyed(),originalWindowsPreserved:before.every(id=>remaining.includes(id)),remainingWindows:remaining}));stop();return;
    }
    if(command.kind!=='probe'){connection.close(1008);return;}
    if(!failure&&window&&!window.isDestroyed()&&!window.webContents.isLoading()) {
     result=await window.webContents.executeJavaScript(`({channels:window.__cwbProbe,bridgeKeys:Object.keys(window.electronBridge||{}),windowType:window.electronBridge?.windowType,bridgeContractValid:${JSON.stringify([...syncMethods,...callMethods,'getPreloadStartedAtMs','subscribeToSystemThemeVariant','subscribeToWorkerMessages','getPathForFile','showContextMenu'])}.every(name=>typeof window.electronBridge[name]==='function')})`);
    }
    connection.send(JSON.stringify({kind:'status',pid:process.pid,startedAt:options.startedAt,instance:options.instance,windowId:window?.id,before,after:electron.BrowserWindow.getAllWindows().map(w=>w.id),hidden:window?!window.isVisible():false,failure,result,menuSummary}));
   });
  });
 });
 globalThis[key]={stop};
 server.listen(0,'127.0.0.1',()=>{
  Promise.resolve().then(async()=>{
   const flatten=(items:any[]):any[]=>items.flatMap(item=>[item,...flatten(item.submenu?.items??[])]);
   menuSummary=flatten(electron.Menu.getApplicationMenu().items).filter(item=>/窗口|Window|window/.test(item.label)).map(item=>({label:item.label,id:item.id,accelerator:item.accelerator,enabled:item.enabled,visible:item.visible}));
   const candidates=flatten(electron.Menu.getApplicationMenu().items).filter(item=>['新窗口','新建窗口','New Window'].includes(item.label)&&item.enabled);
   if(candidates.length!==1)throw new Error('WINDOW_ACTION_NOT_UNIQUE');
   const original=electron.BrowserWindow.getFocusedWindow();
   const created:any[]=[];
   const capture=(_,item)=>created.push(item);
   electron.app.on('browser-window-created',capture);
   try {candidates[0].click(candidates[0],original,{});}
   finally {electron.app.removeListener('browser-window-created',capture);}
   if(created.length!==1)throw new Error('WINDOW_CREATION_NOT_SYNCHRONOUS_UNIQUE');
   window=created[0];window.hide();
   if(original&&!original.isDestroyed())original.focus();
   const wc=window.webContents;
   wc.setBackgroundThrottling(false);
   const deadline=Date.now()+45000;
   while(wc.isLoading()||!wc.getURL().startsWith('app://-/index.html')){
    if(Date.now()>deadline)throw new Error('OFFICIAL_WINDOW_LOAD_TIMEOUT');
    await new Promise(resolve=>setTimeout(resolve,100));
   }
   wc.debugger.attach('1.3');
   await wc.debugger.sendCommand('Page.enable');
   await wc.debugger.sendCommand('Page.addScriptToEvaluateOnNewDocument',{source:instrumentation});
   await wc.loadURL(wc.getURL());
   window.hide();
   if(await wc.executeJavaScript('window.electronBridge.connectGitWorker(crypto.randomUUID())')!==true)throw new Error('GIT_WORKER_REGISTRATION_FAILED');
  }).catch(error=>{failure=/^[A-Z_]+$/.test(error.message)?error.message:'OFFICIAL_WINDOW_INITIALIZATION_FAILED';});
 });
 return true;
}
