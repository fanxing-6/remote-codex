import assert from 'node:assert/strict';
import {spawn,execFile} from 'node:child_process';
import {existsSync} from 'node:fs';
import {mkdir,mkdtemp,readFile,writeFile} from 'node:fs/promises';
import {fileURLToPath} from 'node:url';
import {promisify} from 'node:util';

const root=fileURLToPath(new URL('../../',import.meta.url));
const binary=process.env.CWB_BINARY??root+'bin/remote-codex';
await mkdir(root+'.work/auth-test',{recursive:true});
const work=await mkdtemp(root+'.work/auth-test/run-'),configPath=work+'/config.json';
const origin='http://127.0.0.1:19865',adminOrigin='http://127.0.0.1:19866';
await writeFile(configPath,JSON.stringify({tunnelMode:'custom',publicOrigin:origin,allowedHosts:['127.0.0.1:19865'],dataPort:19865,adminPort:19866,allowInsecureLocalhost:true}),{mode:0o600});
const execute=promisify(execFile),delay=ms=>new Promise(resolve=>setTimeout(resolve,ms));
const service=spawn(binary,['serve','--no-host','--no-tray','--config',configPath],{stdio:['ignore','ignore','pipe']});
const exited=new Promise(resolve=>service.once('exit',resolve));
let stderr='';service.stderr.on('data',data=>{stderr+=data});
const checks=[];
async function cli(command,...args){return execute(binary,[command,'--config',configPath,...args])}
async function api(path,body){
 const credentials=JSON.parse(await readFile(work+'/runtime/cli-session.json','utf8'));
 const response=await fetch(adminOrigin+'/api/'+path,{method:body===undefined?'GET':'POST',headers:{Origin:adminOrigin,Cookie:'cwb_admin='+credentials.id,'X-CSRF-Token':credentials.csrf,'Content-Type':'application/json'},body:body===undefined?undefined:JSON.stringify(body)});
 assert.equal(response.status,200);return response.json();
}
try{
 const deadline=Date.now()+15000;
 while(!existsSync(work+'/runtime/cli-session.json')){assert.equal(service.exitCode,null,stderr);assert(Date.now()<deadline);await delay(100)}
 const c=await api('config');assert(c.configured);assert.equal(c.tunnelMode,'custom');
 assert(JSON.parse((await cli('doctor')).stdout).checks.every(item=>item.ok));
 checks.push('isolated_service_owner_and_doctor');
 const ticket=JSON.parse((await cli('ticket','--permanent')).stdout);
 assert.equal(ticket.permanent,true);assert.equal(ticket.expiresInSec,0);
 const raw=new URL(ticket.url).hash.slice('#ticket='.length);
 let cookie;
 for(let index=0;index<2;index++){
  const challenge=await fetch(origin+'/auth/csrf'),pre=await challenge.json();
  const response=await fetch(origin+'/auth/ticket',{method:'POST',headers:{Origin:origin,Cookie:challenge.headers.getSetCookie().map(value=>value.split(';')[0]).join('; '),'X-CSRF-Token':pre.csrf,'Content-Type':'application/json'},body:JSON.stringify({ticket:raw})});
  assert.equal(response.status,200);assert.match(response.headers.get('set-cookie'),/Max-Age=34560000/);assert.match(response.headers.get('set-cookie'),/HttpOnly/);assert.match(response.headers.get('set-cookie'),/SameSite=Strict/);
  cookie=response.headers.getSetCookie().map(value=>value.split(';')[0]).join('; ');
 }
 checks.push('permanent_ticket_reusable_and_long_lived_session');
 const session=await fetch(origin+'/api/session',{headers:{Cookie:cookie}});assert.equal(session.status,200);
 const persisted=JSON.parse(await readFile(work+'/auth-state.json','utf8'));assert.equal(persisted.tickets.length,1);assert.equal(persisted.sessions.length,2);
 checks.push('permanent_authentication_persisted');
 await cli('revoke-all');assert.equal((await fetch(origin+'/api/session',{headers:{Cookie:cookie}})).status,401);
 const revoked=JSON.parse(await readFile(work+'/auth-state.json','utf8'));assert.equal(revoked.tickets.length,0);assert.equal(revoked.sessions.length,0);
 checks.push('cli_revocation_clears_persisted_sessions_and_tickets');
}finally{
 try{if(service.exitCode===null)await cli('stop')}
 finally{await Promise.race([exited,delay(15000)]);if(service.exitCode===null)service.kill('SIGTERM');await exited}
 assert.equal(service.exitCode,0,stderr);assert(!existsSync(work+'/runtime/cli-session.json'));
 checks.push('graceful_shutdown');
 await writeFile(work+'/report.json',JSON.stringify({checks},null,2));console.log(JSON.stringify({work,checks}));
}
