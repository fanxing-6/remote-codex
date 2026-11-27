const target = Process.getModuleByName('chrome.dll');
const symbols = target.enumerateExports();
const address = fragment => {
 const matches=symbols.filter(item=>item.name.includes(fragment));
 if(matches.length!==1)throw new Error('EXPORT_NOT_UNIQUE');
 return matches[0].address;
};
const options={abi:'win64',exceptions:'propagate'};
const bind=(fragment,result,args)=>new NativeFunction(address(fragment),result,args,options);
const current=bind('?GetCurrent@Isolate','pointer',[]);
const getContext=bind('?GetCurrentContext@Isolate','pointer',['pointer','pointer']);
const scopeNew=bind('??0HandleScope@v8','pointer',['pointer','pointer']);
const scopeEnd=bind('??1HandleScope@v8','void',['pointer']);
const environment=bind('?GetCurrentEnvironment@node','pointer',['pointer']);
const eventLoop=bind('?GetCurrentEventLoop@node','pointer',['pointer']);
const mainContext=bind('?GetMainContext@node','pointer',['pointer','pointer']);
const enter=bind('?Enter@Context@v8','void',['pointer']);
const exit=bind('?Exit@Context@v8','void',['pointer']);
const catcherNew=bind('??0TryCatch@v8','pointer',['pointer','pointer']);
const catcherEnd=bind('??1TryCatch@v8','void',['pointer']);
const hasCaught=bind('?HasCaught@TryCatch@v8','bool',['pointer']);
const newString=bind('?NewFromUtf8@String@v8','pointer',['pointer','pointer','pointer','int','int']);
const compile=bind('?Compile@Script@v8','pointer',['pointer','pointer','pointer','pointer']);
const run=bind('?Run@Script@v8@@QEAA?AV?$MaybeLocal@VValue@v8@@@2@V?$Local@VContext@v8@@@2@@Z','pointer',['pointer','pointer','pointer','pointer']);
const size=bind('uv_handle_size','size_t',['int']);
const initialize=bind('uv_async_init','int',['pointer','pointer','pointer']);
const wake=bind('uv_async_send','int',['pointer']);
const close=bind('uv_close','void',['pointer','pointer']);
const mainThreads=Process.enumerateThreads().filter(item=>item.name==='CrBrowserMain');
if(mainThreads.length!==1)throw new Error('MAIN_THREAD_NOT_UNIQUE');
const mainThread=mainThreads[0].id;
const source=Memory.allocUtf8String(BOOTSTRAP_SOURCE);
const handle=Memory.alloc(Number(size(1)));
let installed=false,isolate,env,hook,failure='',executed=false,cancelled=false;
const closed=new NativeCallback(()=>{
 setTimeout(()=>send({kind:'complete',executed,failure,threadId:mainThread,asyncClosed:true}),0);
},'void',['pointer'],'win64');
const callback=new NativeCallback(()=>{
 try{
  if(cancelled)throw new Error('BOOTSTRAP_CANCELLED');
  if(Process.getCurrentThreadId()!==mainThread||!current().equals(isolate))throw new Error('MAIN_THREAD_CHANGED');
  const scope=Memory.alloc(24),contextOut=Memory.alloc(8),catcher=Memory.alloc(48);
  scopeNew(scope,isolate);
  try{
   mainContext(contextOut,env);
   const context=contextOut.readPointer();
   if(context.isNull())throw new Error('MAIN_CONTEXT_EMPTY');
   enter(context);
   try{
    catcherNew(catcher,isolate);
    try{
     const stringOut=Memory.alloc(8),scriptOut=Memory.alloc(8),resultOut=Memory.alloc(8);
     newString(stringOut,isolate,source,0,-1);
     if(stringOut.readPointer().isNull())throw new Error('SOURCE_STRING_EMPTY');
     compile(scriptOut,context,stringOut.readPointer(),NULL);
     if(scriptOut.readPointer().isNull()||hasCaught(catcher))throw new Error('COMPILE_FAILED');
     run(scriptOut.readPointer(),resultOut,context,NULL);
     if(resultOut.readPointer().isNull()||hasCaught(catcher))throw new Error('SCRIPT_FAILED');
     executed=true;
    }finally{catcherEnd(catcher)}
   }finally{exit(context)}
  }finally{scopeEnd(scope)}
 }catch(error){failure=/^[A-Z_]+$/.test(error.message)?error.message:'NATIVE_BOOTSTRAP_FAILED'}
 finally{close(handle,closed)}
},'void',['pointer'],'win64');
hook=Interceptor.attach(address('uv_run'),{
 onEnter(args){
  if(installed||cancelled||Process.getCurrentThreadId()!==mainThread)return;
  isolate=current();
  if(isolate.isNull())return;
  const scope=Memory.alloc(24),out=Memory.alloc(8);
  scopeNew(scope,isolate);
  try{
   getContext(isolate,out);
   if(out.readPointer().isNull())return;
   env=environment(out.readPointer());
   if(env.isNull()||!eventLoop(isolate).equals(args[0]))return;
   const code=initialize(args[0],handle,callback);
   hook.detach();
   if(code!==0){send({kind:'complete',failure:'ASYNC_INIT_FAILED',asyncClosed:true});return;}
   installed=true;
   if(wake(handle)!==0){failure='ASYNC_SEND_FAILED';close(handle,closed);}
  }finally{scopeEnd(scope)}
 }
});
rpc.exports.cancel=()=>{
 cancelled=true;
 if(!installed){hook.detach();send({kind:'complete',failure:'BOOTSTRAP_CANCELLED',asyncClosed:true});}
 else if(!executed)wake(handle);
};
