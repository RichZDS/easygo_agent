#!/usr/bin/env node
// Tests an already running, fixture-configured managed Compose deployment.
import assert from 'node:assert/strict';
import {writeFile,mkdir} from 'node:fs/promises';
import {join} from 'node:path';
import {randomUUID,createHash} from 'node:crypto';
const origin=process.env.EASYGO_PLATFORM_ORIGIN??'http://127.0.0.1:18980';
const adminPassword=process.env.EASYGO_ADMIN_PASSWORD;if(!adminPassword)throw Error('Bootstrap test password environment required');
const report={origin,checks:[]};const out=process.env.EASYGO_PLATFORM_EVIDENCE??'/tmp/platform-compose-proof';await mkdir(out,{recursive:true});
const sleep=ms=>new Promise(r=>setTimeout(r,ms));
function client(){let cookie='';return{async request(path,body,expected=200){const res=await fetch(origin+path,{method:body===undefined?'GET':'POST',headers:{Origin:origin,'Content-Type':'application/json',...(cookie?{Cookie:cookie}:{})},body:body===undefined?undefined:JSON.stringify(body)});const set=res.headers.get('set-cookie');if(set)cookie=set.split(';')[0];const data=await res.json();assert.equal(res.status,expected,JSON.stringify(data));return data;},async rpc(method,params={}){return(await this.request('/api/rpc',{method,params})).result;}};}
async function until(fn){for(let n=0;n<100;n++){const v=await fn();if(v)return v;await sleep(300);}throw Error('condition timeout');}
const pass=name=>{report.checks.push(name);console.log('PASS '+name);};
try{
 const admin=client(),user=client(),other=client();await admin.request('/api/login',{email:'admin@example.test',password:adminPassword});
 const suffix=randomUUID().slice(0,8),email=`compose-${suffix}@example.test`,password='compose-test-password';
 const identity=(await user.request('/api/register',{email,password},201)).user;
 await other.request('/api/register',{email:`other-${suffix}@example.test`,password},201);
 assert.equal((await user.request('/api/wallet')).balance_micros,0);
 await admin.request('/api/admin/credits',{user_id:identity.id,amount_micros:100000000,reason:'compose proof',idempotency_key:'compose-'+suffix});pass('Docker Web registration/login and admin credits');
 const session=await user.rpc('agent.session.create');
 const run=await user.rpc('agent.run.start',{session_id:session.id,input:'Reply briefly.',idempotency_key:'first'});
 const done=await until(async()=>{const r=await user.rpc('agent.run.get',{run_id:run.id});return !['queued','running'].includes(r.status)&&r;});assert.equal(done.status,'completed',JSON.stringify(done));assert.match(JSON.stringify(done.result),/PLATFORM_OK/);
 await until(async()=>(await user.request('/api/wallet')).held_micros===0);
 assert.equal((await user.request('/api/wallet')).balance_micros,99975000);pass('Docker TS loop -> mTLS gateway -> provider fixture -> token debit');
 await other.request('/api/rpc',{method:'agent.session.history',params:{session_id:session.id}},400);
 await user.request('/api/rpc',{method:'platform.wallet.settle',params:{}},400);pass('Docker public API denies foreign sessions and internal ledger methods');
 await writeFile(join(out,'host-sentinel'),'host-only');await writeFile(join(out,'sibling'),'other-workspace-only');
 const task=await user.rpc('workshop.submit',{workflow:'proof',runtime:'fixture',input:JSON.stringify({mode:'first',host_sentinel:join(out,'host-sentinel'),sibling:join(out,'sibling')}),idempotency_key:'artifact'});
 const final=await until(async()=>{const t=await user.rpc('workshop.get',{task_id:task.id});return !['queued','running','cancelling'].includes(t.status)&&t;});assert.equal(final.status,'succeeded',JSON.stringify(final));
 const artifact=final.runs[0].artifacts.find(a=>a.path==='artifact.txt');assert.equal(artifact.sha256,createHash('sha256').update('own-artifact').digest('hex'));
 if(process.env.EASYGO_REQUIRE_ARTIFACT==='1'){
  const data=await user.rpc('workshop.artifact',{task_id:task.id,path:'artifact.txt'});assert.equal(Buffer.from(data.data_base64,'base64').toString(),'own-artifact');assert.equal(data.sha256,artifact.sha256);
  await other.request('/api/rpc',{method:'workshop.artifact',params:{task_id:task.id,path:'artifact.txt'}},400);
  pass('registered artifact download verifies bytes and tenant ownership');
 }
 await until(async()=>(await user.request('/api/wallet')).held_micros===0);
 assert.equal((await user.request('/api/wallet')).balance_micros,99965000);pass('containerized workshop controls isolated child and bills native gateway usage');
 report.user_email=email;report.namespace=identity.namespace;report.session=session.id;report.task=task.id;report.wallet=await user.request('/api/wallet');report.pass=true;
}catch(e){report.pass=false;report.error=e.stack;console.error(e.stack);process.exitCode=1;}
await writeFile(join(out,'report.json'),JSON.stringify(report,null,2));
