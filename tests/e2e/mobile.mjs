import assert from 'node:assert/strict';
import {readFile, writeFile, mkdir, readdir, stat} from 'node:fs/promises';
import {resolve,sep} from 'node:path';
import {fileURLToPath} from 'node:url';
import {execFile} from 'node:child_process';
import {promisify} from 'node:util';
import {createHash} from 'node:crypto';
import {chromium, devices} from '../../web/node_modules/playwright/index.mjs';

const root=fileURLToPath(new URL('../../',import.meta.url));
assert(process.env.REMOTE_CODEX_E2E_CONFIG,'REMOTE_CODEX_E2E_CONFIG must identify an isolated configuration');
const configPath=resolve(process.env.REMOTE_CODEX_E2E_CONFIG);
assert(configPath.startsWith(resolve(root,'.work')+sep),'E2E configuration must be inside .work');
const configurationDir=resolve(configPath,'..');
const work=root+'.work/mobile';
await mkdir(work,{recursive:true});
const hash=value=>createHash('sha256').update(value).digest('hex');
const configuration=await readFile(configPath);
const config=JSON.parse(configuration);
const directTLS=process.env.CWB_DIRECT_TLS==='1';
const publicHost=new URL(config.publicOrigin).hostname;
if(directTLS)assert(config.tlsPort);
const state=async()=>JSON.parse(await readFile(configurationDir+'/auth-state.json','utf8'));
const permanentTickets=(await state()).tickets.filter(item=>item.permanent);
async function certificateHashes(){
 const result={};
 if(!config.tlsAutocert)return result;
 for(const name of await readdir(configurationDir+'/certs',{recursive:true})){
  const path=configurationDir+'/certs/'+name;
  if((await stat(path)).isFile())result[name]=hash(await readFile(path));
 }
 return result;
}
const certificates=await certificateHashes();
const execute=promisify(execFile);
const binary=process.env.CWB_BINARY??root+'bin/remote-codex';
const {stdout}=await execute(binary,['ticket','--config',configPath,'--ttl','900']);
const login=JSON.parse(stdout);
const browser=await chromium.launch({headless:true,executablePath:process.env.CHROME_PATH,args:directTLS?['--host-resolver-rules=MAP '+publicHost+':443 127.0.0.1:'+config.tlsPort]:[],proxy:process.env.E2E_PROXY?{server:process.env.E2E_PROXY,bypass:'127.0.0.1,localhost'+(directTLS?','+publicHost:'')}:undefined});
const report={checks:[],focus:[],layouts:[],pickers:[],errors:[],teardownEvents:[],origin:config.publicOrigin,access:directTLS?'local TLS':'public tunnel'};
const attachment=work+'/cwb-mobile-verification.txt';
await writeFile(attachment,await readFile(root+'README.md'));
const attachmentName='cwb-mobile-verification.txt';
let context,page;
let closingContext=false;
const drafts=new Map();
function pageError(error){
 if(closingContext&&error.message==='Peer closed MessagePort connection.')report.teardownEvents.push(error.message);
 else report.errors.push(error.message);
}
const plus=()=>page.getByRole('button',{name:/^(添加文件等内容|Add files and more)$/});
const editor=()=>page.getByRole('textbox').first();
const sleep=ms=>new Promise(resolve=>setTimeout(resolve,ms));
async function settle(){await sleep(800)}
async function captureNativeFocus(){
 await context.addInitScript(()=>{window.__originalFocus=HTMLElement.prototype.focus});
}
async function noEditableFocus(name){
 const active=await page.evaluate(()=>{
  const element=document.activeElement;
  const editable=element instanceof HTMLTextAreaElement || element instanceof HTMLInputElement&&!['button','checkbox','radio','file','hidden','image','range','reset','submit','color'].includes(element.type) || element instanceof HTMLElement&&(element.isContentEditable||['','true','plaintext-only'].includes(element.getAttribute('contenteditable')??'false'));
  return {editable,tag:element?.tagName,role:element?.getAttribute('role')};
 });
 report.focus.push({name,...active});assert.equal(active.editable,false,name);
}
async function clickOutsideComposer(){
 const point=await editor().evaluate(element=>{
  const composer=element.closest('[data-composer-surface-variant]'),main=element.closest('main');
  for(let y=80;y<composer.getBoundingClientRect().top-10;y+=16)for(let x=80;x<innerWidth-10;x+=16){
   const target=document.elementFromPoint(x,y);
   if(target instanceof HTMLElement&&main.contains(target)&&!target.isContentEditable&&!target.closest('button,a,input,select,label,[role=button],[role=menu],[role=menuitem],[data-composer-surface-variant]'))return {x,y};
  }
  throw Error('main blank area required');
 });
 await page.touchscreen.tap(point.x,point.y);
}
async function focusProtection(name){
 await noEditableFocus(name+' navigation');
 await editor().evaluate(element=>element.focus());await noEditableFocus(name+' script focus');
 await editor().dispatchEvent('click');await editor().evaluate(element=>element.focus());await noEditableFocus(name+' untrusted click');
 await editor().evaluate(element=>window.__originalFocus.call(element));await noEditableFocus(name+' native focus bypass');
 await page.evaluate(()=>{window.dispatchEvent(new Event('focus'));document.dispatchEvent(new Event('visibilitychange'))});
 await settle();await noEditableFocus(name+' window focus and visibility');
 await plus().tap();await settle();await noEditableFocus(name+' attachment menu open');
 await page.keyboard.press('Escape');await settle();await noEditableFocus(name+' attachment menu close');
 const model=page.locator('[data-composer-surface-variant]').getByRole('button',{name:/模型|model|GPT|Auto|自动|Pro/i}).first();
 await model.tap();await settle();await noEditableFocus(name+' model menu open');
 await page.keyboard.press('Escape');await page.mouse.move(0,0);await settle();await noEditableFocus(name+' model menu close');
 await clickOutsideComposer();await settle();
 await editor().click();
 assert(await editor().evaluate(element=>element===document.activeElement||element.contains(document.activeElement)),name+' user click');
 const original=await editor().innerText();
 try{
  await page.keyboard.insertText('中文');await page.keyboard.type(' English');
  assert((await editor().innerText()).includes('中文 English'),name+' input');
  await sleep(650);await editor().evaluate(element=>element.focus());
  assert(await editor().evaluate(element=>element===document.activeElement||element.contains(document.activeElement)),name+' active editor focus');
  await editor().locator('p').first().click();
  const nested=await editor().evaluate(element=>{
   const paragraph=element.querySelector('p');
   if(!paragraph)throw Error('editor paragraph required');
   const previous=paragraph.getAttribute('tabindex');
   let childFocused=false;
   const observe=event=>{if(event.target===paragraph&&document.activeElement===paragraph)childFocused=true};
   document.addEventListener('focusin',observe,true);
   paragraph.tabIndex=0;
   try{
    window.__originalFocus.call(paragraph);element.focus();
    return {childFocused,returned:document.activeElement===element};
   }finally{
    document.removeEventListener('focusin',observe,true);
    if(previous===null)paragraph.removeAttribute('tabindex');else paragraph.setAttribute('tabindex',previous);
   }
  });
  assert.deepEqual(nested,{childFocused:true,returned:true},name+' editor internal focus synchronization');
 }finally{
  await editor().locator('p').first().click();await editor().fill(original);
 }
 await plus().click();await noEditableFocus(name+' active editor attachment menu');
 await page.keyboard.press('Escape');await page.mouse.move(0,0);await settle();await noEditableFocus(name+' active editor menu close');
 await editor().click();await editor().blur();
 await editor().evaluate(element=>element.focus());await noEditableFocus(name+' keyboard dismissal');
 await editor().click();await clickOutsideComposer();
 await editor().evaluate(element=>element.focus());await settle();await noEditableFocus(name+' outside click and async focus');
 await editor().click();
 await page.evaluate(()=>window.dispatchEvent(new Event('blur')));
 await page.evaluate(()=>window.dispatchEvent(new Event('focus')));
 await editor().evaluate(element=>element.focus());await noEditableFocus(name+' window return');
 const blank=await editor().evaluate(element=>{
  const composer=element.closest('[data-composer-surface-variant]');
  const rectangle=composer.getBoundingClientRect();
  for(let y=rectangle.top+6;y<rectangle.bottom-6;y+=8)for(let x=rectangle.left+6;x<rectangle.right-6;x+=8){
   const target=document.elementFromPoint(x,y);
   if(target instanceof HTMLElement&&composer.contains(target)&&!target.isContentEditable&&!target.closest('button,a,input,select,[role=button],[role=menu],[role=menuitem],[role=listbox],[role=option],[contenteditable=false]'))return {x,y};
  }
  throw Error('composer blank area required');
 });
 await page.touchscreen.tap(blank.x,blank.y);await editor().evaluate(element=>element.focus());
 assert(await editor().evaluate(element=>element===document.activeElement||element.contains(document.activeElement)),name+' composer blank intent');
 await editor().blur();await page.touchscreen.tap(blank.x,blank.y);await editor().blur();await sleep(650);
 await editor().evaluate(element=>element.focus());await noEditableFocus(name+' expired blank intent');
 const plusFollowsEditor=await editor().evaluate((element,button)=>!!(element.compareDocumentPosition(button)&Node.DOCUMENT_POSITION_FOLLOWING),await plus().elementHandle());
 await plus().focus();await page.keyboard.press(plusFollowsEditor?'Shift+Tab':'Tab');
 assert(await editor().evaluate(element=>element===document.activeElement||element.contains(document.activeElement)),name+' trusted Tab navigation');await editor().blur();
 await editor().evaluate(element=>element.focus());await noEditableFocus(name+' Tab authorization consumed');
 report.checks.push(name+' mobile focus protection and Chinese English input');
}
async function released(){
 const end=Date.now()+90000;
 while(Date.now()<end){
  const {stdout}=await execute(binary,['status','--config',configPath]);
  const status=JSON.parse(stdout);
  if(!status.controllerActive&&status.shimConnected)return;
  await sleep(1000);
 }
 throw Error('controller release timeout');
}
async function closeContext(){
 closingContext=true;
 await page.evaluate(()=>window.dispatchEvent(new Event('offline')));
 await sleep(250);
 await context.close();
}
async function home(mode){
 await page.getByRole('button',{name:/^(首页|Home)$/}).click();
 await page.getByRole('button',{name:mode==='chat'?/^(聊天|Chat)$/:/^(工作|Work)$/}).click();
 await editor().waitFor();await settle();
 if(!drafts.has(mode))drafts.set(mode,await editor().innerText());
}
async function layout(name){
 await settle();
 const result=await editor().evaluate(input=>{
  const composer=input.closest('[data-composer-surface-variant]');
  const rectangle=element=>element.getBoundingClientRect().toJSON();
  return {editor:rectangle(input),composer:rectangle(composer),root:rectangle(document.getElementById('root')),shell:rectangle(document.querySelector('[data-app-shell-frame]')),rail:rectangle(document.querySelector('.app-shell-left-panel')),viewport:{width:visualViewport.width,height:visualViewport.height,top:visualViewport.offsetTop},documentWidth:document.documentElement.scrollWidth,scrollY,keyboard:document.documentElement.dataset.cwbKeyboard};
 });
 report.layouts.push({name,...result});
 const bottom=result.viewport.top+result.viewport.height;
 assert(result.editor.top>=result.viewport.top-1, name+' editor top');
 assert(result.editor.bottom<=bottom+1,name+' editor bottom');
 assert(result.editor.left>=0&&result.editor.right<=result.viewport.width+1,name+' editor width');
 assert(result.documentWidth<=result.viewport.width+1,name+' horizontal overflow');
 assert(Math.abs(result.shell.top-result.viewport.top)<=1,name+' header');
 assert(Math.abs(result.rail.width-52)<=1,name+' navigation rail');
 assert(Math.abs(result.composer.bottom-bottom)<=1,name+' composer gap: '+(bottom-result.composer.bottom));
 assert.equal(result.scrollY,0,name+' document scroll');
}
async function keyboardLayouts(name){
 await page.setViewportSize({width:390,height:844});await layout(name+' normal');
 await editor().blur();const before=await page.evaluate(()=>({...window.__mobileEvents}));
 await editor().click();await page.setViewportSize({width:390,height:420});await layout(name+' keyboard');
 const after=await page.evaluate(()=>({...window.__mobileEvents}));
 assert(after.viewportResize>before.viewportResize,name+' visualViewport resize event');
 assert(after.focusin>before.focusin,name+' focusin event');
 assert.equal(await page.locator('html').getAttribute('data-cwb-keyboard'),'true');
 if(name.endsWith('home'))await page.screenshot({path:work+'/'+name.replaceAll(' ','-')+'-keyboard.png'});
 await page.setViewportSize({width:390,height:844});await layout(name+' restored');
 await noEditableFocus(name+' viewport keyboard dismissal');
 await editor().evaluate(element=>element.focus());await noEditableFocus(name+' keyboard dismissal async focus');
}
async function choose(button,name,upload=false){
 const [picker]=await Promise.all([page.waitForEvent('filechooser'),button.click()]);
 const attributes=await picker.element().evaluate(input=>({accept:input.getAttribute('accept'),capture:input.getAttribute('capture'),label:input.getAttribute('aria-label')}));
 report.pickers.push({name,...attributes});
 if(!/camera|相机|拍照/i.test(name)){assert(!attributes.accept||attributes.accept==='*/*');assert.equal(attributes.capture,null)}
 else {assert.notEqual(attributes.capture,null);assert.equal(attributes.accept,'image/*')}
 await picker.setFiles(upload?attachment:[]);
 await settle();await noEditableFocus(name+' file picker return');
 if(upload){
  await page.getByText(attachmentName,{exact:true}).first().waitFor({timeout:60000});
  await settle();
  assert.equal(await page.getByText(/^(未能附加文件。请重试。|Unable to attach files. Try again.)$/).count(),0);
  await page.locator('[data-composer-surface-variant]').getByRole('button',{name:/移除|删除|Remove/i}).last().click();
  await page.getByText(attachmentName,{exact:true}).first().waitFor({state:'hidden'});
  report.checks.push(name+' text attachment received and removed');
 }
}
async function sharedMenu(mode){
 await home(mode);await plus().click();
 await choose(page.getByRole('button',{name:/^(照片|Photos)$/}).last(),mode+' photos',true);
 await plus().click();await choose(page.getByRole('button',{name:/^(文件|Files)$/}).last(),mode+' files',true);
 await plus().click();await choose(page.getByRole('button',{name:/^(相机|Camera)$/}).last(),mode+' camera');
 // 逐一触发页面实际提供的输入元素，覆盖隐藏的媒体入口及 showPicker。
 for(const input of await page.locator('input[type=file]').all()){
  if(await input.isDisabled())continue;
  const label=await input.getAttribute('aria-label');
  const [picker]=await Promise.all([page.waitForEvent('filechooser'),input.evaluate(element=>element.click())]);
  const attributes=await picker.element().evaluate(element=>({accept:element.getAttribute('accept'),capture:element.getAttribute('capture')}));
  report.pickers.push({name:mode+' direct '+label,...attributes});
  if(attributes.capture===null)assert(!attributes.accept||attributes.accept==='*/*');
  await picker.setFiles([]);
 }
 const input=page.locator('input[type=file]:not([capture])').first();
 const [picker]=await Promise.all([page.waitForEvent('filechooser'),input.evaluate(element=>element.showPicker())]);
 assert.equal(await picker.element().getAttribute('accept'),null);await picker.setFiles([]);
 report.checks.push(mode+' click and showPicker');
}
async function thread(kind){
 await page.setViewportSize({width:390,height:844});
 await page.getByRole('button',{name:/^(显示侧边栏|Show sidebar)$/}).click();
 const rows=kind==='chatgpt'?page.locator('[data-sidebar-chatgpt-conversation-key]'):page.locator('[data-app-action-sidebar-thread-row][data-app-action-sidebar-thread-kind="'+kind+'"]');
 await rows.first().waitFor();await rows.first().click();
 await page.getByRole('button',{name:/^(隐藏侧边栏|Hide sidebar)$/}).click();
 await editor().waitFor({timeout:60000});await settle();
}
async function chatConversation(){
 await home('chat');await plus().click();
 const [picker]=await Promise.all([page.waitForEvent('filechooser'),page.getByRole('button',{name:/^(文件|Files)$/}).last().click()]);
 assert.equal(await picker.element().getAttribute('accept'),null);await picker.setFiles(attachment);
 await page.getByText(attachmentName,{exact:true}).first().waitFor({timeout:60000});
 await editor().click();await editor().fill('请确认收到附件，并仅回复：CWB_BROWSER_OK');
 await page.getByRole('button',{name:/^(发送|发送消息|发送提示|Send|Send message|Send prompt)$/i}).click();
 await noEditableFocus('chat send');
 await page.getByText(/^CWB_BROWSER_OK[。.!]?$/).filter({visible:true}).first().waitFor({timeout:180000});
 await settle();await noEditableFocus('chat streamed response');
 report.checks.push('chat attachment submitted and real assistant replied');
 await keyboardLayouts('chat thread');
}
try{
 report.stage='Android login';await released();
 context=await browser.newContext({...devices['Pixel 7'],viewport:{width:390,height:844}});
 await captureNativeFocus();
 closingContext=false;page=await context.newPage();page.setDefaultTimeout(15000);page.on('pageerror',pageError);
 await page.goto(login.url,{waitUntil:'domcontentloaded'});await page.waitForURL(url=>url.pathname==='/',{timeout:30000});await page.waitForFunction(()=>window.__cwbRuntime?.connected,undefined,{timeout:90000});
 await editor().waitFor({timeout:120000});
 await settle();await noEditableFocus('Android initial load');
 for(const path of ['/cwb/browser.js','/cwb/mobile.css']){
  const resource=await page.locator(path.endsWith('.js')?'script[src^="'+path+'"]':'link[href^="'+path+'"]');
  const url=await resource.getAttribute(path.endsWith('.js')?'src':'href');assert.match(url,/\?cwb=mobile-focus-v2$/);
  const response=await page.evaluate(async url=>{const response=await fetch(url);return {status:response.status,cacheControl:response.headers.get('cache-control')}},url);
  assert.equal(response.status,200);assert.equal(response.cacheControl,'no-store');
 }
 report.checks.push('browser runtime and stylesheet versioned with no-store');
 await page.evaluate(()=>{window.__mobileEvents={viewportResize:0,focusin:0};visualViewport.addEventListener('resize',()=>window.__mobileEvents.viewportResize++);document.addEventListener('focusin',()=>window.__mobileEvents.focusin++)});
 assert.match(await page.locator('meta[name=viewport]').getAttribute('content'),/viewport-fit=cover, interactive-widget=resizes-content/);
 for(const mode of ['chat','work']){
  report.stage=mode+' home';console.log(JSON.stringify({stage:report.stage}));
  await home(mode);
  await page.evaluate(()=>{const url=new URL(location.href);url.searchParams.set('initialRoute','/');history.replaceState(history.state,'',url)});
  await page.reload({waitUntil:'domcontentloaded'});
  await page.waitForFunction(()=>window.__cwbRuntime?.connected,undefined,{timeout:90000});await editor().waitFor({timeout:120000});await settle();
  assert.equal(await page.getByRole('button',{name:mode==='chat'?/^(聊天|Chat)$/:/^(工作|Work)$/}).getAttribute('aria-pressed'),'true');
  await noEditableFocus(mode+' first screen after reload');
  await page.evaluate(()=>{window.__mobileEvents={viewportResize:0,focusin:0};visualViewport.addEventListener('resize',()=>window.__mobileEvents.viewportResize++);document.addEventListener('focusin',()=>window.__mobileEvents.focusin++)});
  await focusProtection(mode+' home');await keyboardLayouts(mode+' home');await sharedMenu(mode);
  await page.screenshot({path:work+'/'+mode+'-home.png'});
 }
 report.stage='work thread';console.log(JSON.stringify({stage:report.stage}));await thread('local');await focusProtection('work thread');await keyboardLayouts('work thread');
 await plus().click();await choose(page.getByRole('button',{name:/^(文件和文件夹|Files and folders)$/}).last(),'work thread files',false);
 const before=new Set(await readdir(homedir()+'/.codex/attachments',{recursive:true}));
 await plus().click();const [picker]=await Promise.all([page.waitForEvent('filechooser'),page.getByRole('button',{name:/^(文件和文件夹|Files and folders)$/}).last().click()]);
 assert.equal(await picker.element().getAttribute('accept'),null);await picker.setFiles(attachment);
 await page.getByText(attachmentName,{exact:true}).first().waitFor({timeout:60000});
 await noEditableFocus('work thread uploaded file return');
 const created=(await readdir(homedir()+'/.codex/attachments',{recursive:true})).filter(name=>!before.has(name)&&name.endsWith('/'+attachmentName));
 assert.equal(created.length,1);assert.equal(hash(await readFile(homedir()+'/.codex/attachments/'+created[0])),hash(await readFile(attachment)));
 report.checks.push('work thread file bytes verified on host');
 await page.getByRole('button',{name:new RegExp('移除.*'+attachmentName.replaceAll('.','\\.'))}).click();
 report.stage='chat history';await home('chat');await thread('chatgpt');await focusProtection('chat history');
 report.stage='chat thread';console.log(JSON.stringify({stage:report.stage}));await chatConversation();
 await plus().click();await choose(page.getByRole('button',{name:/^(文件|Files)$/}).last(),'chat thread files',true);
 for(const mode of ['chat','work']){await home(mode);await editor().click();await editor().fill(drafts.get(mode));await editor().blur();}
 report.checks.push('home drafts preserved');
 const storage=await context.storageState();await closeContext();
 report.stage='iOS focus';console.log(JSON.stringify({stage:report.stage}));await released();
 context=await browser.newContext({...devices['iPhone 13'],storageState:storage});await captureNativeFocus();
 closingContext=false;page=await context.newPage();page.setDefaultTimeout(15000);page.on('pageerror',pageError);
 await page.goto(config.publicOrigin);await page.waitForFunction(()=>window.__cwbRuntime?.connected,undefined,{timeout:90000});await editor().waitFor({timeout:120000});await settle();
 await noEditableFocus('iOS initial load');
 for(const mode of ['chat','work']){await home(mode);await focusProtection('iOS '+mode+' home');}
 await closeContext();
 for(const [platform,userAgent] of [['macOS','Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/142.0.0.0 Safari/537.36'],['Windows','Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/142.0.0.0 Safari/537.36']]){
  report.stage=platform+' desktop';console.log(JSON.stringify({stage:report.stage}));await released();
  context=await browser.newContext({userAgent,viewport:{width:1440,height:900},storageState:storage});
  await captureNativeFocus();
  closingContext=false;page=await context.newPage();page.on('pageerror',pageError);await page.goto(config.publicOrigin);
  await page.waitForFunction(()=>window.__cwbRuntime?.connected,undefined,{timeout:90000});await editor().waitFor({timeout:120000});
  for(const mode of ['chat','work']){
   await home(mode);assert.equal(await page.evaluate(()=>window.__cwbRuntime.mobile),false);
   assert.equal(await page.locator('html').getAttribute('data-cwb-mobile'),null);
   await assert.rejects(page.evaluate(()=>window.electronBridge.getPathForFile()),/FILE_UPLOAD_NOT_SUPPORTED_V1/);
   assert.match(await page.evaluate(()=>HTMLInputElement.prototype.click.toString()),/\[native code\]/);
   assert(await page.evaluate(()=>HTMLElement.prototype.focus===window.__originalFocus));
   await editor().blur();await editor().focus();
   assert(await editor().evaluate(element=>element===document.activeElement||element.contains(document.activeElement)),platform+' programmatic focus');
   await editor().blur();await editor().evaluate(element=>new Promise(resolve=>requestAnimationFrame(()=>{element.focus();resolve()})));
   assert(await editor().evaluate(element=>element===document.activeElement||element.contains(document.activeElement)),platform+' asynchronous focus');
   assert.equal(await page.locator('input[type=file][aria-label="添加照片"]').getAttribute('accept'),'image/*');
   for(const viewport of [{width:1440,height:900},{width:1920,height:1080},{width:1024,height:768}]){
    await page.setViewportSize(viewport);await settle();const rectangle=await editor().boundingBox();
    assert(rectangle.x>=0&&rectangle.y>=0&&rectangle.x+rectangle.width<=viewport.width+1&&rectangle.y+rectangle.height<=viewport.height+1);
    assert(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth));
   }
  }
  report.checks.push(platform+' browser layout and native picker attributes');await closeContext();
 }
 assert.deepEqual(report.errors,[]);
 report.checks.push('no page script errors');
}catch(error){
 if(page&&!page.isClosed()){
  await writeFile(work+'/failure-ui.txt',await page.locator('body').ariaSnapshot());
  await writeFile(work+'/failure-composers.json',JSON.stringify(await page.locator('[data-composer-surface-variant]').evaluateAll(elements=>elements.map(element=>element.outerHTML)),null,2));
 }
 report.failure={message:error.message,stack:error.stack};throw error;
}
finally{
 await browser.close();
 assert.deepEqual((await state()).tickets.filter(item=>item.permanent),permanentTickets);
 assert.equal(hash(await readFile(configPath)),hash(configuration));
 assert.deepEqual(await certificateHashes(),certificates);
 report.checks.push('isolated configuration certificates and permanent tickets preserved');
 await writeFile(work+'/report.json',JSON.stringify(report,null,2));
 console.log(JSON.stringify(report,null,2));
}
