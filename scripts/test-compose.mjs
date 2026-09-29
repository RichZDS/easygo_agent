#!/usr/bin/env node
// One-click Compose proof with the fixture model. Starts compose.yaml plus
// compose.fixture.yaml as a throwaway project with a throwaway data directory,
// checks what init generated, runs scripts/test-platform-compose.mjs, changes
// one .env value and runs `up` again, then removes everything it created.
// No provider keys and no paid calls. Talks to the daemon at
// EASYGO_DOCKER_SOCKET (default /var/run/docker.sock); prefer a dedicated one.
// Images are built only when missing; EASYGO_COMPOSE_BUILD=1 rebuilds them.
import assert from 'node:assert/strict';
import {execFile} from 'node:child_process';
import {mkdir,mkdtemp,realpath,rm,rmdir,writeFile} from 'node:fs/promises';
import {createServer} from 'node:net';
import {tmpdir} from 'node:os';
import {join,resolve} from 'node:path';
import {fileURLToPath} from 'node:url';
import {randomBytes,randomInt} from 'node:crypto';
import {promisify} from 'node:util';

const exec=promisify(execFile),root=resolve(fileURLToPath(new URL('..',import.meta.url)));
const socket=process.env.EASYGO_DOCKER_SOCKET??'/var/run/docker.sock',docker=process.env.EASYGO_DOCKER_BINARY??'docker';
const work=await realpath(await mkdtemp(join(tmpdir(),'easygo-compose-'))),data=join(work,'data'),emptyEnv=join(work,'empty.env');
const evidence=process.env.EASYGO_PLATFORM_EVIDENCE??join(tmpdir(),'easygo-compose-proof');
// The daemon creates the data directory, as it would on a first real start.
await mkdir(evidence,{recursive:true});await writeFile(emptyEnv,'');
const port=await new Promise((ok,fail)=>{const s=createServer().once('error',fail).listen(0,'127.0.0.1',()=>{const p=s.address().port;s.close(()=>ok(p));});});
const project='easygo-it-'+randomBytes(3).toString('hex'),password=randomBytes(18).toString('base64url'),origin=`http://127.0.0.1:${port}`,subnet=randomInt(200,250);
// Only these variables reach Compose: a developer's .env or shell keys never leak into the test stack.
const env={PATH:process.env.PATH,HOME:process.env.HOME,...(process.env.DOCKER_CONFIG?{DOCKER_CONFIG:process.env.DOCKER_CONFIG}:{}),DOCKER_HOST:'unix://'+socket,
 EASYGO_DOCKER_SOCKET:socket,EASYGO_DATA_DIR:data,EASYGO_PORT:String(port),EASYGO_PUBLIC_ORIGIN:origin,EASYGO_ADMIN_EMAIL:'admin@example.test',EASYGO_ADMIN_PASSWORD:password,
 EASYGO_REGISTRATION:'true',EASYGO_RUNTIME_IMAGE:'easygo-task-fixture:compose',DEEPSEEK_API_KEY:'fixture-key',
 EASYGO_PLATFORM_SUBNET:`172.31.${subnet}.0/24`,EASYGO_PLATFORM_GATEWAY:`172.31.${subnet}.1`};
const compose=(args,extra={})=>exec(docker,['compose','-p',project,'--project-directory',root,'-f',join(root,'compose.yaml'),'-f',join(root,'compose.fixture.yaml'),'--env-file',emptyEnv,...args],{env:{...env,...extra},maxBuffer:64<<20});
const sleep=ms=>new Promise(r=>setTimeout(r,ms));
async function web(){for(let n=0;n<120;n++){try{if((await fetch(origin+'/')).status===200)return;}catch{}await sleep(1000);}throw Error('Web did not answer within 120 s');}
// Reads the generated state from inside the init image: the data directory is root-only on the host.
const probe=`const fs=require('fs'),s='/state',read=f=>fs.readFileSync(s+'/'+f,'utf8'),at=f=>{const x=fs.statSync(s+'/'+f);return x.uid+':'+(x.mode&0o777).toString(8);};
const text=['gateway.json','loop.json','workshop.json'].map(read).join(''),w=JSON.parse(read('workshop.json')).workshop.sandbox,l=JSON.parse(read('loop.json')).platform,g=JSON.parse(read('gateway.json'));
console.log(JSON.stringify({host_root:w.host_root,image:w.image,limits:[w.memory_bytes,w.nano_cpus,w.pids_limit,g.meter.max_output_tokens],registration:l.registration,origin:l.public_origin,
 leaked:[process.env.EASYGO_ADMIN_PASSWORD,'fixture-key'].filter(v=>text.includes(v)).length,ca:read('pki/public/ca.crt'),
 owners:Object.fromEntries(['.','pki/.ca/ca.key','pki/workshop','pki/workshop/tls.key','pki/public/ca.crt','gateway','agent','workshop','gateway.json','loop.json','workshop.json'].map(f=>[f,at(f)]))}));`;
const inspect=async()=>JSON.parse((await compose(['run','--rm','--no-deps','-T','--entrypoint','node','init','-e',probe])).stdout);
const report={project,origin,checks:[]},pass=name=>{report.checks.push(name);console.log('PASS '+name);};
try{
 await compose(['up','-d',...(process.env.EASYGO_COMPOSE_BUILD==='1'?['--build']:[])]);await web();
 const first=await inspect();
 assert.equal(first.host_root,join(data,'workshop'));assert.equal(first.image,'easygo-task-fixture:compose');assert.deepEqual(first.limits,[2048*1048576,1e9,256,32768]);
 assert.equal(first.registration,true);assert.equal(first.origin,origin);assert.equal(first.leaked,0);
 assert.deepEqual(first.owners,{'.':'0:700','pki/.ca/ca.key':'0:600','pki/workshop':'1000:700','pki/workshop/tls.key':'1000:600','pki/public/ca.crt':'1000:600',gateway:'1000:700',agent:'1000:700',workshop:'1000:700','gateway.json':'1000:600','loop.json':'1000:600','workshop.json':'1000:600'});
 pass('init resolved the daemon-visible data path and wrote configs without secrets');
 await exec(process.execPath,[join(root,'scripts/test-platform-compose.mjs')],{env:{PATH:process.env.PATH,EASYGO_PLATFORM_ORIGIN:origin,EASYGO_ADMIN_PASSWORD:password,EASYGO_REQUIRE_ARTIFACT:'1',EASYGO_PLATFORM_EVIDENCE:evidence}}).then(r=>process.stdout.write(r.stdout));
 pass('Web, billing and an isolated task container through the one-click stack');
 await compose(['up','-d'],{EASYGO_REGISTRATION:'false'});await web();
 const second=await inspect();assert.equal(second.registration,false);assert.equal(second.ca,first.ca);
 const post=(path,body)=>fetch(origin+path,{method:'POST',headers:{Origin:origin,'Content-Type':'application/json'},body:JSON.stringify(body)});
 const late=await post('/api/register',{email:'late@example.test',password:'late-test-password'});assert.equal(late.status,403);assert.equal((await late.json()).error.code,'registration_disabled');
 assert.equal((await post('/api/login',{email:'admin@example.test',password})).status,200);
 pass('a second up applies the changed .env value and keeps the PKI and accounts');
 report.pass=true;
}catch(e){report.pass=false;report.error=e.stack;console.error(e.stack);process.exitCode=1;
 await compose(['logs','--no-color','--timestamps']).then(r=>writeFile(join(evidence,'compose.log'),r.stdout)).catch(()=>{});}
finally{
 await compose(['down','-v','--remove-orphans','-t','30']).catch(e=>{console.error(e.message);process.exitCode=1;});
 const left=(await exec(docker,['ps','-aq','--filter','label=ai.easygo.workshop.root='+join(data,'workshop')],{env})).stdout.trim();
 if(left){console.error('task containers left behind: '+left);process.exitCode=1;}
 // Root-owned state can only be removed from a container.
 await exec(docker,['run','--rm','--network','none','-v',data+':/state','--entrypoint','find','easygo-init:platform','/state','-mindepth','1','-delete'],{env}).catch(e=>console.error(e.message));
 await rmdir(data).catch(e=>{console.error(e.message);process.exitCode=1;});await rm(work,{recursive:true,force:true});
 await writeFile(join(evidence,'compose-report.json'),JSON.stringify(report,null,2));
}
