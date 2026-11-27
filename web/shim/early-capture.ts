import {Encoder} from 'cbor-x';
import {assertSerializable,maxFrame,maxQueue,maxPorts} from '../shared';
const root=window as any,codec=new Encoder({structuredClone:true,useRecords:false});
let bytes=0,ports=0;
root.__cwbEarlyMessages=[];
root.__cwbCapture=(event:MessageEvent)=>{
 if(event.source!==null&&event.source!==window)return;
 try{
  assertSerializable(event.data);const size=codec.encode(event.data).byteLength;
  if(size>maxFrame||bytes+size>maxQueue||ports+event.ports.length>maxPorts||root.__cwbEarlyMessages.length>=1024)throw new Error('INITIAL_QUEUE_LIMIT');
  bytes+=size;ports+=event.ports.length;root.__cwbEarlyMessages.push(event);
 }catch{root.__cwbCaptureError='INITIAL_QUEUE_INVALID';window.removeEventListener('message',root.__cwbCapture)}
};
window.addEventListener('message',root.__cwbCapture);
