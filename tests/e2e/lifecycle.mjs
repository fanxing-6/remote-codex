import {mkdir,readFile,writeFile,readdir} from 'node:fs/promises';
import {spawn,execFile} from 'node:child_process';
import {promisify} from 'node:util';
import assert from 'node:assert/strict';
const root=new URL('../../',import.meta.url).pathname,binary=process.env.CWB_BINARY??root+'bin/remote-codex',work=root+'.work/lifecycle';
await mkdir(work,{recursive:true});
await writeFile(work+'/config.json',await readFile(root+'.work/e2e/config.json'),{mode:0o600});
const daemon=spawn(binary,['serve','--config',work+'/config.json','--tunnel-config',root+'.work/e2e/tunnel.yml'],{stdio:['ignore','pipe','ignore'],env:{...process.env,PATH:'/usr/bin:/bin:/usr/sbin:/sbin'}});
const events=[];let partial='';daemon.stdout.on('data',data=>{partial+=data.toString();const lines=partial.split('\n');partial=lines.pop();for(const line of lines)if(line)events.push(JSON.parse(line))});
const execute=promisify(execFile),sleep=ms=>new Promise(resolve=>setTimeout(resolve,ms));
const report={trayEnabled:true};
try{
 for(let i=0;i<100;i++){try{if((await fetch('http://127.0.0.1:18766/')).ok)break}catch{}await sleep(100)}
 let status;for(let i=0;i<120;i++){status=JSON.parse((await execute(binary,['status','--config',work+'/config.json'])).stdout);if(status.shimConnected)break;await sleep(250)}assert.equal(status.shimConnected,true);assert.equal(status.inspectorClosed,true);report.hostConnected=true;
 assert((await(await fetch('http://127.0.0.1:18766/app.js')).text()).length>1000);report.embeddedAdmin=true;
 const doctor=JSON.parse((await execute(binary,['doctor','--config',work+'/config.json'])).stdout);assert(doctor.checks.every(item=>item.ok));report.doctorPassed=true;
 if(process.env.CWB_TRAY_INSPECT==='1'){console.log('TRAY_READY');await sleep(45000)}
}finally{
 await execute(binary,['stop','--config',work+'/config.json']);for(let i=0;i<200&&daemon.exitCode===null;i++)await sleep(100);assert.equal(daemon.exitCode,0);assert(events.some(item=>item.event==='host_stopped'));assert(events.some(item=>item.event==='gateway_stopped'&&!item.errorCode));report.gracefulExit=true;report.hiddenWindowReleased=true;
 const files=await readdir(work+'/runtime');assert(!files.includes('cli-session.json'));assert(!files.includes('bootstrap.code'));report.runtimeFilesReleased=true;
 const doctor=JSON.parse((await execute(binary,['doctor','--config',work+'/config.json','--tunnel-config',root+'.work/e2e/tunnel.yml'])).stdout);assert(doctor.checks.some(item=>item.label==='临时 Inspector 端口'&&item.ok));report.offlineDoctor=true;
 await writeFile(work+'/report.json',JSON.stringify(report,null,2));console.log(JSON.stringify(report));
}
