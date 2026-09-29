#!/usr/bin/env node
// Writes operator configuration and dev PKI; never reads or writes API passwords.
import {mkdir,readFile,writeFile,realpath,stat} from 'node:fs/promises';
import {execFile} from 'node:child_process';
import {promisify} from 'node:util';
import {join,resolve} from 'node:path';
import {fileURLToPath} from 'node:url';
import {trustedProxyAddresses} from '../services/agent-loop/src/platform/client-address.mjs';
const root=resolve(fileURLToPath(new URL('..',import.meta.url))),args={},proxyArgs=[];
for(let i=2;i<process.argv.length;i+=2){if(!['--state','--origin','--admin-email','--runtime-image','--fixture-url','--trusted-proxy'].includes(process.argv[i])||!process.argv[i+1])throw Error('Usage: --state ABSOLUTE_DIR [--origin HTTP_ORIGIN] [--admin-email EMAIL] [--runtime-image IMAGE] [--fixture-url TEST_ONLY_URL] [--trusted-proxy IP]...');if(process.argv[i]==='--trusted-proxy')proxyArgs.push(process.argv[i+1]);else args[process.argv[i].slice(2)]=process.argv[i+1];}
if(!args.state)throw Error('--state required');
const trustedProxies=[...trustedProxyAddresses(proxyArgs)];
const requested=resolve(args.state);await mkdir(requested,{recursive:true,mode:0o700});const state=await realpath(requested);if(state!==requested)throw Error('State path must not contain symlinks');
for(const name of ['gateway','agent','workshop'])await mkdir(join(state,name),{recursive:true,mode:0o700});
try{await stat(join(state,'pki'));}catch{await promisify(execFile)('bash',[join(root,'scripts/dev-pki.sh'),join(state,'pki')]);}
const tls={cert_file:'/run/easygo/identity/tls.crt',key_file:'/run/easygo/identity/tls.key',ca_file:'/run/easygo/trust/ca.crt'};
const grant=(id,methods,namespaces=['*'])=>({id,cert_file:`/run/easygo/trust/${id}.crt`,methods,namespaces});
const endpoint=(name,port)=>({url:`https://${name}:${port}/rpc`,peer_certificate_file:`/run/easygo/trust/${name}.crt`});
const gateway=JSON.parse(await readFile(join(root,'services/ai-gateway/config.deepseek.example.json'),'utf8'));
gateway.authorization=[grant('ai-gateway',['health']),grant('agent-loop',['gateway.generate','gateway.models']),grant('workshop',['gateway.native']),grant('client',['health'])];
gateway.meter={...endpoint('agent-loop',8442),database:'/data/meter.db',max_output_tokens:32768};
if(args['fixture-url']){
 const u=new URL(args['fixture-url']);if(!['http:','https:'].includes(u.protocol)||u.username||u.password||u.search||u.hash)throw Error('invalid fixture base URL');
 gateway.models={chat:{protocol:'chat_completions',endpoint:u.origin+'/chat',model:'fixture-model',api_key_env:'DEEPSEEK_API_KEY'},responses:{protocol:'responses',endpoint:u.origin+'/responses',model:'fixture-model',api_key_env:'DEEPSEEK_API_KEY'}};
}
const loop={listen:':8442',tls,authorization:[grant('agent-loop',['health']),grant('client',['health']),grant('ai-gateway',['platform.wallet.reserve','platform.wallet.settle'])],database:'/data/agent.sqlite',gateway:endpoint('ai-gateway',8441),workshop:endpoint('workshop',8443),model:'chat',streaming:true,max_steps:20,context_bytes:128000,concurrency:8,pack_dir:'/opt/easygo/packs/base',platform:{...(trustedProxies.length?{trusted_proxies:trustedProxies}:{}),listen:'0.0.0.0:8080',database:'/data/platform.sqlite',public_origin:args.origin??'http://127.0.0.1:8090',secure_cookies:(args.origin??'http://127.0.0.1:8090').startsWith('https:'),registration:true,bootstrap_admin:{email:args['admin-email']??'admin@example.test',password_env:'EASYGO_ADMIN_PASSWORD'}},knowledge:{database:'/data/knowledge.sqlite',profile_limit:5}};
const workshop=JSON.parse(await readFile(join(root,'services/workshop/config.runtimes.example.json'),'utf8'));
workshop.authorization=[grant('workshop',['health']),grant('client',['health']),grant('agent-loop',['workshop.workflows','workshop.submit','workshop.get','workshop.list','workshop.cancel','workshop.resume','workshop.events','workshop.result','workshop.artifact'])];
workshop.workshop.pack_dir='/opt/easygo/packs/base';
workshop.workshop.model_gateway={...endpoint('ai-gateway',8441),tls};
workshop.workshop.sandbox={mode:'docker',docker_binary:'/usr/local/bin/docker',endpoint:'unix:///run/docker/docker.sock',image:args['runtime-image']??'easygo-task-runtime:platform',owner:'easygo-platform',host_root:join(state,'workshop'),memory_bytes:1073741824,nano_cpus:1000000000,pids_limit:128,tmpfs_bytes:67108864};
if(args['fixture-url']){
 workshop.workshop.engines={codex:{binary:'codex'}};
 workshop.workshop.runtime_profiles={fixture:{engine:'codex',protocol:'responses',gateway_model:'responses'}};
 workshop.workshop.workflows=[{name:'proof',version:'1',instructions:'Execute offline proof.',runtime:'fixture',policy:'workspace-write',timeout_seconds:180,artifacts:['artifact.txt','isolation-proof.json']}];
}
for(const [name,value]of Object.entries({gateway,loop,workshop}))await writeFile(join(state,name+'.json'),JSON.stringify(value,null,2)+'\n',{mode:0o600,flag:'wx'});
console.log(JSON.stringify({state,origin:loop.platform.public_origin,admin_email:loop.platform.bootstrap_admin.email,registration:true,credential_environment:['EASYGO_ADMIN_PASSWORD','DEEPSEEK_API_KEY'],runtime_image:workshop.workshop.sandbox.image}));
