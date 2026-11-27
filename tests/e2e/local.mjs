import {fileURLToPath} from 'node:url';
import path from 'node:path';
const {chromium}=await import(process.env.CWB_PLAYWRIGHT_PATH??new URL('../../web/node_modules/playwright/index.mjs',import.meta.url).href);
import {mkdir,readFile,writeFile,stat} from 'node:fs/promises';
import {spawn} from 'node:child_process';
import {randomBytes,randomUUID} from 'node:crypto';
import assert from 'node:assert/strict';
import net from 'node:net';
const root=fileURLToPath(new URL('../../',import.meta.url)).replaceAll('\\','/');
const windows=process.platform==='win32';
const binary=process.env.CWB_BINARY??root+(windows?'bin/remote-codex-windows-amd64.exe':'bin/remote-codex');
const recoveryOnly=process.env.CWB_RECOVERY_ONLY==='1';
const work=root+'.work/e2e';await mkdir(work,{recursive:true});
const config={tunnelMode:'custom',publicOrigin:'http://127.0.0.1:18765',allowedHosts:['127.0.0.1:18765'],dataPort:18765,adminPort:18766,allowInsecureLocalhost:true,trustedProxy:['127.0.0.1/32'],bootstrapPython:process.env.BOOTSTRAP_PYTHON??root+(windows?'bin/.venv/Scripts/python.exe':'bin/.venv/bin/python'),bootstrapHelper:'',auth:{mode:'password'}};
await writeFile(work+'/config.json',JSON.stringify(config),{mode:0o600});
await writeFile(work+'/tunnel.yml','tunnel: local-validation\ningress:\n  - hostname: existing.example.test\n    service: http://127.0.0.1:3430\n  - service: http_status:404\n',{mode:0o600});
const daemon=spawn(binary,['serve','--no-tray','--config',work+'/config.json','--tunnel-config',work+'/tunnel.yml'],{cwd:path.dirname(binary),env:{...process.env,PATH:windows?process.env.SystemRoot+'\\System32;'+process.env.SystemRoot+'\\System32\\WindowsPowerShell\\v1.0':'/usr/bin:/bin:/usr/sbin:/sbin'},stdio:['ignore','pipe','pipe'],windowsHide:true});
let events=[];daemon.stdout.on('data',data=>{for(const line of data.toString().split('\n').filter(Boolean)){const event=JSON.parse(line);events.push(event);if(event.event==='host_offline')console.log(JSON.stringify(event))}});
daemon.stderr.on('data',()=>{});
const report={mode:recoveryOnly?'history_recovery':'full',checks:[],browserErrors:[],httpErrors:[],viewports:[]};let browser,dataPage,expectedRoute;
const sleep=ms=>new Promise(resolve=>setTimeout(resolve,ms));
async function waitUntil(fn,ms=90000){const end=Date.now()+ms;while(Date.now()<end){if(await fn())return;await sleep(250)}throw new Error('CONDITION_TIMEOUT')}
async function adminAPI(page,name,body){return page.evaluate(async({name,body})=>{const session=await (await fetch('/api/session')).json();const response=await fetch('/api/'+name,{method:body===undefined?'GET':'POST',headers:{'Content-Type':'application/json','X-CSRF-Token':session.csrf},body:body===undefined?undefined:JSON.stringify(body)});return {status:response.status,data:await response.json()}},{name,body})}
try{
 await waitUntil(async()=>{try{return (await fetch('http://127.0.0.1:18766/')).ok}catch{return false}},15000);
 browser=await chromium.launch({headless:true,executablePath:windows?'C:/Program Files/Google/Chrome/Application/chrome.exe':'/Applications/Google Chrome.app/Contents/MacOS/Google Chrome',...(!windows?{proxy:{server:'http://127.0.0.1:7890',bypass:'127.0.0.1,localhost'}}:{})});
 const adminContext=await browser.newContext();const admin=await adminContext.newPage();
 admin.on('pageerror',()=>report.browserErrors.push('ADMIN_PAGE_ERROR'));
 const code=await readFile(work+'/runtime/bootstrap.code','utf8');if(!windows)assert.equal((await stat(work+'/runtime/bootstrap.code')).mode&0o777,0o600);
 await admin.goto('http://127.0.0.1:18766/#bootstrap='+code);
 await admin.waitForSelector('#gate[hidden]',{state:'attached',timeout:90000});await admin.locator('[data-view=setup]').click();assert.equal(new URL(admin.url()).hash,'');report.checks.push('admin_bootstrap');
 const pre=await (await adminContext.request.get('http://127.0.0.1:18766/api/bootstrap/csrf')).json();
 assert.equal((await adminContext.request.post('http://127.0.0.1:18766/api/bootstrap',{headers:{Origin:'http://127.0.0.1:18766','X-CSRF-Token':pre.csrf},data:{code}})).status(),403);report.checks.push('bootstrap_reuse_rejected');
 await admin.locator('#next-1').click();
 const password=randomBytes(24).toString('hex');await admin.locator('#origin').fill(config.publicOrigin);await admin.locator('#owner-password').fill(password);await admin.locator('#setup-form button.primary').click();
 await admin.waitForSelector('#step-3:not([hidden])');report.checks.push('wizard_setup');
 await admin.locator('#preview-tunnel').click();await waitUntil(async()=> (await admin.locator('#tunnel-diff').innerText()).includes('18765'));
 await admin.locator('#apply-tunnel').click();await waitUntil(async()=> (await readFile(work+'/tunnel.yml','utf8')).includes('18765'));report.checks.push('tunnel_fixture_preview_apply');
 await admin.locator('#finish-setup').click();
 const connected=await adminAPI(admin,'host/start',{});assert.equal(connected.status,200);report.checks.push('host_attached');
 const health=(await adminAPI(admin,'status')).data;assert.equal(health.shimConnected,true);assert.equal(health.hiddenWindowAlive,true);
 report.inspectorOpenMs=health.inspectorOpenMs;assert.equal(health.inspectorClosed,true);
 assert((await admin.locator('#qr').evaluate(canvas=>canvas.toDataURL())).startsWith('data:image/png'));report.checks.push('offline_qr');
 await admin.screenshot({path:work+'/admin.png',fullPage:true});
 await admin.locator('[data-view=doctor]').click();await admin.locator('#run-doctor').click();report.doctor=(await adminAPI(admin,'doctor')).data.checks;assert(report.doctor.every(check=>check.ok));report.checks.push('doctor');
 const context=await browser.newContext();const page=await context.newPage();dataPage=page;
 report.socketErrors=[];page.on('websocket',socket=>socket.on('socketerror',value=>report.socketErrors.push(value.match(/net::[A-Z_]+/)?.[0]??(/frame header/i.test(value)?'INVALID_FRAME_HEADER':/reserved bit/i.test(value)?'INVALID_RESERVED_BITS':/mask/i.test(value)?'INVALID_MASK':/utf.?8/i.test(value)?'INVALID_UTF8':'SOCKET_ERROR'))));
 page.on('dialog',async dialog=>{report.dialogs=(report.dialogs??0)+1;await dialog.accept()});
 page.on('pageerror',error=>report.browserErrors.push({code:({'Peer closed MessagePort connection.':'PEER_PORT_CLOSED','CONNECTION_LOST_STATE_UNKNOWN':'CONNECTION_LOST_STATE_UNKNOWN','CONNECTION_UNAVAILABLE':'CONNECTION_UNAVAILABLE'})[error.message]??'DATA_PAGE_ERROR'}));
 page.on('response',response=>{if(response.status()>=400&&new URL(response.url()).host==='127.0.0.1:18765')report.httpErrors.push({status:response.status(),path:new URL(response.url()).pathname})});
 page.on('requestfailed',request=>report.httpErrors.push({host:new URL(request.url()).hostname,error:(request.failure()?.errorText??'').match(/^net::[A-Z_]+/)?.[0]??'NETWORK_FAILED'}));
 page.on('response',response=>{if(response.status()>=400&&new URL(response.url()).host!=='127.0.0.1:18765')report.httpErrors.push({status:response.status(),host:new URL(response.url()).hostname})});
 await page.goto(config.publicOrigin);await page.locator('#password').fill(password);await page.locator('form button').click();
 await page.waitForFunction(()=>window.__cwbRuntime?.connected===true,undefined,{timeout:90000});report.checks.push('data_login_ws');
 await page.getByRole('textbox').first().waitFor({timeout:30000});report.checks.push('official_ui_loaded');
 const existing=page.locator('[data-app-action-sidebar-thread-row][data-app-action-sidebar-thread-pinned="false"]');
 report.stage='existing_chat';report.existingRowCount=await existing.count();report.historyAttempts=0;
 let historyLoaded=false;
 for(let index=0;index<Math.min(await existing.count(),10);index++){
  report.historyAttempts++;
  await existing.nth(index).click();const deadline=Date.now()+15000;
  while(Date.now()<deadline&&await page.locator('[data-turn-key]').count()===0){assert.equal(await page.evaluate(()=>window.__cwbRuntime.connected),true);await sleep(250)}
  if(await page.locator('[data-turn-key]').count()>0){historyLoaded=true;break}
 }
 assert(historyLoaded);report.checks.push('existing_chat_loaded');
 const viewports=[[360,800],[390,844],[430,932],[768,1024],[1024,768],[1440,900],[1920,1080],[844,390]];
 for(const [width,height] of viewports){await page.setViewportSize({width,height});await sleep(500);const box=await page.getByRole('textbox').first().boundingBox();report.viewports.push({width,height,box,documentWidth:await page.evaluate(()=>document.documentElement.scrollWidth)});assert(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth));assert(box&&box.width>=160&&box.x>=0&&box.x+box.width<=width+1&&box.y+box.height<=height+1);}report.checks.push('eight_viewports');
 await page.setViewportSize({width:1440,height:900});
 assert.equal(await page.getByRole('textbox').count(),1);
 const editor=page.getByRole('textbox').first();
 const message=recoveryOnly?await page.locator('[data-user-message-bubble], [data-message-author-role="user"]').last().innerText():randomUUID();
 if(!recoveryOnly){
 await editor.fill(message);
 const sendButton=page.getByRole('button',{name:/^(发送|发送消息|发送提示|Send|Send message|Send prompt)$/i}).first();
 await sendButton.click({timeout:10000});
 await page.getByText(message,{exact:true}).first().waitFor({timeout:90000});console.log(JSON.stringify({check:'message_present_in_ui'}));
 const turn=page.locator('[data-turn-key]').filter({has:page.getByText(message,{exact:true})}).last();
 const reply=turn.locator('[data-local-conversation-final-assistant] [data-markdown-text-style]').last();
 await reply.waitFor({timeout:180000});assert((await reply.innerText()).trim().length>0);report.checks.push('real_message_reply');console.log(JSON.stringify({check:'real_message_reply'}));
 }else report.checks.push('existing_message_recovery');
 report.channels=await page.evaluate(()=>Object.values(window.__cwbRuntime.diagnostics.ports).reduce((result,port)=>{const previous=result[port.channelType]??{sent:0,received:0};previous.sent+=port.sent;previous.received+=port.received;result[port.channelType]=previous;return result},{}));for(const name of ['connect-app-host','codex_desktop:connect-app-server-manager','codex_desktop:git-worker-port'])assert(report.channels[name]?.sent>0&&report.channels[name]?.received>0);report.checks.push('three_messageport_channels');
 report.stage='history_reload';await waitUntil(async()=>/^\/(c|local)\//.test(new URL(page.url()).searchParams.get('initialRoute')??''),10000);report.routeKind=new URL(page.url()).searchParams.get('initialRoute').split('/')[1];await page.reload();await page.locator('[data-turn-key]').getByText(message,{exact:true}).first().waitFor({timeout:60000});report.checks.push('history_read_after_reload');console.log(JSON.stringify({check:'history_read_after_reload'}));
 report.stage='takeover';const generation=await page.evaluate(()=>window.__cwbRuntime.generation);
 const secondReady=context.waitForEvent('page');await page.evaluate(()=>window.open(location.href,'_blank'));const second=await secondReady;second.on('dialog',dialog=>dialog.accept());second.on('pageerror',error=>report.browserErrors.push({code:({'Peer closed MessagePort connection.':'PEER_PORT_CLOSED','CONNECTION_LOST_STATE_UNKNOWN':'CONNECTION_LOST_STATE_UNKNOWN','CONNECTION_UNAVAILABLE':'CONNECTION_UNAVAILABLE'})[error.message]??'DATA_PAGE_ERROR',name:error.name}));
 second.on('response',response=>{if(response.request().resourceType()==='document'||response.status()>=400)report.httpErrors.push({phase:'second',type:response.request().resourceType(),status:response.status(),path:new URL(response.url()).pathname})});
 second.on('requestfailed',request=>report.httpErrors.push({phase:'second',type:request.resourceType(),error:(request.failure()?.errorText??'').match(/^net::[A-Z_]+/)?.[0]??'NETWORK_FAILED'}));
 await second.waitForLoadState('domcontentloaded');await second.locator('#cwb-status button').waitFor({timeout:30000});
 assert.equal(await page.evaluate(()=>window.__cwbRuntime.connected),true);report.checks.push('occupied_prompt');
 await second.reload();await second.locator('#cwb-status button').waitFor({timeout:30000});assert.equal(await page.evaluate(()=>window.__cwbRuntime.connected),true);assert.equal(await second.evaluate(()=>sessionStorage.getItem('cwb-owned-claim')),null);report.checks.push('copied_tab_requires_explicit_takeover');
 await second.locator('#cwb-status button').click();await second.waitForFunction(()=>window.__cwbRuntime?.connected===true,undefined,{timeout:90000});
 await page.locator('#cwb-status').filter({hasText:'控制权已被接管'}).waitFor({timeout:15000});await sleep(2200);assert.equal(await page.evaluate(()=>window.__cwbRuntime.connected),false);assert.equal(await page.evaluate(()=>window.__cwbRuntime.generation),generation);report.checks.push('explicit_takeover_4009');
 await second.getByText(message,{exact:true}).first().waitFor({timeout:30000});
 dataPage=second;report.stage='recovery';report.recoveryPasses=0;
 for(let attempt=0;attempt<3;attempt++){
  const recoveryStarted=Date.now();
  const beforeRecovery=await second.evaluate(()=>performance.timeOrigin);
  expectedRoute=new URL(second.url()).searchParams.get('initialRoute');
  await context.setOffline(true);await waitUntil(async()=>!(await second.evaluate(()=>window.__cwbRuntime?.connected)),15000);await sleep(2000);await context.setOffline(false);
  await second.waitForFunction(origin=>performance.timeOrigin!==origin&&window.__cwbRuntime?.connected,beforeRecovery,{timeout:90000});
  await second.getByText(message,{exact:true}).first().waitFor({timeout:60000});assert.equal(await second.locator('[data-user-message-bubble], [data-message-author-role="user"]').filter({hasText:message}).count(),1);report.recoveryPasses++;report.recoveryTimesMs??=[];report.recoveryTimesMs.push(Date.now()-recoveryStarted);console.log(JSON.stringify({check:'recovery_pass',attempt:attempt+1,durationMs:Date.now()-recoveryStarted}));
 }report.checks.push('disconnect_reload_no_replay');
 report.stage='inflight_write';const uncertainMessage=randomUUID();await second.getByRole('textbox').first().fill(uncertainMessage);await second.getByRole('button',{name:/^(发送|发送消息|发送提示|Send|Send message|Send prompt)$/i}).first().click();await second.getByText(uncertainMessage,{exact:true}).first().waitFor({timeout:30000});
 const interruptedTurn=second.locator('[data-turn-key]').filter({has:second.getByText(uncertainMessage,{exact:true})}).last();assert.equal(await interruptedTurn.locator('[data-local-conversation-final-assistant]').count(),0);
 const interruptedOrigin=await second.evaluate(()=>performance.timeOrigin);await context.setOffline(true);await waitUntil(async()=>!(await second.evaluate(()=>window.__cwbRuntime?.connected)),15000);await second.locator('#cwb-status').filter({hasText:'状态未知'}).waitFor({timeout:10000});await sleep(2000);await context.setOffline(false);
 await second.waitForFunction(origin=>performance.timeOrigin!==origin&&window.__cwbRuntime?.connected,interruptedOrigin,{timeout:90000});await second.getByRole('textbox').first().waitFor({timeout:60000});await sleep(3000);
 report.inflightReadbackCount=await second.locator('[data-user-message-bubble], [data-message-author-role="user"]').filter({hasText:uncertainMessage}).count();assert(report.inflightReadbackCount<=1);
 if(report.inflightReadbackCount===1)await second.locator('[data-turn-key]').filter({has:second.getByText(uncertainMessage,{exact:true})}).last().locator('[data-local-conversation-final-assistant] [data-markdown-text-style]').last().waitFor({timeout:180000});report.checks.push('inflight_write_recovery_no_replay');
 report.stage='revoke';const revoked=await adminAPI(admin,'auth/revoke-all',{});assert.equal(revoked.status,200);await waitUntil(async()=>!(await second.evaluate(()=>window.__cwbRuntime?.connected)),10000);report.checks.push('session_revoke_closes_ws');
 const updatedPassword=randomBytes(24).toString('hex');assert.equal((await adminAPI(admin,'password',{password:updatedPassword})).status,200);report.checks.push('password_change');
 const login=await context.newPage();await login.goto(config.publicOrigin);await login.locator('#password').fill(updatedPassword);await login.locator('form button').click();await login.waitForFunction(()=>window.__cwbRuntime?.connected===true,undefined,{timeout:90000});report.checks.push('updated_password_login');
 const logout=await login.evaluate(async()=>{const session=await(await fetch('/api/session')).json();return(await fetch('/auth/logout',{method:'POST',headers:{'X-CSRF-Token':session.csrf}})).status});assert.equal(logout,200);await waitUntil(async()=>!(await login.evaluate(()=>window.__cwbRuntime.connected)),10000);report.checks.push('logout_closes_ws');
 for(const action of ['stop','start','reconnect'])assert.equal((await adminAPI(admin,'host/'+action,{})).status,200);report.checks.push('admin_stop_start_reconnect');
 report.stage='listener_settings';await admin.locator('[data-view=security]').click();await admin.locator('#listener-origin').fill('http://127.0.0.1:18767');await admin.locator('#data-port').fill('18767');await admin.locator('#admin-port').fill('18768');await admin.locator('#listeners-form button').click();await admin.waitForURL('http://127.0.0.1:18768/');await admin.waitForSelector('#gate[hidden]',{state:'attached'});
 assert.equal((await adminAPI(admin,'config')).data.dataPort,18767);assert.equal((await fetch('http://127.0.0.1:18767/livez')).status,200);assert.equal((await adminAPI(admin,'listeners',{publicOrigin:config.publicOrigin,dataPort:18765,adminPort:18766})).status,200);await admin.goto('http://127.0.0.1:18766/');report.checks.push('visual_listener_change');
 assert(!report.browserErrors.some(error=>typeof error==='string'||error.code==='DATA_PAGE_ERROR'));report.checks.push('no_unexpected_javascript_errors');
}catch(error){if(dataPage&&!dataPage.isClosed())report.runtime=await dataPage.evaluate(expected=>({transport:window.__cwbRuntime?.diagnostics,connected:window.__cwbRuntime?.connected,closeCode:window.__cwbRuntime?.lastClose,socketState:window.__cwbRuntime?.socketState,status:document.querySelector('#cwb-status')?.textContent,textboxes:document.querySelectorAll('[role="textbox"]').length,turns:document.querySelectorAll('[data-turn-key]').length,routeUnchanged:new URL(location.href).searchParams.get('initialRoute')===expected,online:navigator.onLine,path:location.pathname,readyState:document.readyState}),expectedRoute);report.failure=error.name;report.events=events;process.exitCode=1}
finally{
 if(browser)await browser.close();
 const stop=spawn(binary,['stop','--config',work+'/config.json'],{stdio:'ignore'});await new Promise(resolve=>stop.on('exit',resolve));
 try{
  await waitUntil(async()=>daemon.exitCode!==null,20000);assert.equal(daemon.exitCode,0);assert(events.some(event=>event.event==='gateway_stopped'));
  for(const port of [18765,18766,18767,18768,9229]){const open=await new Promise(resolve=>{const connection=net.connect({host:'127.0.0.1',port});connection.once('connect',()=>{connection.destroy();resolve(true)});connection.once('error',()=>resolve(false))});assert.equal(open,false)}
  report.checks.push('graceful_stop_all_ports_closed');
 }catch{report.cleanupFailure='RESOURCE_RELEASE_FAILED';process.exitCode=1;if(daemon.exitCode===null)daemon.kill('SIGTERM')}
 const counts=items=>Object.values(items.reduce((result,item)=>{const normalized=typeof item==='string'?{code:item}:Object.fromEntries(['code','status','type','error'].filter(key=>item[key]!==undefined).map(key=>[key,item[key]])),key=JSON.stringify(normalized);result[key]??={...normalized,count:0};result[key].count++;return result},{}));
 report.browserErrors=counts(report.browserErrors);report.httpErrors=counts(report.httpErrors);if(!report.failure)delete report.stage;
 await writeFile(work+(recoveryOnly?'/recovery-report.json':'/report.json'),JSON.stringify(report,null,2));console.log(JSON.stringify(report));
}
