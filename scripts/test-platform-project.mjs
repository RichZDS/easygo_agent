#!/usr/bin/env node
// Opt-in, billable: a real model builds the six-file project from
// scripts/platform-project-spec.md inside an isolated task container, submitted
// through the authenticated platform like any user task, then the independent
// oracle judges the workspace. The provider key is read from a file and given
// only to the gateway process; it is never printed or written to evidence.
import assert from 'node:assert/strict';
import {spawn,execFile} from 'node:child_process';
import {promisify} from 'node:util';
import {once} from 'node:events';
import net from 'node:net';
import {mkdir,mkdtemp,writeFile,readFile,readdir,lstat} from 'node:fs/promises';
import {openSync,closeSync,createReadStream} from 'node:fs';
import {join,resolve} from 'node:path';
import {fileURLToPath} from 'node:url';
import {randomUUID} from 'node:crypto';
import {createRPCClient} from './rpc-call.mjs';
const exec=promisify(execFile),root=resolve(fileURLToPath(new URL('..',import.meta.url)));
const env=name=>{const v=process.env[name];if(!v)throw Error('Missing '+name);return v;};
const go=process.env.EASYGO_GO_BIN??'go',docker=env('EASYGO_DOCKER_TEST_BINARY'),endpoint=env('EASYGO_DOCKER_TEST_ENDPOINT');
const image=process.env.EASYGO_LIVE_RUNTIME_IMAGE??'easygo-task-runtime:platform',runtime=process.env.EASYGO_LIVE_RUNTIME??'codex',model=process.env.EASYGO_LIVE_MODEL??'deepseek-flash';
const repairs=Number(process.env.EASYGO_LIVE_REPAIR_ROUNDS??1),budgetCredits=Number(process.env.EASYGO_LIVE_CREDITS??3000),taskSeconds=Number(process.env.EASYGO_LIVE_TASK_SECONDS??1500);
assert.ok(Number.isInteger(repairs)&&repairs>=0&&repairs<=3&&Number.isInteger(budgetCredits)&&budgetCredits>0&&budgetCredits<=100000&&Number.isInteger(taskSeconds)&&taskSeconds<=3600);
const key=(await readFile(env('EASYGO_LIVE_KEY_FILE'),'utf8')).trim();if(key.length<16||/\s/.test(key))throw Error('Key file must hold one API key');
const parent=resolve(process.env.EASYGO_LIVE_STATE_ROOT??join(root,'..','easygo-live-project'));await mkdir(parent,{recursive:true,mode:0o700});
const state=await mkdtemp(join(parent,'run-')),owner='live-'+state.split('-').at(-1);
const report={state,runtime,model,image,budget_credits:budgetCredits,rounds:[],paid_provider:true};
const children=[],clients=[];const sleep=ms=>new Promise(r=>setTimeout(r,ms));
async function save(){await writeFile(join(state,'report.json'),JSON.stringify(report,null,2));}
async function port(){const s=net.createServer();s.listen(0,'127.0.0.1');await once(s,'listening');const p=s.address().port;await new Promise(r=>s.close(r));return p;}
async function until(fn,timeout,interval){const end=Date.now()+timeout;while(Date.now()<end){const result=await fn();if(result)return result;await sleep(interval);}throw Error('Timed out waiting for condition');}
function start(name,bin,args,extra={}){const fd=openSync(join(state,name+'.log'),'a',0o600);const child=spawn(bin,args,{cwd:root,env:{PATH:process.env.PATH,LANG:'C.UTF-8',...extra},stdio:['ignore',fd,fd]});closeSync(fd);const entry={name,child,done:new Promise(r=>{child.once('error',e=>r({error:e.message}));child.once('exit',(code,signal)=>r({code,signal}));})};children.push(entry);return entry;}
async function stop(e){if(e.child.exitCode===null&&e.child.signalCode===null)e.child.kill('SIGTERM');let timer;await Promise.race([e.done,new Promise(r=>{timer=setTimeout(()=>{e.child.kill('SIGKILL');r();},12000);})]);clearTimeout(timer);}
function browser(origin){let cookie='';return {async request(path,body,method=body===undefined?'GET':'POST',expected=200){const r=await fetch(origin+path,{method,headers:{'Content-Type':'application/json',Origin:origin,...(cookie?{Cookie:cookie}:{})},body:body===undefined?undefined:JSON.stringify(body)});const set=r.headers.get('set-cookie');if(set)cookie=set.split(';')[0];const value=await r.json();assert.equal(r.status,expected,JSON.stringify(value));return value;},async rpc(method,params={}){return (await this.request('/api/rpc',{method,params})).result;}};}
async function oracle(workspace){
 const started=Date.now();
 const args=['--host',endpoint,'run','--rm','--name','oracle-'+randomUUID().slice(0,12),'--network','none','--read-only','--user','1000:1000','--cap-drop','ALL','--security-opt','no-new-privileges=true','--pids-limit','128','--memory','512m','--memory-swap','512m','--cpus','1','--tmpfs','/tmp:rw,nosuid,nodev,noexec,size=67108864,mode=1777','--mount',`type=bind,src=${workspace},dst=/project,readonly`,'--mount',`type=bind,src=${join(root,'scripts/platform-project-oracle.mjs')},dst=/oracle.mjs,readonly`,'--entrypoint','/usr/local/bin/node',image,'/oracle.mjs','/project'];
 let out,code=0;try{out=(await exec(docker,args,{timeout:90000,maxBuffer:1<<20})).stdout;}catch(e){out=e.stdout??'';code=e.code??1;}
 let result;try{result=JSON.parse(out.trim().split('\n').at(-1));}catch{result={ok:false,failure:'oracle output unreadable'};}
 // A crashed or killed oracle cannot pass on a stale ok:true line.
 return {exit:code,wall_ms:Date.now()-started,...result,ok:code===0&&result.ok===true};
}
// Streams every regular file of any size; chunks overlap so a key cannot hide across a boundary.
async function fileHasKey(path){let tail='';for await(const chunk of createReadStream(path,{encoding:'latin1',highWaterMark:1<<20})){const text=tail+chunk;if(text.includes(key))return true;tail=text.slice(-(key.length-1));}return false;}
async function scanForKey(dir,found=[]){for(const name of await readdir(dir)){const path=join(dir,name),info=await lstat(path);if(info.isDirectory())await scanForKey(path,found);else if(info.isFile()&&await fileHasKey(path))found.push(path);}return found;}
try{
 const spec=await readFile(join(root,'scripts/platform-project-spec.md'),'utf8');
 await exec('bash',[join(root,'scripts/dev-pki.sh'),join(state,'pki')]);
 await exec(go,['build','-o',join(state,'gateway'),'./cmd/server'],{cwd:join(root,'services/ai-gateway')});
 await exec(go,['build','-o',join(state,'workshop'),'./cmd/server'],{cwd:join(root,'services/workshop')});
 await exec('npm',['run','build'],{cwd:join(root,'services/agent-loop')});
 const [gp,wp,ap,hp]=await Promise.all([port(),port(),port(),port()]);const origin=`http://127.0.0.1:${hp}`;
 const identity=n=>({cert_file:join(state,'pki',n,'tls.crt'),key_file:join(state,'pki',n,'tls.key'),ca_file:join(state,'pki/public/ca.crt')});
 const cert=n=>join(state,'pki/public',n+'.crt');const ep=(n,p)=>({url:`https://127.0.0.1:${p}/rpc`,peer_certificate_file:cert(n)});
 const grant=(id,methods,namespaces=['*'])=>({id,cert_file:cert(id),methods,namespaces});
 const gateway=JSON.parse(await readFile(join(root,'services/ai-gateway/config.deepseek.example.json'),'utf8'));
 Object.assign(gateway,{listen:`127.0.0.1:${gp}`,tls:identity('ai-gateway'),authorization:[grant('client',['health']),grant('agent-loop',['gateway.generate','gateway.models']),grant('workshop',['gateway.native'])],meter:{...ep('agent-loop',ap),database:join(state,'meter.db'),max_output_tokens:4096}});
 for(const [alias,m]of Object.entries(gateway.models)){m.model=model;m.parameters=alias==='responses'?{reasoning:{effort:'low'}}:{thinking:{type:'disabled'}};}
 const workshopConfig=JSON.parse(await readFile(join(root,'services/workshop/config.runtimes.example.json'),'utf8')).workshop;
 const profiles={codex:'codex-responses',claude:'claude-messages',pi:'pi-main',openclaw:'openclaw-main'};assert.ok(profiles[runtime],'runtime must be codex, claude, pi or openclaw');
 const workshop={listen:`127.0.0.1:${wp}`,tls:identity('workshop'),authorization:[grant('client',['health']),grant('agent-loop',['workshop.workflows','workshop.submit','workshop.get','workshop.list','workshop.cancel','workshop.resume','workshop.events','workshop.result','workshop.artifact'])],workshop:{root:join(state,'workshop-data'),concurrency:1,queue_capacity:4,max_output_bytes:4194304,model_gateway:{...ep('ai-gateway',gp),tls:identity('workshop')},sandbox:{mode:'docker',docker_binary:docker,endpoint,image,owner,host_root:join(state,'workshop-data'),memory_bytes:2147483648,nano_cpus:2000000000,pids_limit:256,tmpfs_bytes:268435456},engines:workshopConfig.engines,runtime_profiles:workshopConfig.runtime_profiles,
  workflows:[{name:'build-project',version:'1',instructions:'Implement the software project specified in the user input inside the current workspace. Create exactly the files it lists, using Node.js built-ins only; nothing can be installed and there is no network. Run the project and its tests with node to check your work before finishing, then summarize what you built.',runtime:profiles[runtime],policy:'workspace-write',timeout_seconds:taskSeconds,artifacts:['package.json','src/store.mjs','src/server.mjs','bin/server.mjs','README.md','tests/smoke.test.mjs']}]}};
 const loop={listen:`127.0.0.1:${ap}`,tls:identity('agent-loop'),authorization:[grant('client',['health']),grant('ai-gateway',['platform.wallet.reserve','platform.wallet.settle'])],database:join(state,'agent.sqlite'),gateway:ep('ai-gateway',gp),workshop:ep('workshop',wp),model:'chat',streaming:true,max_steps:20,concurrency:4,context_bytes:128000,platform:{listen:`127.0.0.1:${hp}`,database:join(state,'platform.sqlite'),public_origin:origin,secure_cookies:false,registration:true,bootstrap_admin:{email:'admin@example.test',password_env:'LIVE_ADMIN_PASSWORD'}},knowledge:{database:join(state,'knowledge.sqlite'),profile_limit:5,consolidate_interval_ms:86400000}};
 for(const [name,config]of Object.entries({gateway,workshop,loop}))await writeFile(join(state,name+'.json'),JSON.stringify(config),{mode:0o600});
 const adminPassword='live-admin-'+randomUUID();
 start('gateway',join(state,'gateway'),['--config',join(state,'gateway.json')],{DEEPSEEK_API_KEY:key});
 start('workshop',join(state,'workshop'),['--config',join(state,'workshop.json')]);
 start('loop',process.execPath,[join(root,'services/agent-loop/dist/server.js'),'--config',join(state,'loop.json')],{LIVE_ADMIN_PASSWORD:adminPassword});
 for(const [name,p]of [['ai-gateway',gp],['workshop',wp],['agent-loop',ap]]){const tls=identity('client');const c=createRPCClient({url:ep(name,p).url,certFile:tls.cert_file,keyFile:tls.key_file,caFile:tls.ca_file,peerCertificateFile:cert(name)});clients.push(c);await until(async()=>{try{return await c.health();}catch{return false;}},30000,250);}
 const admin=browser(origin),user=browser(origin);await admin.request('/api/login',{email:'admin@example.test',password:adminPassword});
 const account=(await user.request('/api/register',{email:'builder@example.test',password:'builder-'+randomUUID()},'POST',201)).user;
 // The grant bounds spend: admission stops once available credits run out. Only a
 // request whose actual usage exceeds its own conservative reservation can overshoot,
 // and that overage is recorded as debt, not dropped.
 await admin.request('/api/admin/credits',{user_id:account.id,amount_micros:budgetCredits*1000000,reason:'live project benchmark budget',idempotency_key:'live-budget'});
 const catalog=await user.rpc('agent.workshop.catalog');report.catalog=catalog;
 const terminal=async id=>until(async()=>{const t=await user.rpc('workshop.get',{task_id:id});return !['queued','running','cancelling'].includes(t.status)&&t;},(taskSeconds+120)*1000,5000);
 let task=await user.rpc('workshop.submit',{workflow:'build-project',runtime:profiles[runtime],input:spec,idempotency_key:'live-project'});
 const workspace=join(state,'workshop-data/workspaces',task.id);report.task=task.id;
 for(let round=0;round<=repairs;round++){
  const began=Date.now();task=await terminal(task.id);const verdict=await oracle(workspace);
  const run=task.runs.at(-1);report.rounds.push({round,status:task.status,error:run?.error??null,elapsed_ms:Date.now()-began,artifacts:(run?.artifacts??[]).map(a=>({path:a.path,size:a.size})),oracle:verdict});await save();
  console.log(`ROUND ${round} task=${task.status} oracle=${verdict.ok?'pass':'fail'} ${verdict.phase??''} ${verdict.failure??''}`);
  if(verdict.ok||round===repairs)break;
  const failure=`The independent acceptance test failed in phase "${verdict.phase??'unknown'}": ${verdict.failure??'no detail'}. Re-read the specification from the first message, fix the project in place, keep exactly the six files, run your own checks with node, then summarize.`;
  task=await user.rpc('workshop.resume',{task_id:task.id,input:failure});
 }
 await until(async()=>(await user.request('/api/wallet')).held_micros===0,120000,2000);
 const receipts=(await user.request('/api/usage')).receipts;const w=await user.request('/api/wallet');
 report.usage={requests:receipts.length,statuses:Object.fromEntries([...new Set(receipts.map(r=>r.status))].map(k=>[k,receipts.filter(r=>r.status===k).length])),input_tokens:receipts.reduce((s,r)=>s+(r.settlement?.usage?.input_tokens??0),0),cache_read_tokens:receipts.reduce((s,r)=>s+(r.settlement?.usage?.cache_read_tokens??0),0),output_tokens:receipts.reduce((s,r)=>s+(r.settlement?.usage?.output_tokens??0),0),charged_micros:receipts.reduce((s,r)=>s+r.charged_micros,0)};
 report.wallet={balance_micros:w.balance_micros,held_micros:w.held_micros};assert.equal(budgetCredits*1000000-w.balance_micros,report.usage.charged_micros);
 report.pass=report.rounds.at(-1).oracle.ok===true;if(!report.pass)process.exitCode=1;
}catch(e){report.pass=false;report.error=String(e.stack).split(key).join('<REDACTED>');console.error(report.error);process.exitCode=1;}
finally{
 clients.forEach(c=>c.close());for(const c of children.reverse())await stop(c);
 // After every process stopped: nothing under the state directory (task workspaces,
 // native HOMEs, service logs, configs, report) may contain the provider key.
 try{const leaked=await scanForKey(state);report.key_leaks=leaked.length;if(leaked.length){report.pass=false;process.exitCode=1;console.error('PROVIDER KEY FOUND IN '+leaked.length+' state file(s)');}}catch(e){report.pass=false;report.key_scan_error=e.message;process.exitCode=1;}
 await save();console.log('REPORT '+join(state,'report.json'));
}
