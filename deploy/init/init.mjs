#!/usr/bin/env node
// One-shot Compose init. Runs as root before the services start, then exits:
// checks .env, keeps a long-lived internal PKI, rewrites the three service
// configs from .env and hands the data directories to UID 1000.
// It never prints, stores or forwards the admin password or provider keys.
import {chmodSync,chownSync,existsSync,readdirSync,readFileSync,renameSync,rmdirSync,statSync} from 'node:fs';
import {execFileSync} from 'node:child_process';
import {hostname} from 'node:os';
import {isAbsolute,join,normalize} from 'node:path';
import http from 'node:http';

const state='/state',socket='/run/docker/docker.sock',env=process.env;
const identities=['ai-gateway','agent-loop','workshop','client'],configs=['gateway.json','loop.json','workshop.json'];
const fail=message=>{console.error(`easygo-init: ${message}`);process.exit(1);};

if(!existsSync(join(state,'agent','platform.sqlite'))&&(env.EASYGO_ADMIN_PASSWORD??'').length<12)
 fail('the first start creates the admin account: set EASYGO_ADMIN_PASSWORD in .env (at least 12 characters)');
const registration=(env.EASYGO_REGISTRATION??'false').toLowerCase();
if(!['true','false'].includes(registration))fail('EASYGO_REGISTRATION must be true or false');

// Docker creates a directory when a file bind mount starts before its source exists.
for(const name of configs){const path=join(state,name);if(existsSync(path)&&statSync(path).isDirectory()){if(readdirSync(path).length)fail(`${name} is a non-empty directory; remove it and start again`);rmdirSync(path);}}

// Services only read certificates at startup, so rotation takes effect on the next recreate.
const pki=join(state,'pki');
const fresh=file=>{try{execFileSync('openssl',['x509','-checkend','2592000','-noout','-in',file],{stdio:'ignore'});return true;}catch{return false;}};
if(existsSync(pki)&&![join(pki,'public','ca.crt'),...identities.map(id=>join(pki,id,'tls.crt'))].every(fresh)){
 const aside=`pki.replaced-${new Date().toISOString().replace(/[:.]/g,'-')}`;
 renameSync(pki,join(state,aside));console.log(`easygo-init: internal certificates were missing or expire within 30 days; generating new ones (old copy: ${aside})`);
}

// Task workspaces are bind-mounted by the Docker daemon, so the workshop needs
// the daemon-visible path of this directory. Ask the daemon about our own mount.
async function daemonVisibleState(){
 if(env.EASYGO_HOST_DATA_DIR)return env.EASYGO_HOST_DATA_DIR;
 const id=/\/containers\/([0-9a-f]{64})\//.exec(readFileSync('/proc/self/mountinfo','utf8'))?.[1]??hostname();
 const raw=await new Promise((resolve,reject)=>{
  const req=http.get({socketPath:socket,path:`/containers/${encodeURIComponent(id)}/json`},res=>{let body='';res.setEncoding('utf8');res.on('data',chunk=>body+=chunk);res.on('end',()=>res.statusCode===200?resolve(body):reject(Error(`Docker API answered ${res.statusCode}`)));});
  req.on('error',reject);req.setTimeout(10000,()=>req.destroy(Error('Docker API timed out')));
 });
 const source=JSON.parse(raw).Mounts?.find(m=>m.Destination===state)?.Source;
 if(!source)throw Error('the data directory mount is missing from docker inspect');
 return source;
}
let hostState;
try{hostState=await daemonVisibleState();}catch(e){fail(`cannot resolve the data directory through ${socket}: ${e.message}. Check EASYGO_DOCKER_SOCKET, or set EASYGO_HOST_DATA_DIR to the absolute host path of EASYGO_DATA_DIR`);}
if(!isAbsolute(hostState)||normalize(hostState)!==hostState)fail('the daemon-visible data directory must be a clean absolute path');

const args=['--state',state,'--force','--host-root',join(hostState,'workshop'),
 '--origin',env.EASYGO_PUBLIC_ORIGIN??'http://localhost:8090','--admin-email',env.EASYGO_ADMIN_EMAIL??'admin@example.com',
 '--registration',registration,'--runtime-image',env.EASYGO_RUNTIME_IMAGE??'easygo-task-runtime:platform',
 '--task-memory-mib',env.EASYGO_TASK_MEMORY_MIB??'2048','--task-cpus',env.EASYGO_TASK_CPUS??'1','--task-pids',env.EASYGO_TASK_PIDS??'256','--max-output-tokens',env.EASYGO_MAX_OUTPUT_TOKENS??'32768'];
for(const ip of (env.EASYGO_TRUSTED_PROXIES??'').split(/[\s,]+/).filter(Boolean))args.push('--trusted-proxy',ip);
if(env.EASYGO_FIXTURE_URL)args.push('--fixture-url',env.EASYGO_FIXTURE_URL);
try{execFileSync(process.execPath,['/opt/easygo/scripts/configure-platform.mjs',...args],{stdio:'inherit',env:{PATH:env.PATH,EASYGO_PKI_DAYS:env.EASYGO_PKI_DAYS||'3650'}});}
catch{fail('configuration was rejected; see the message above');}

// Services run as UID 1000. The CA key and the state root stay root-only.
chmodSync(state,0o700);
for(const name of ['gateway','agent','workshop',...configs])chownSync(join(state,name),1000,1000);
for(const dir of [...identities,'public']){chownSync(join(pki,dir),1000,1000);for(const file of readdirSync(join(pki,dir)))chownSync(join(pki,dir,file),1000,1000);}
console.log('easygo-init: ready');
