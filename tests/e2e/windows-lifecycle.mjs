import assert from 'node:assert/strict';
import {execFile,spawn} from 'node:child_process';
import {mkdir,readFile,readdir,writeFile} from 'node:fs/promises';
import net from 'node:net';
import {fileURLToPath} from 'node:url';
import {promisify} from 'node:util';
const execute=promisify(execFile);
const root=fileURLToPath(new URL('../../',import.meta.url)).replaceAll('\\','/');
const binary=process.env.CWB_BINARY??root+'bin/remote-codex-windows-amd64.exe',work=root+'.work/windows-lifecycle';
await mkdir(work,{recursive:true});
await writeFile(work+'/config.json',await readFile(root+'.work/e2e/config.json'));
const daemon=spawn(binary,['serve','--config',work+'/config.json','--tunnel-config',root+'.work/e2e/tunnel.yml'],{windowsHide:true,stdio:'ignore'});
const delay=ms=>new Promise(resolve=>setTimeout(resolve,ms));
async function until(check,timeout=90000){const end=Date.now()+timeout;while(Date.now()<end){if(await check())return;await delay(250)}throw new Error('CONDITION_TIMEOUT')}
async function cli(name,custom=true){return JSON.parse((await execute(binary,[name,...(custom?['--config',work+'/config.json']:[])],{windowsHide:true})).stdout)}
async function api(name,body){const credentials=JSON.parse(await readFile(work+'/runtime/cli-session.json','utf8'));const response=await fetch('http://127.0.0.1:18766/api/'+name,{method:'POST',headers:{Origin:'http://127.0.0.1:18766',Cookie:'cwb_admin='+credentials.id,'X-CSRF-Token':credentials.csrf,'Content-Type':'application/json'},body:JSON.stringify(body)});assert.equal(response.status,200)}
async function powershell(script){return(await execute('powershell.exe',['-NoProfile','-NonInteractive','-Command',script],{windowsHide:true})).stdout.trim()}
const report={checks:[]};let registered=false,launcherStarted=false;
try{
 await until(async()=>{try{return(await cli('status')).shimConnected}catch{return false}});report.checks.push('tray_host_attached');
 assert((await cli('doctor')).checks.every(check=>check.ok));report.checks.push('tray_doctor');
 assert.equal(await powershell("@(Get-ScheduledTask | Where-Object TaskName -eq 'RemoteCodex').Count"),'0');
 await api('autostart',{enabled:true});registered=true;
 const task=JSON.parse(await powershell("$t=Get-ScheduledTask -TaskName RemoteCodex; @{logonType=[string]$t.Principal.LogonType;runLevel=[string]$t.Principal.RunLevel;triggers=$t.Triggers.Count;arguments=$t.Actions.Arguments} | ConvertTo-Json -Compress"));
 assert.equal(task.logonType,'Interactive');assert.equal(task.runLevel,'Limited');assert.equal(task.triggers,1);assert(task.arguments.includes('--tunnel-config'));report.checks.push('interactive_scheduled_task_registered');
 await api('autostart',{enabled:false});registered=false;
 assert.equal(await powershell("@(Get-ScheduledTask | Where-Object TaskName -eq 'RemoteCodex').Count"),'0');report.checks.push('scheduled_task_removed');
 await execute(binary,['stop','--config',work+'/config.json'],{windowsHide:true});await until(async()=>daemon.exitCode!==null,20000);assert.equal(daemon.exitCode,0);
 const files=await readdir(work+'/runtime');assert(!files.includes('bootstrap.code')&&!files.includes('cli-session.json'));report.checks.push('tray_graceful_cleanup');
 await execute('cmd.exe',['/d','/c',root+'打开控制面板.cmd'],{windowsHide:true,timeout:40000});launcherStarted=true;
 await until(async()=>{try{return(await cli('status',false)).shimConnected}catch{return false}});
 assert((await cli('doctor',false)).checks.every(check=>check.ok));report.checks.push('cmd_launcher_default_browser','default_configuration_host_attached','default_configuration_doctor');
}catch(error){report.failure=error.name;report.errorCode=/^[A-Z_]+$/.test(error.message)?error.message:'LIFECYCLE_CHECK_FAILED';process.exitCode=1}
finally{
 if(registered)await api('autostart',{enabled:false});
 if(daemon.exitCode===null){await execute(binary,['stop','--config',work+'/config.json'],{windowsHide:true});await until(async()=>daemon.exitCode!==null,20000)}
 if(launcherStarted)await execute(binary,['stop'],{windowsHide:true});
 for(const port of [18765,18766,9229])await until(async()=>!(await new Promise(resolve=>{const socket=net.connect(port,'127.0.0.1');socket.on('connect',()=>{socket.destroy();resolve(true)});socket.on('error',()=>resolve(false))})),20000);
 report.checks.push('all_ports_released');await writeFile(work+'/report.json',JSON.stringify(report,null,2));console.log(JSON.stringify(report));
}
