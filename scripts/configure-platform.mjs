#!/usr/bin/env node
// Writes operator configuration and dev PKI; never reads or writes API passwords.
import {mkdir,readFile,writeFile,realpath,stat} from 'node:fs/promises';
import {execFile} from 'node:child_process';
import {promisify} from 'node:util';
import {join,resolve,isAbsolute,normalize} from 'node:path';
import {fileURLToPath} from 'node:url';
import {trustedProxyAddresses} from '../services/agent-loop/src/platform/client-address.mjs';
const root=resolve(fileURLToPath(new URL('..',import.meta.url))),args={},proxyArgs=[];
const usage='Usage: --state ABSOLUTE_DIR [--origin HTTP_ORIGIN] [--admin-email EMAIL] [--registration true|false] [--runtime-image IMAGE] [--host-root DAEMON_VISIBLE_DIR] [--task-memory-mib N] [--task-cpus N] [--task-pids N] [--max-output-tokens N] [--fixture-url TEST_ONLY_URL] [--trusted-proxy IP]... [--force]';
const valued=['--state','--origin','--admin-email','--registration','--runtime-image','--host-root','--task-memory-mib','--task-cpus','--task-pids','--max-output-tokens','--fixture-url','--trusted-proxy'];
for(let i=2;i<process.argv.length;i++){const flag=process.argv[i];if(flag==='--force'){args.force=true;continue;}if(!valued.includes(flag)||!process.argv[i+1])throw Error(usage);const value=process.argv[++i];if(flag==='--trusted-proxy')proxyArgs.push(value);else args[flag.slice(2)]=value;}
if(!args.state)throw Error('--state required');
// --force rewrites the three config files (never the PKI) so a Compose init can apply .env changes on every start.
const registration=args.registration??'true';if(!['true','false'].includes(registration))throw Error('--registration must be true or false');
const hostRoot=args['host-root'];if(hostRoot!==undefined&&(!isAbsolute(hostRoot)||normalize(hostRoot)!==hostRoot||hostRoot.length>1&&hostRoot.endsWith('/')))throw Error('--host-root must be a clean absolute path');
const bounded=(name,fallback,min,max)=>{const v=Number(args[name]??fallback);if(!Number.isFinite(v)||v<min||v>max)throw Error(`--${name} must be between ${min} and ${max}`);return v;};
// Memory, PID and /tmp sizes are what the paid four-CLI sample ran with (doc/p1-verification.md section 4).
// One CPU keeps single-core hosts working; Docker rejects a CPU limit above the host's count.
const taskMemoryMiB=bounded('task-memory-mib',2048,64,65536),taskCPUs=bounded('task-cpus',1,0.1,32),taskPIDs=bounded('task-pids',256,16,4096);
// 32768: a 4096 cap truncated every file-writing CLI response in the paid DeepSeek run.
const maxOutputTokens=bounded('max-output-tokens',32768,1,131072);
if(![taskMemoryMiB,taskPIDs,maxOutputTokens].every(Number.isInteger))throw Error('--task-memory-mib, --task-pids and --max-output-tokens must be integers');
const trustedProxies=[...trustedProxyAddresses(proxyArgs)];
const requested=resolve(args.state);await mkdir(requested,{recursive:true,mode:0o700});const state=await realpath(requested);if(state!==requested)throw Error('State path must not contain symlinks');
for(const name of ['gateway','agent','workshop'])await mkdir(join(state,name),{recursive:true,mode:0o700});
try{await stat(join(state,'pki'));}catch{await promisify(execFile)('bash',[join(root,'scripts/dev-pki.sh'),join(state,'pki')]);}
const tls={cert_file:'/run/easygo/identity/tls.crt',key_file:'/run/easygo/identity/tls.key',ca_file:'/run/easygo/trust/ca.crt'};
const grant=(id,methods,namespaces=['*'])=>({id,cert_file:`/run/easygo/trust/${id}.crt`,methods,namespaces});
const endpoint=(name,port)=>({url:`https://${name}:${port}/rpc`,peer_certificate_file:`/run/easygo/trust/${name}.crt`});
const gateway=JSON.parse(await readFile(join(root,'services/ai-gateway/config.deepseek.example.json'),'utf8'));
gateway.authorization=[grant('ai-gateway',['health']),grant('agent-loop',['gateway.generate','gateway.models']),grant('workshop',['gateway.native']),grant('client',['health'])];
gateway.meter={...endpoint('agent-loop',8442),database:'/data/meter.db',max_output_tokens:maxOutputTokens};
if(args['fixture-url']){
 const u=new URL(args['fixture-url']);if(!['http:','https:'].includes(u.protocol)||u.username||u.password||u.search||u.hash)throw Error('invalid fixture base URL');
 gateway.models={chat:{protocol:'chat_completions',endpoint:u.origin+'/chat',model:'fixture-model',api_key_env:'DEEPSEEK_API_KEY'},responses:{protocol:'responses',endpoint:u.origin+'/responses',model:'fixture-model',api_key_env:'DEEPSEEK_API_KEY'}};
}
const loop={listen:':8442',tls,authorization:[grant('agent-loop',['health']),grant('client',['health']),grant('ai-gateway',['platform.wallet.reserve','platform.wallet.settle'])],database:'/data/agent.sqlite',gateway:endpoint('ai-gateway',8441),workshop:endpoint('workshop',8443),model:'chat',streaming:true,max_steps:20,context_bytes:128000,concurrency:8,pack_dir:'/opt/easygo/packs/base',platform:{...(trustedProxies.length?{trusted_proxies:trustedProxies}:{}),listen:'0.0.0.0:8080',database:'/data/platform.sqlite',public_origin:args.origin??'http://127.0.0.1:8090',secure_cookies:(args.origin??'http://127.0.0.1:8090').startsWith('https:'),registration:registration==='true',bootstrap_admin:{email:args['admin-email']??'admin@example.test',password_env:'EASYGO_ADMIN_PASSWORD'}},knowledge:{database:'/data/knowledge.sqlite',profile_limit:5}};
const workshop=JSON.parse(await readFile(join(root,'services/workshop/config.runtimes.example.json'),'utf8'));
workshop.authorization=[grant('workshop',['health']),grant('client',['health']),grant('agent-loop',['workshop.workflows','workshop.submit','workshop.get','workshop.list','workshop.cancel','workshop.resume','workshop.events','workshop.result','workshop.artifact','workshop.message','workshop.evidence'])];
workshop.workshop.pack_dir='/opt/easygo/packs/base';
workshop.workshop.model_gateway={...endpoint('ai-gateway',8441),tls};
workshop.workshop.sandbox={mode:'docker',docker_binary:'/usr/local/bin/docker',endpoint:'unix:///run/docker/docker.sock',image:args['runtime-image']??'easygo-task-runtime:platform',owner:'easygo-platform',host_root:hostRoot??join(state,'workshop'),memory_bytes:taskMemoryMiB*1048576,nano_cpus:Math.round(taskCPUs*1e9),pids_limit:taskPIDs,tmpfs_bytes:268435456};
if(args['fixture-url']){
 workshop.workshop.engines={codex:{binary:'codex'}};
 workshop.workshop.runtime_profiles={fixture:{engine:'codex',protocol:'responses',gateway_model:'responses'}};
 workshop.workshop.workflows=[{name:'proof',version:'1',instructions:'Execute offline proof.',runtime:'fixture',policy:'workspace-write',timeout_seconds:180,artifacts:['artifact.txt','isolation-proof.json']}];
}
for(const [name,value]of Object.entries({gateway,loop,workshop}))await writeFile(join(state,name+'.json'),JSON.stringify(value,null,2)+'\n',{mode:0o600,flag:args.force?'w':'wx'});
console.log(JSON.stringify({state,origin:loop.platform.public_origin,admin_email:loop.platform.bootstrap_admin.email,registration:loop.platform.registration,credential_environment:['EASYGO_ADMIN_PASSWORD','DEEPSEEK_API_KEY'],runtime_image:workshop.workshop.sandbox.image,host_root:workshop.workshop.sandbox.host_root}));
