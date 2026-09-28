#!/usr/bin/env node
// Crash/restart fault injection for the managed chain. Local fixture model only;
// never reads provider credentials. Each fault kills a real service process with
// SIGKILL at a chosen point and checks durable accounting and task recovery.
import assert from 'node:assert/strict';
import {spawn,execFile} from 'node:child_process';
import {promisify} from 'node:util';
import {once} from 'node:events';
import http from 'node:http';
import net from 'node:net';
import {mkdtemp,writeFile,readFile} from 'node:fs/promises';
import {openSync,closeSync} from 'node:fs';
import {join,resolve} from 'node:path';
import {fileURLToPath} from 'node:url';
import {randomUUID} from 'node:crypto';
import {createRPCClient} from './rpc-call.mjs';
const exec=promisify(execFile),root=resolve(fileURLToPath(new URL('..',import.meta.url)));
const go=process.env.EASYGO_GO_BIN??'go',docker=process.env.EASYGO_DOCKER_TEST_BINARY,endpoint=process.env.EASYGO_DOCKER_TEST_ENDPOINT,image=process.env.EASYGO_DOCKER_TEST_IMAGE;
if(!docker||!endpoint||!image)throw Error('Explicit dedicated Docker test binary/endpoint/image required.');
const state=await mkdtemp('/tmp/egp-fault-'),owner='fault-'+state.split('-').at(-1);
const report={state,checks:[],faults:{},model_requests:0,knowledge_requests:0,chat_requests:0,native_requests:0,paid_provider:false};
const procs={},clients=[];const sleep=ms=>new Promise(r=>setTimeout(r,ms));let model,beforeResponse;
const check=name=>{report.checks.push(name);console.log('PASS '+name);};
async function save(){await writeFile(join(state,'report.json'),JSON.stringify(report,null,2));}
async function port(){const s=net.createServer();s.listen(0,'127.0.0.1');await once(s,'listening');const p=s.address().port;await new Promise(r=>s.close(r));return p;}
async function until(fn,timeout=30000,interval=500){const end=Date.now()+timeout;while(Date.now()<end){const result=await fn();if(result)return result;await sleep(interval);}throw Error('Timed out waiting for condition');}
function launch(name,bin,args,env={}){const fd=openSync(join(state,name+'.log'),'a',0o600);const child=spawn(bin,args,{cwd:root,env:{PATH:process.env.PATH,LANG:'C.UTF-8',...env},stdio:['ignore',fd,fd]});closeSync(fd);procs[name]={name,bin,args,env,child,done:new Promise(r=>{child.once('error',e=>r({error:e.message}));child.once('exit',(code,signal)=>r({code,signal}));})};return procs[name];}
const relaunch=name=>{const p=procs[name];return launch(name,p.bin,p.args,p.env);};
// Like a supervisor restart policy: a crashed loop keeps its 30s ownership lease, so
// early restarts exit with database_owned until the lease expires.
async function recover(name,ready){const started=Date.now();let attempts=0;for(;;){attempts++;const p=relaunch(name);const r=await Promise.race([ready().then(()=>'up',()=>'timeout'),p.done.then(()=>'exited')]);if(r==='timeout')throw Error(name+' not healthy');if(r==='up')return {attempts,recovery_ms:Date.now()-started};if(Date.now()-started>90000)throw Error(name+' did not recover');await sleep(2000);}}
async function stop(name,signal='SIGTERM'){const e=procs[name];if(e.child.exitCode===null&&e.child.signalCode===null)e.child.kill(signal);let timer;await Promise.race([e.done,new Promise(r=>{timer=setTimeout(()=>{e.child.kill('SIGKILL');r();},12000);})]);clearTimeout(timer);}
function browser(origin){let cookie='';return {async request(path,body,method=body===undefined?'GET':'POST',expected=200){const r=await fetch(origin+path,{method,headers:{'Content-Type':'application/json',Origin:origin,...(cookie?{Cookie:cookie}:{})},body:body===undefined?undefined:JSON.stringify(body)});const set=r.headers.get('set-cookie');if(set)cookie=set.split(';')[0];const value=await r.json();assert.equal(r.status,expected,JSON.stringify(value));return value;},async rpc(method,params={}){return (await this.request('/api/rpc',{method,params})).result;}};}
async function containers(){const {stdout}=await exec(docker,['--host',endpoint,'ps','-aq','--filter','label=ai.easygo.workshop.owner='+owner]);return stdout.split(/\s+/).filter(Boolean);}
try{
 await exec('bash',[join(root,'scripts/dev-pki.sh'),join(state,'pki')]);
 await exec(go,['build','-o',join(state,'gateway'),'./cmd/server'],{cwd:join(root,'services/ai-gateway')});
 await exec(go,['build','-o',join(state,'workshop'),'./cmd/server'],{cwd:join(root,'services/workshop')});
 await exec('npm',['run','build'],{cwd:join(root,'services/agent-loop')});
 const [gp,wp,ap,hp]=await Promise.all([port(),port(),port(),port()]);const origin=`http://127.0.0.1:${hp}`;report.origin=origin;
 const identity=n=>({cert_file:join(state,'pki',n,'tls.crt'),key_file:join(state,'pki',n,'tls.key'),ca_file:join(state,'pki/public/ca.crt')});
 const cert=n=>join(state,'pki/public',n+'.crt');const ep=(n,p)=>({url:`https://127.0.0.1:${p}/rpc`,peer_certificate_file:cert(n)});
 const grant=(id,methods,namespaces=['*'])=>({id,cert_file:cert(id),methods,namespaces});
 model=http.createServer(async(req,res)=>{try{
  let text='';for await(const c of req)text+=c;const p=JSON.parse(text);report.model_requests++;
  assert.equal(req.headers.authorization,'Bearer fixture-key');assert.equal(p.model,'fixture-model');
  // Memory consolidation is metered too; it runs at loop startup, so answer it and count it apart.
  const textOf=c=>typeof c==='string'?c:(c??[]).map(b=>b.text??'').join(''),system=(p.messages??[]).filter(m=>m.role==='system').map(m=>textOf(m.content)).join('\n'),knowledge=/Extract durable|Reconcile prior/.test(system);
  if(knowledge)report.knowledge_requests++;
  else if(beforeResponse){const action=beforeResponse;beforeResponse=undefined;await action(req.url);}
  if(req.url==='/responses'){report.native_requests++;res.setHeader('Content-Type','application/json');res.end(JSON.stringify({id:'fixture-'+report.model_requests,status:'completed',error:null,output:[{type:'message',role:'assistant',content:[{type:'output_text',text:'fixture completed'}]}],usage:{input_tokens:7,output_tokens:3}}));return;}
  report.chat_requests++;const answer=knowledge?JSON.stringify({memories:[]}):'PLATFORM_OK',usage={prompt_tokens:20,completion_tokens:5,prompt_tokens_details:{cached_tokens:4}},message={role:'assistant',content:answer};
  if(!p.stream){res.setHeader('Content-Type','application/json');res.end(JSON.stringify({choices:[{message,finish_reason:'stop'}],usage}));return;}
  res.writeHead(200,{'Content-Type':'text/event-stream'});
  for(const value of [{choices:[{index:0,delta:{content:answer},finish_reason:null}]},{choices:[{index:0,delta:{},finish_reason:'stop'}]},{choices:[],usage}])res.write('data: '+JSON.stringify(value)+'\n\n');res.end('data: [DONE]\n\n');
 }catch(e){console.error('Fixture: '+e.message);if(!res.headersSent)res.writeHead(500);res.end();}});
 model.listen(0,'127.0.0.1');await once(model,'listening');const mp=model.address().port;
 const gateway={listen:`127.0.0.1:${gp}`,tls:identity('ai-gateway'),authorization:[grant('client',['health']),grant('agent-loop',['gateway.generate','gateway.models']),grant('workshop',['gateway.native'])],meter:{...ep('agent-loop',ap),database:join(state,'meter.db'),max_output_tokens:256},models:{chat:{protocol:'chat_completions',endpoint:`http://127.0.0.1:${mp}/chat`,model:'fixture-model',api_key_env:'FIXTURE_MODEL_KEY'},responses:{protocol:'responses',endpoint:`http://127.0.0.1:${mp}/responses`,model:'fixture-model',api_key_env:'FIXTURE_MODEL_KEY'}}};
 const workshop={listen:`127.0.0.1:${wp}`,tls:identity('workshop'),authorization:[grant('client',['health']),grant('agent-loop',['workshop.workflows','workshop.submit','workshop.get','workshop.list','workshop.cancel','workshop.resume','workshop.events','workshop.result','workshop.artifact'])],workshop:{root:join(state,'workshop-data'),concurrency:2,queue_capacity:8,max_output_bytes:4194304,model_gateway:{...ep('ai-gateway',gp),tls:identity('workshop')},sandbox:{mode:'docker',docker_binary:docker,endpoint,image,owner,host_root:join(state,'workshop-data')},engines:{codex:{binary:'codex'}},runtime_profiles:{fixture:{engine:'codex',protocol:'responses',gateway_model:'responses'}},workflows:[{name:'proof',version:'1',instructions:'Execute offline proof.',runtime:'fixture',policy:'workspace-write',timeout_seconds:180,artifacts:['artifact.txt','isolation-proof.json']}]}};
 const loop={listen:`127.0.0.1:${ap}`,tls:identity('agent-loop'),authorization:[grant('client',['health']),grant('ai-gateway',['platform.wallet.reserve','platform.wallet.settle'])],database:join(state,'agent.sqlite'),gateway:ep('ai-gateway',gp),workshop:ep('workshop',wp),model:'chat',streaming:true,max_steps:4,concurrency:8,context_bytes:128000,platform:{listen:`127.0.0.1:${hp}`,database:join(state,'platform.sqlite'),public_origin:origin,secure_cookies:false,registration:true,bootstrap_admin:{email:'admin@example.test',password_env:'TEST_ADMIN_PASSWORD'}},knowledge:{database:join(state,'knowledge.sqlite'),profile_limit:5,consolidate_interval_ms:86400000}};
 for(const [name,config]of Object.entries({gateway,workshop,loop}))await writeFile(join(state,name+'.json'),JSON.stringify(config),{mode:0o600});
 launch('gateway',join(state,'gateway'),['--config',join(state,'gateway.json')],{FIXTURE_MODEL_KEY:'fixture-key'});
 launch('workshop',join(state,'workshop'),['--config',join(state,'workshop.json')]);
 launch('loop',process.execPath,[join(root,'services/agent-loop/dist/server.js'),'--config',join(state,'loop.json')],{TEST_ADMIN_PASSWORD:'test-admin-password-v1'});
 const health={};for(const [name,p]of [['ai-gateway',gp],['workshop',wp],['agent-loop',ap]]){const tls=identity('client');health[name]=createRPCClient({url:ep(name,p).url,certFile:tls.cert_file,keyFile:tls.key_file,caFile:tls.ca_file,peerCertificateFile:cert(name)});clients.push(health[name]);}
 const healthy=async(name,timeout=30000)=>until(async()=>{try{return await health[name].health();}catch{return false;}},timeout);
 for(const name of Object.keys(health))await healthy(name);
 const admin=browser(origin),alice=browser(origin);await admin.request('/api/login',{email:'admin@example.test',password:'test-admin-password-v1'});
 const au=(await alice.request('/api/register',{email:'alice@example.test',password:'alice-test-password'},'POST',201)).user;
 await admin.request('/api/admin/credits',{user_id:au.id,amount_micros:1000000000,reason:'fault fixture',idempotency_key:'fault-grant'});
 const session=(await alice.rpc('agent.session.create')).id;
 const wallet=()=>alice.request('/api/wallet'),receipts=async()=>(await alice.request('/api/usage')).receipts;
 const terminalTask=id=>until(async()=>{const t=await alice.rpc('workshop.get',{task_id:id});return !['queued','running','cancelling'].includes(t.status)&&t;},60000,750);
 const terminalRun=id=>until(async()=>{const v=await alice.rpc('agent.run.get',{run_id:id});return !['queued','running'].includes(v.status)&&v;});
 const newest=async known=>{const all=await receipts();const fresh=all.filter(r=>!known.has(r.request_id));assert.equal(fresh.length,1,JSON.stringify(fresh));return fresh[0];};
 const calls=()=>report.model_requests-report.knowledge_requests;
 const quiesce=async()=>{await alice.rpc('agent.memory.consolidate');await until(async()=>(await wallet()).held_micros===0);};
 const snapshot=async()=>{await quiesce();return {wallet:await wallet(),known:new Set((await receipts()).map(r=>r.request_id)),model:calls()};};
 const loopDown=async()=>{await stop('loop','SIGKILL');report.faults.current.killed_at=Date.now();};
 // Warm path: one normal run so the steady-state charge is established.
 const warm=await alice.rpc('agent.run.start',{session_id:session,input:'warm',idempotency_key:randomUUID()});assert.equal((await terminalRun(warm.id)).status,'completed');
 await until(async()=>(await wallet()).held_micros===0);

 // F1: wallet dies while a native model call is in flight. The provider answer
 // must be receipted durably by the gateway and settled exactly once after restart.
 {
  report.faults.current=report.faults.wallet_down_native={};const before=await snapshot();
  beforeResponse=async url=>{assert.equal(url,'/responses');await loopDown();};
  const task=await alice.rpc('workshop.submit',{workflow:'proof',runtime:'fixture',input:JSON.stringify({mode:'first',host_sentinel:join(state,'none'),sibling:join(state,'none')}),idempotency_key:'fault-native'});
  await until(()=>procs.loop.child.exitCode!==null||procs.loop.child.signalCode!==null);
  await sleep(4000);Object.assign(report.faults.current,await recover('loop',()=>healthy('agent-loop',95000)));report.faults.current.downtime_ms=Date.now()-report.faults.current.killed_at;
  assert.equal((await terminalTask(task.id)).status,'succeeded');
  await until(async()=>(await wallet()).held_micros===0);
  const r=await newest(before.known),after=await wallet();
  assert.equal(r.source,'gateway.native');assert.equal(r.status,'settled');assert.equal(r.charged_micros,10000);assert.equal(r.settlement.usage.input_tokens,7);assert.equal(r.settlement.usage.output_tokens,3);
  assert.equal(before.wallet.balance_micros-after.balance_micros,10000);assert.equal(calls()-before.model,1);
  await sleep(3000);assert.equal((await wallet()).balance_micros,after.balance_micros);
  Object.assign(report.faults.current,{request_id:r.request_id,charged_micros:r.charged_micros,provider_requests:calls()-before.model});
  check('wallet crash during native call: durable gateway receipt settles exactly once after restart');
 }

 // F2: wallet (and the loop client) dies during a main-chain streaming call. The
 // gateway either keeps known usage or holds the reservation; never free, never double.
 {
  report.faults.current=report.faults.wallet_down_chat={};const before=await snapshot();
  beforeResponse=async url=>{assert.equal(url,'/chat');await loopDown();};
  const run=await alice.rpc('agent.run.start',{session_id:session,input:'FAULT_CHAT',idempotency_key:randomUUID()});
  await until(()=>procs.loop.child.exitCode!==null||procs.loop.child.signalCode!==null);
  await sleep(4000);Object.assign(report.faults.current,await recover('loop',()=>healthy('agent-loop',95000)));
  const final=await terminalRun(run.id);assert.equal(final.status,'interrupted');assert.equal(final.error.code,'interrupted');
  const r=await until(async()=>{const x=(await receipts()).find(v=>!before.known.has(v.request_id));return x?.settlement&&x;});
  let w=await wallet();
  if(r.status==='settled'){assert.equal(r.charged_micros,25000);assert.equal(before.wallet.balance_micros-w.balance_micros,25000);assert.equal(w.held_micros,0);}
  else{
   assert.equal(r.status,'pending');assert.equal(r.settlement.usage.known,false);assert.equal(w.held_micros,r.reserved_micros);assert.equal(w.balance_micros,before.wallet.balance_micros);
   const listed=(await admin.request('/api/admin/usage/pending')).receipts.find(v=>v.request_id===r.request_id);assert.equal(listed.status,'pending');
   await admin.request('/api/admin/usage/resolve',{namespace:au.namespace,request_id:r.request_id,decision:'settle',usage:{known:true,input_tokens:20,output_tokens:5},reason:'provider console shows 20/5',idempotency_key:'fault-chat-resolve'});
   w=await wallet();assert.equal(w.held_micros,0);assert.equal(before.wallet.balance_micros-w.balance_micros,25000);
  }
  assert.equal(calls()-before.model,1);await sleep(3000);assert.equal((await wallet()).balance_micros,w.balance_micros);
  Object.assign(report.faults.current,{request_id:r.request_id,gateway_outcome:r.status,run_status:final.status,provider_requests:calls()-before.model});
  check(`wallet crash during chat stream: ${r.status} receipt, run interrupted without replay, exactly one charge`);
 }

 // F3: gateway dies after dispatching to the provider. Restart must convert the
 // active claim into an uncertain receipt that keeps the hold for reconciliation.
 {
  report.faults.current=report.faults.gateway_down_chat={};const before=await snapshot();
  beforeResponse=async url=>{assert.equal(url,'/chat');await stop('gateway','SIGKILL');report.faults.current.killed_at=Date.now();};
  const run=await alice.rpc('agent.run.start',{session_id:session,input:'FAULT_GATEWAY',idempotency_key:randomUUID()});
  const final=await terminalRun(run.id);assert.equal(final.status,'failed');
  const held=await wallet();assert.ok(held.held_micros>0,'hold must survive gateway crash');assert.equal(held.balance_micros,before.wallet.balance_micros);
  relaunch('gateway');await healthy('ai-gateway');report.faults.current.downtime_ms=Date.now()-report.faults.current.killed_at;
  const r=await until(async()=>{const x=(await receipts()).find(v=>!before.known.has(v.request_id));return x?.status==='pending'&&x;});
  assert.equal(r.settlement.outcome,'uncertain');assert.equal(r.settlement.usage.known,false);assert.equal((await wallet()).held_micros,r.reserved_micros);
  assert.ok((await admin.request('/api/admin/usage/pending')).receipts.some(v=>v.request_id===r.request_id));
  await admin.request('/api/admin/usage/resolve',{namespace:au.namespace,request_id:r.request_id,decision:'release',reason:'provider shows no completion',idempotency_key:'fault-gateway-release'});
  const w=await wallet();assert.equal(w.held_micros,0);assert.equal(w.balance_micros,before.wallet.balance_micros);
  const resolved=(await receipts()).find(v=>v.request_id===r.request_id);assert.equal(resolved.resolution.payload.decision,'release');assert.equal(resolved.status,'resolved_released');
  assert.equal(calls()-before.model,1);
  const retry=await alice.rpc('agent.run.start',{session_id:session,input:'after gateway restart',idempotency_key:randomUUID()});assert.equal((await terminalRun(retry.id)).status,'completed');
  await until(async()=>(await wallet()).held_micros===0);assert.equal(before.wallet.balance_micros-(await wallet()).balance_micros,25000);
  Object.assign(report.faults.current,{request_id:r.request_id,reserved_micros:r.reserved_micros,run_status:final.status,resolution:'release'});
  check('gateway crash after dispatch: uncertain hold kept, listed for admin, released once; service recovers');
 }

 // F4: workshop controller dies while a task container runs. Restart reaps the
 // orphan container and marks the task interrupted without replaying it.
 {
  report.faults.current=report.faults.workshop_down={};const before=await snapshot();
  const task=await alice.rpc('workshop.submit',{workflow:'proof',runtime:'fixture',input:'{"mode":"sleep"}',idempotency_key:'fault-workshop'});
  await until(async()=>{try{return await readFile(join(state,'workshop-data/workspaces',task.id,'ready'),'utf8');}catch{return false;}});
  await stop('workshop','SIGKILL');const orphans=await containers();assert.equal(orphans.length,1);report.faults.current.orphans_after_kill=orphans.length;
  relaunch('workshop');await healthy('workshop');
  assert.deepEqual(await containers(),[]);
  const t=await alice.rpc('workshop.get',{task_id:task.id});assert.equal(t.status,'interrupted');assert.equal(t.runs.length,1);
  await sleep(2000);assert.equal((await alice.rpc('workshop.get',{task_id:task.id})).runs.length,1);assert.equal(calls()-before.model,0);
  Object.assign(report.faults.current,{task:task.id,status:t.status,runs:t.runs.length,orphans_after_restart:0});
  check('workshop crash during container run: orphan reaped, task interrupted, nothing replayed');
 }

 // F5: graceful restart of all services preserves accounts, history and ledger.
 {
  const view=async()=>({wallet:await wallet(),usage:(await receipts()).map(({request_id,status,charged_micros})=>({request_id,status,charged_micros})),history:(await alice.rpc('agent.session.history',{session_id:session})).messages.map(m=>m.seq)});
  await quiesce();const before=await view();for(const name of ['loop','workshop','gateway'])await stop(name);
  for(const name of ['gateway','workshop','loop'])relaunch(name);for(const name of Object.keys(health))await healthy(name);
  assert.deepEqual(await view(),before);check('graceful restart of all three services preserves wallet, receipts and history');
 }
 const all=await receipts(),w=await wallet();
 const charged=all.reduce((s,r)=>s+r.charged_micros,0);assert.equal(1000000000-w.balance_micros,charged);assert.equal(w.held_micros,0);
 report.final={balance_micros:w.balance_micros,held_micros:w.held_micros,receipts:all.length,charged_micros:charged};
 check('final ledger: balance equals grant minus the sum of recorded charges, no holds left');
 report.pass=true;
}catch(e){report.pass=false;report.error=e.stack;console.error(e.stack);process.exitCode=1;}
finally{delete report.faults.current;clients.forEach(c=>c.close());for(const name of Object.keys(procs).reverse())await stop(name);if(model){model.closeAllConnections();await new Promise(r=>model.close(r));}try{for(const id of await containers())await exec(docker,['--host',endpoint,'rm','--force',id]);}catch{}await save();console.log('REPORT '+join(state,'report.json'));}
