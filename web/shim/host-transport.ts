import {Encoder} from 'cbor-x';
import {channels,callMethods,maxFrame,maxQueue,maxPorts,maxPending,assertSerializable} from '../shared';
const root=globalThis as any;
const codec=new Encoder({structuredClone:true,useRecords:false});
const ports=new Map<string,MessagePort>(),subscriptions=new Map<string,()=>void>();
const closedPorts=new Set<string>();
let generation=0,sequence=0,pending=0,active=false,initialBytes=0;
const initial:any[]=[];
const send=(message:any)=>{
 assertSerializable(message);
 const bytes=codec.encode({...message,generation});
 if(bytes.byteLength>maxFrame)throw new Error('FRAME_LIMIT');
 if(!active){if(initial.length>=1024||initialBytes+bytes.byteLength>maxQueue)throw new Error('INITIAL_QUEUE_LIMIT');initialBytes+=bytes.byteLength;initial.push(message);return;}
 root.__cwbSend(bytes.toBase64());
};
function attach(id:string,port:MessagePort,channelType:string){
 if(ports.has(id)||closedPorts.has(id)||ports.size>=maxPorts||!channels.includes(channelType))throw new Error('PORT_LIMIT_OR_TYPE');
 ports.set(id,port);port.onmessage=event=>{try{send({kind:'port-data',portId:id,channelType,data:event.data})}catch{send({kind:'error',code:'UNSUPPORTED_PORT_DATA'});root.__cwbFatal('UNSUPPORTED_PORT_DATA')}};
 port.onmessageerror=()=>root.__cwbFatal('PORT_MESSAGE_ERROR');port.start();
 port.addEventListener('close',()=>{if(!ports.delete(id))return;closedPorts.add(id);if(closedPorts.size>1024){root.__cwbFatal('PORT_LIMIT_OR_TYPE');return}send({kind:'port-close',portId:id,channelType})});
}
function disconnect(){const previous=[...ports.values()];ports.clear();closedPorts.clear();for(const port of previous)port.close();for(const unsubscribe of subscriptions.values())unsubscribe();subscriptions.clear();}
const forward=(event:MessageEvent)=>{
 if(event.source!==null&&event.source!==window)return;
 if(event.data?.type==='connect-app-host'||event.data?.type==='codex_desktop:connect-app-server-manager')return;
 try {
  const channelType=event.data?.type;
  if(event.ports.length&&!channels.includes(channelType))throw new Error('CHANNEL_UNKNOWN');
  const ids=event.ports.map(port=>{const id='host-'+(++sequence);attach(id,port,channelType);return id});
  send({kind:'window-message',data:event.data,channelType,ports:ids});
 } catch {root.__cwbFatal('UNSUPPORTED_WINDOW_MESSAGE');}
};
window.addEventListener('message',forward);
root.__cwbHost={
 reset(next:number){disconnect();generation=next;},
 activate(){active=true;initialBytes=0;for(const message of initial.splice(0))send(message);},
 async receive(base64:string){
  const bytes=Uint8Array.fromBase64(base64);if(bytes.byteLength>maxFrame)throw new Error('FRAME_LIMIT');
  const message=codec.decode(bytes);if(message.generation!==generation)throw new Error('STALE_GENERATION');
  if(message.kind==='port-open'){
   if(!channels.slice(0,2).includes(message.channelType))throw new Error('CHANNEL_UNKNOWN');
   const channel=new MessageChannel();attach(message.portId,channel.port1,message.channelType);
   const data={...message.metadata,type:message.channelType,...message.channelType==='connect-app-host'?{port:channel.port2}:{}};
   window.postMessage(data,location.origin,[channel.port2]);return;
  }
  if(message.kind==='port-data'){const port=ports.get(message.portId);if(!port)throw new Error('PORT_UNKNOWN');port.postMessage(message.data);return;}
  if(message.kind==='port-close'){const port=ports.get(message.portId);if(!port){if(closedPorts.has(message.portId))return;throw new Error('PORT_UNKNOWN')};ports.delete(message.portId);closedPorts.add(message.portId);port.close();return;}
  if(message.kind==='call'){
   if(!callMethods.includes(message.method)||pending>=maxPending)throw new Error('METHOD_DENIED');
   pending++;
   const current=generation;
   let timer:any;
   Promise.race([Promise.resolve().then(()=>root.electronBridge[message.method](...message.args)),new Promise((_,reject)=>{timer=setTimeout(()=>reject(new Error('CALL_TIMEOUT')),30000)})])
    .then(value=>{if(current===generation)send({kind:'result',id:message.id,value})})
    .catch(()=>{if(current===generation)send({kind:'error',id:message.id,code:'HOST_CALL_FAILED'})})
    .finally(()=>{clearTimeout(timer);pending--});return;
  }
  if(message.kind==='subscribe-worker'){
   if(message.worker!=='git'||subscriptions.has(message.worker))throw new Error('WORKER_DENIED');
   subscriptions.set(message.worker,root.electronBridge.subscribeToWorkerMessages(message.worker,data=>send({kind:'worker-message',worker:message.worker,data})));return;
  }
  if(message.kind==='unsubscribe-worker'){subscriptions.get(message.worker)?.();subscriptions.delete(message.worker);return;}
  throw new Error('MESSAGE_KIND_UNKNOWN');
 },
 dispose(){disconnect();window.removeEventListener('message',forward);}
};
if(root.__cwbCaptureError)throw new Error('INITIAL_QUEUE_INVALID');
if(root.__cwbCapture){window.removeEventListener('message',root.__cwbCapture);for(const event of root.__cwbEarlyMessages??[])forward(event);delete root.__cwbCapture;delete root.__cwbEarlyMessages;}
root.electronBridge.subscribeToSystemThemeVariant(()=>send({kind:'theme',value:root.electronBridge.getSystemThemeVariant()}));
