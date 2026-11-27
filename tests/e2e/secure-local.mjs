import assert from 'node:assert/strict';
import {spawn} from 'node:child_process';
import {randomBytes} from 'node:crypto';
import {mkdir,readFile,writeFile} from 'node:fs/promises';
import https from 'node:https';
import net from 'node:net';
import {fileURLToPath} from 'node:url';
const {chromium}=await import(process.env.CWB_PLAYWRIGHT_PATH??new URL('../../web/node_modules/playwright/index.mjs',import.meta.url).href);
const {default:httpProxy}=await import(process.env.CWB_HTTP_PROXY_PATH);
const root=fileURLToPath(new URL('../../',import.meta.url)).replaceAll('\\','/');
const windows=process.platform==='win32';
const binary=process.env.CWB_BINARY??root+(windows?'bin/remote-codex-windows-amd64.exe':'bin/remote-codex');
const work=root+'.work/secure-local';await mkdir(work,{recursive:true});
const config={tunnelMode:'custom',publicOrigin:'https://127.0.0.1:19443',allowedHosts:['127.0.0.1:19443'],dataPort:18765,adminPort:18766,allowInsecureLocalhost:false,trustedProxy:['127.0.0.1/32'],bootstrapPython:process.env.BOOTSTRAP_PYTHON??root+(windows?'bin/.venv/Scripts/python.exe':'bin/.venv/bin/python'),bootstrapHelper:'',auth:{mode:'password'}};
await writeFile(work+'/config.json',JSON.stringify(config),{mode:0o600});
await writeFile(work+'/tunnel.yml','tunnel: local-validation\ningress:\n  - service: http_status:404\n',{mode:0o600});
const proxy=httpProxy.createProxyServer({target:'http://127.0.0.1:18765',ws:true});
const secure=https.createServer({key:await readFile(root+'.work/windows/tls-key.pem'),cert:await readFile(root+'.work/windows/tls-cert.pem')},(request,response)=>proxy.web(request,response));
secure.on('upgrade',(request,socket,head)=>proxy.ws(request,socket,head));
await new Promise(resolve=>secure.listen(19443,'127.0.0.1',resolve));
const daemon=spawn(binary,['serve','--no-tray','--config',work+'/config.json','--tunnel-config',work+'/tunnel.yml'],{windowsHide:true,stdio:'ignore'});
const delay=ms=>new Promise(resolve=>setTimeout(resolve,ms));
async function until(check,timeout=90000){const end=Date.now()+timeout;while(Date.now()<end){if(await check())return;await delay(200)}throw new Error('CONDITION_TIMEOUT')}
let browser,context,page,upgraded=0,received=0;
const report={checks:[]};
try{
 report.stage='service';
 await until(async()=>{try{return(await fetch('http://127.0.0.1:18766/')).ok}catch{return false}},15000);
 report.stage='browser';
 browser=await chromium.launch({headless:true,args:['--ignore-certificate-errors'],executablePath:windows?'C:/Program Files/Google/Chrome/Application/chrome.exe':'/Applications/Google Chrome.app/Contents/MacOS/Google Chrome'});
 context=await browser.newContext({ignoreHTTPSErrors:true});
 const admin=await context.newPage();
 report.stage='bootstrap';
 await admin.goto('http://127.0.0.1:18766/#bootstrap='+await readFile(work+'/runtime/bootstrap.code','utf8'));
 await admin.waitForSelector('#gate[hidden]',{state:'attached',timeout:90000});await admin.locator('[data-view=setup]').click();
 await admin.locator('#next-1').click();
 report.stage='setup';
 const password=randomBytes(24).toString('hex');
 await admin.locator('#origin').fill(config.publicOrigin);await admin.locator('#owner-password').fill(password);await admin.locator('#setup-form button.primary').click();await admin.waitForSelector('#step-3:not([hidden])');
 report.stage='host';
 const started=await admin.evaluate(async()=>{const session=await(await fetch('/api/session')).json();return(await fetch('/api/host/start',{method:'POST',headers:{Origin:location.origin,'X-CSRF-Token':session.csrf,'Content-Type':'application/json'},body:'{}'})).status});assert.equal(started,200);
 page=await context.newPage();page.on('dialog',dialog=>dialog.accept());
 page.on('websocket',socket=>{if(socket.url().startsWith('wss://127.0.0.1:19443/')){upgraded++;socket.on('framereceived',()=>received++)}});
 report.stage='login';await page.goto(config.publicOrigin);await page.locator('#password').fill(password);await page.locator('form button').click();
 report.stage='wss';
 await page.waitForFunction(()=>window.__cwbRuntime?.connected,undefined,{timeout:90000});await page.getByRole('textbox').first().waitFor({timeout:30000});
 const cookies=await context.cookies(config.publicOrigin);assert(cookies.some(cookie=>cookie.name==='__Host-cwb_session'&&cookie.secure&&cookie.httpOnly&&cookie.sameSite==='Strict'));
 assert(upgraded>0&&received>0);report.checks.push('secure_cookie','https_login','wss_upgrade_and_frames','official_ui_over_tls');report.upgrades=upgraded;report.receivedFrames=received;
 delete report.stage;
}catch(error){report.failure=error.name;if(page)report.runtime=await page.evaluate(()=>({connected:window.__cwbRuntime?.connected,closeCode:window.__cwbRuntime?.lastClose,status:document.querySelector('#cwb-status')?.textContent}));process.exitCode=1}
finally{
 if(browser)await browser.close();
 const stop=spawn(binary,['stop','--config',work+'/config.json'],{windowsHide:true,stdio:'ignore'});await new Promise(resolve=>stop.on('exit',resolve));
 await until(async()=>daemon.exitCode!==null,20000);assert.equal(daemon.exitCode,0);
 proxy.close();await new Promise(resolve=>secure.close(resolve));
 for(const port of [18765,18766,19443]){const open=await new Promise(resolve=>{const socket=net.connect(port,'127.0.0.1');socket.on('connect',()=>{socket.destroy();resolve(true)});socket.on('error',()=>resolve(false))});assert.equal(open,false)}
 report.checks.push('graceful_stop_all_ports_closed');await writeFile(work+'/report.json',JSON.stringify(report,null,2));console.log(JSON.stringify(report));
}
