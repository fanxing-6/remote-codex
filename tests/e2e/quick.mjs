import assert from 'node:assert/strict';
import {spawn,execFile} from 'node:child_process';
import {existsSync} from 'node:fs';
import {mkdir,mkdtemp,readFile,writeFile,stat} from 'node:fs/promises';
import {fileURLToPath} from 'node:url';
import {promisify} from 'node:util';
import {chromium} from '../../web/node_modules/playwright/index.mjs';
import {build} from '../../web/node_modules/esbuild/lib/main.js';

const root=fileURLToPath(new URL('../../',import.meta.url));
const binary=process.env.CWB_BINARY??root+'bin/remote-codex';
await mkdir(root+'.work/quick-test',{recursive:true});
const work=await mkdtemp(root+'.work/quick-test/run-');
const configPath=work+'/config.json',adminOrigin='http://127.0.0.1:19766';
await writeFile(configPath,JSON.stringify({dataPort:19765,adminPort:19766}),{mode:0o600});
const execute=promisify(execFile),delay=ms=>new Promise(resolve=>setTimeout(resolve,ms));
const report={checks:[],events:[]};
const daemon=spawn(binary,['serve','--no-tray','--no-host','--config',configPath],{stdio:['ignore','pipe','pipe']});
const exited=new Promise(resolve=>daemon.once('exit',resolve));
let partial='',stderr='';
daemon.stdout.on('data',data=>{partial+=data;const lines=partial.split('\n');partial=lines.pop();for(const line of lines)if(line)report.events.push(JSON.parse(line))});
daemon.stderr.on('data',data=>{stderr+=data});
async function until(check,timeout=90000){const deadline=Date.now()+timeout;while(Date.now()<deadline){if(await check())return;assert.equal(daemon.exitCode,null,stderr);await delay(200)}throw Error('CONDITION_TIMEOUT')}
async function cli(command,...args){return JSON.parse((await execute(binary,[command,'--config',configPath,...args])).stdout)}
async function api(path,body){
 const owner=JSON.parse(await readFile(work+'/runtime/cli-session.json','utf8'));
 const response=await fetch(adminOrigin+'/api/'+path,{method:body===undefined?'GET':'POST',headers:{Origin:adminOrigin,Cookie:'cwb_admin='+owner.id,'X-CSRF-Token':owner.csrf,'Content-Type':'application/json'},body:body===undefined?undefined:JSON.stringify(body)});
 assert.equal(response.status,200,await response.clone().text());return response.json();
}
function processGone(pid){try{process.kill(pid,0);return false}catch(error){if(error.code==='ESRCH')return true;throw error}}
async function publicGET(origin,path,name){
 const output=work+'/'+name;
 const {stdout}=await execute('curl',[...(process.env.E2E_PROXY?['--proxy',process.env.E2E_PROXY]:[]),'--retry','8','--retry-all-errors','--retry-delay','5','--connect-timeout','10','--max-time','20','--fail','--silent','--show-error','--output',output,'--write-out','%{http_code}',origin+path],{timeout:240000});
 assert.equal(stdout,'200');return readFile(output,'utf8');
}
let browser,quickPID;
try{
 await until(()=>existsSync(work+'/runtime/cli-session.json'),15000);
 let status;
 await until(async()=>{status=await cli('status');return status.quickTunnelState==='running'});
 quickPID=status.quickTunnelPID;
 assert.match(status.publicOrigin,/^https:\/\/[a-z0-9-]+\.trycloudflare\.com$/);
 assert(quickPID>0);assert.equal(status.shimConnected,false);
 report.checks.push('allocated_https_origin_without_host_attachment');
 const saved=JSON.parse(await readFile(configPath,'utf8'));
 assert.equal(saved.tunnelMode,'quick');assert.equal(saved.quickTunnelProtocol,'http2');assert.equal(saved.publicOrigin,'');assert.deepEqual(saved.allowedHosts,[]);assert.equal(saved.allowInsecureLocalhost,false);
 assert.equal(saved.auth.password.algo,'scrypt-v1');assert.equal(saved.auth.password.hash.length,64);assert.equal(saved.auth.password.salt.length,32);
 assert.equal((await stat(configPath)).mode&0o777,0o600);
 const passwordRecord=JSON.stringify(saved.auth.password);
 report.checks.push('automatic_scrypt_owner_and_private_atomic_configuration');
 assert((await cli('doctor')).checks.every(item=>item.ok));report.checks.push('quick_mode_doctor');
 assert.deepEqual(JSON.parse(await publicGET(status.publicOrigin,'/livez','livez.json')),{ok:true});
 assert.match(await publicGET(status.publicOrigin,'/auth/login','login.html'),/remote-codex/);
 report.checks.push('public_livez_200','public_login_200');
 const rejected=await fetch('http://127.0.0.1:19765/livez');assert.equal(rejected.status,403);
 const login=await cli('ticket');assert(login.url.startsWith(status.publicOrigin+'/auth/login#ticket='));
 assert.equal((await cli('ticket','--permanent')).permanent,true);
 report.checks.push('strict_host_and_temporary_permanent_ticket_cli');
 browser=await chromium.launch({headless:true,executablePath:process.env.CHROME_PATH});
 const context=await browser.newContext();
 const page=await context.newPage();
 const errors=[];page.on('pageerror',error=>errors.push(error.message));
 await page.goto(adminOrigin+'/#bootstrap='+await readFile(work+'/runtime/bootstrap.code','utf8'));
 await page.locator('#gate[hidden]').waitFor({state:'attached'});
 await page.locator('#qr:visible').waitFor();
 assert.match(await page.locator('#tunnel-state').innerText(),/一键临时通道/);
 assert.equal(await page.locator('#public-link').getAttribute('href'),status.publicOrigin);
 assert(await page.locator('#qr').evaluate(canvas=>canvas.width>0&&canvas.height>0));
 report.checks.push('local_admin_entry_and_login_qr');
 await page.locator('#restart-tunnel').click();
 await until(async()=>{const next=await cli('status');return next.quickTunnelState==='running'&&next.quickTunnelPID!==quickPID});
 assert(processGone(quickPID));status=await cli('status');quickPID=status.quickTunnelPID;
 await page.waitForFunction(origin=>document.getElementById('public-link').getAttribute('href')===origin,status.publicOrigin);
 report.checks.push('admin_restart_updates_origin_and_qr_reaps_child');
 const killedPID=quickPID;
 process.kill(killedPID,'SIGTERM');
 await until(async()=>{status=await cli('status');return status.quickTunnelState==='running'&&status.quickTunnelPID!==killedPID});
 assert(processGone(killedPID));quickPID=status.quickTunnelPID;
 assert(report.events.some(item=>item.event==='quick_tunnel_exited'));
 report.checks.push('unexpected_exit_logged_and_automatically_reconnected');
 await api('listeners',{publicOrigin:status.publicOrigin,dataPort:19765,adminPort:19766});
 assert.equal((await api('config')).tunnelMode,'quick');assert.equal((await cli('status')).quickTunnelPID,quickPID);
 report.checks.push('listener_settings_preserve_quick_mode');
 await page.locator('#change-mode').click();await page.locator('#tunnel-mode').selectOption('custom');
 await page.locator('#origin').fill('https://chat.example.com');
 await page.locator('#setup-form button.primary').click();
 await page.locator('#step-3:not([hidden])').waitFor();
 assert.equal((await api('config')).tunnelMode,'custom');assert(processGone(quickPID));
 const custom=await execute('curl',['--silent','--show-error','--header','Host: chat.example.com','--output',work+'/custom-livez.json','--write-out','%{http_code}','http://127.0.0.1:19765/livez']);assert.equal(custom.stdout,'200');
 report.checks.push('custom_mode_switch_keeps_strict_host_and_stops_quick_child');
 await api('setup',{tunnelMode:'custom',mode:'password',password:'',publicOrigin:'http://127.0.0.1:19765'});
 const runtimeScript=work+'/mobile-runtime.js';
 await build({entryPoints:[root+'tests/e2e/mobile-runtime.ts'],bundle:true,platform:'browser',format:'iife',outfile:runtimeScript});
 for(const userAgent of ['Mozilla/5.0 (Linux; Android 14) AppleWebKit/537.36 Chrome/142.0.0.0 Mobile Safari/537.36','Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 Version/18.0 Mobile/15E148 Safari/604.1']){
  const mobile=await browser.newContext({userAgent,viewport:{width:390,height:844},isMobile:true,hasTouch:true});
  await mobile.addInitScript({path:runtimeScript});
  const loginPage=await mobile.newPage();await loginPage.goto('http://127.0.0.1:19765/auth/login');
  const input=loginPage.locator('#password');
  const noFocus=async()=>assert.equal(await input.evaluate(element=>document.activeElement===element),false);
  assert.equal(await loginPage.locator('html').getAttribute('data-cwb-mobile'),'true');
  await input.evaluate(element=>element.focus());await noFocus();
  await input.evaluate(element=>window.__originalFocus.call(element));await noFocus();
  await input.dispatchEvent('click');await input.evaluate(element=>element.focus());await noFocus();
  await input.tap();assert(await input.evaluate(element=>document.activeElement===element));
  await loginPage.setViewportSize({width:390,height:420});
  await loginPage.waitForFunction(()=>document.documentElement.dataset.cwbKeyboard==='true');
  assert.equal(await loginPage.locator('html').evaluate(element=>element.style.getPropertyValue('--cwb-vvh')),'420px');
  await loginPage.setViewportSize({width:390,height:844});
  await loginPage.waitForFunction(()=>document.documentElement.dataset.cwbKeyboard==='false');
  await noFocus();await input.evaluate(element=>element.focus());await noFocus();
  await input.tap();await loginPage.evaluate(()=>window.dispatchEvent(new Event('blur')));await input.evaluate(element=>element.focus());await noFocus();
  for(const width of [360,390,430,768]){await loginPage.setViewportSize({width,height:844});assert(await loginPage.evaluate(()=>document.documentElement.scrollWidth<=innerWidth))}
  await mobile.close();
 }
 const desktop=await browser.newContext();await desktop.addInitScript({path:runtimeScript});const desktopLogin=await desktop.newPage();await desktopLogin.goto('http://127.0.0.1:19765/auth/login');
 assert(await desktopLogin.evaluate(()=>HTMLElement.prototype.focus===window.__originalFocus));assert.equal(await desktopLogin.locator('html').getAttribute('data-cwb-mobile'),null);await desktop.close();
 report.checks.push('android_ios_focus_guards_and_viewport_runtime','desktop_native_focus_preserved');
 await api('setup',{tunnelMode:'quick',mode:'password',password:'',publicOrigin:'',quickTunnelProtocol:'http2'});
 status=await cli('status');quickPID=status.quickTunnelPID;
 assert.equal(status.tunnelMode,'quick');assert.equal(status.quickTunnelState,'running');
 assert.equal(JSON.stringify(JSON.parse(await readFile(configPath,'utf8')).auth.password),passwordRecord);
 report.checks.push('quick_mode_restored_with_existing_owner_password');
 const record=JSON.parse(await readFile(configPath,'utf8')).auth.password;
 await execute(binary,['revoke-all','--config',configPath]);assert.deepEqual(record,saved.auth.password);
 assert.deepEqual(errors,[]);report.checks.push('no_admin_script_errors');
}finally{
 if(browser)await browser.close();
 try{if(daemon.exitCode===null)await execute(binary,['stop','--config',configPath])}
 finally{await Promise.race([exited,delay(15000)]);if(daemon.exitCode===null)daemon.kill('SIGTERM');await exited}
 assert.equal(daemon.exitCode,0,stderr);
 if(quickPID)assert(processGone(quickPID));
 assert(!existsSync(work+'/runtime/cli-session.json'));assert(!existsSync(work+'/runtime/bootstrap.code'));
 report.checks.push('clean_service_and_cloudflared_shutdown');
 await writeFile(work+'/report.json',JSON.stringify(report,null,2));
 console.log(JSON.stringify({work,checks:report.checks}));
}
