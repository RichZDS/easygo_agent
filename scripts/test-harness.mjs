#!/usr/bin/env node
// Deterministic P1 harness proof: fixture model -> loop -> workshop -> Docker CLI
// -> crew RPC -> durable events -> loop tools. No provider credentials are read.
import assert from 'node:assert/strict';
import { spawn, execFile } from 'node:child_process';
import { promisify } from 'node:util';
import { once } from 'node:events';
import http from 'node:http';
import net from 'node:net';
import { mkdtemp, mkdir, readFile, writeFile, copyFile, rm, readdir, lstat, readlink, chmod } from 'node:fs/promises';
import { openSync, closeSync } from 'node:fs';
import { join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { createHash, randomBytes, randomUUID } from 'node:crypto';
import { createRPCClient } from './rpc-call.mjs';

const root = resolve(fileURLToPath(new URL('..', import.meta.url)));
const exec = promisify(execFile);
const args = process.argv.slice(2);
if (![2,4].includes(args.length) || args[0] !== '--evidence' || (args.length === 4 && args[2] !== '--scenarios')) throw Error('Usage: node scripts/test-harness.mjs --evidence NEW_DIRECTORY [--scenarios S1,S2,...,S9]');
const evidenceDir = resolve(args[1]);
const scenarioIDs = ['S1','S2','S3','S4','S5','S6','S7','S8','S9'];
const selected = args[3]?.split(',') ?? scenarioIDs;
assert.ok(selected.length > 0 && new Set(selected).size === selected.length && selected.every(id => scenarioIDs.includes(id)), 'Unknown or duplicate scenario');
const go = process.env.EASYGO_GO_BIN ?? 'go';
const docker = process.env.EASYGO_DOCKER_TEST_BINARY;
const endpoint = process.env.EASYGO_DOCKER_TEST_ENDPOINT;
const image = process.env.EASYGO_DOCKER_TEST_IMAGE;
if (!docker || !image || !endpoint?.startsWith('unix://') || endpoint === 'unix:///var/run/docker.sock' || endpoint === 'unix:///run/docker.sock') {
  throw Error('Explicit dedicated EASYGO_DOCKER_TEST_BINARY, EASYGO_DOCKER_TEST_ENDPOINT and EASYGO_DOCKER_TEST_IMAGE required');
}
await mkdir(evidenceDir, { recursive: true });
await writeFile(join(evidenceDir, 'report.json'), '{}\n', { flag: 'wx' });
const state = await mkdtemp('/tmp/egh-');
const owner = `harness-${randomUUID()}`;
const namespace = 'harness';
const sandbox = { mode:'docker', docker_binary:docker, endpoint, image, owner, host_root:join(state,'workshop-data'), memory_bytes:64*1024*1024, nano_cpus:100_000_000, pids_limit:128, tmpfs_bytes:1024*1024, disk_quota_bytes:16*1024*1024, disk_quota_files:1000, disk_poll_ms:200 };
const report = { scope:'P1 harness', scenario_ids:selected, deferred:scenarioIDs.filter(id=>!selected.includes(id)), paid_models:false, state, owner, sandbox, scenarios:[], commands:[], model_requests:0, pass:false };
const children = [], clients = [];
const stopSignal = new AbortController();
let provider, agent, broker, current;
for (const signal of ['SIGINT','SIGTERM']) process.once(signal, () => stopSignal.abort(Error(`Interrupted by ${signal}`)));
const sleep = ms => new Promise(resolve => setTimeout(resolve, ms));
const textOf = content => typeof content === 'string' ? content : (content ?? []).map(block => block.text ?? '').join('');
const taskTerminal = task => !['queued','running','cancelling'].includes(task.status);
const crew = (...args) => ({ op:'crew', args });
const input = script => JSON.stringify({ mode:'crew', script });

async function command(name, bin, argv, cwd=root, extraEnv={}) {
  try {
    const result = await exec(bin, argv, { cwd, env:{...process.env,GOMAXPROCS:'2',...extraEnv}, maxBuffer:4*1024*1024 });
    await writeFile(join(evidenceDir, `${name}.log`), result.stdout + result.stderr);
    report.commands.push({ name, exit_code:0 });
    return result.stdout;
  } catch (error) {
    await writeFile(join(evidenceDir, `${name}.log`), (error.stdout ?? '') + (error.stderr ?? ''));
    report.commands.push({ name, exit_code:error.code ?? null });
    throw Error(`${name} failed (exit ${error.code ?? 'unknown'})`);
  }
}
async function port() {
  const server = net.createServer(); server.listen(0,'127.0.0.1'); await once(server,'listening');
  const port = server.address().port; await new Promise(resolve => server.close(resolve)); return port;
}
async function until(fn, label, timeout=60000) {
  const deadline = Date.now() + timeout;
  while (Date.now() < deadline) {
    stopSignal.signal.throwIfAborted();
    const result = await fn(); if (result) return result;
    for (const entry of children) if (entry.child.exitCode !== null || entry.child.signalCode !== null) throw Error(`${entry.name} exited before ${label}`);
    await sleep(200);
  }
  throw Error(`Timed out: ${label}`);
}
function start(name, bin, argv, env={}) {
  const fd = openSync(join(state, `${name}.log`),'a',0o600);
  const child = spawn(bin,argv,{cwd:root,env:{PATH:process.env.PATH,LANG:'C.UTF-8',GOMAXPROCS:'2',...env},stdio:['ignore',fd,fd]});
  closeSync(fd);
  const entry = {name,child,done:new Promise(resolve=>{child.once('error',()=>resolve({error:'spawn_failed'}));child.once('exit',(code,signal)=>resolve({code,signal}));})};
  children.push(entry);
}
async function stop(entry) {
  if (entry.child.exitCode === null && entry.child.signalCode === null) entry.child.kill('SIGTERM');
  let timer;
  const result = await Promise.race([entry.done,new Promise(resolve=>{timer=setTimeout(()=>{entry.child.kill('SIGKILL');resolve({error:'shutdown_timeout'});},12000);})]);
  clearTimeout(timer);
  return result;
}
async function rpc(client, method, params={}) {
  const value = await client.call(method,{namespace,...params});
  if (current) current.rpc.push({method,params,value});
  return value;
}

// Every side effect is requested by a real model response through the gateway.
// The oracle reads durable tool results over agent.session.history RPC; the
// fixture's final assistant text has no authority over the scenario verdict.
async function tools(calls) {
  const session = await rpc(agent,'agent.session.create');
  const run = await rpc(agent,'agent.run.start',{session_id:session.id,input:JSON.stringify({harness_plan:calls}),idempotency_key:randomUUID()});
  const completed = await until(async()=>{const value=await rpc(agent,'agent.run.get',{run_id:run.id});return !['queued','running'].includes(value.status)&&value;},'loop run completion');
  assert.equal(completed.status,'completed',JSON.stringify(completed.error));
  const history = await rpc(agent,'agent.session.history',{session_id:session.id,limit:100});
  const results = history.messages.filter(m=>m.role==='tool').flatMap(m=>m.content);
  assert.equal(results.length,calls.length,'all planned tool calls must have durable results');
  for (let i=0;i<results.length;i++) {
    assert.equal(results[i].name,calls[i].name);
    assert.ok(!results[i].is_error,`tool error: ${results[i].text}`);
  }
  return results.map(result=>JSON.parse(result.text));
}
const tool = (name,args) => ({name,arguments:args});
async function submit(script, workflow='channel') {
  const [task] = await tools([tool('workshop_submit',{workflow,input:input(script)})]);
  return task;
}
async function terminal(id) {
  const task = await until(async()=>{const task=await rpc(broker,'workshop.get',{task_id:id});return taskTerminal(task)&&task;},'workshop terminal');
  if (task.status !== 'succeeded') await rpc(broker,'workshop.events',{task_id:id,after:0});
  return task;
}
async function messages(id, after=0) {
  const [page] = await tools([tool('workshop_messages',{task_id:id,after})]);
  assert.equal(page.namespace,namespace); assert.equal(page.task_id,id);
  assert.ok(Buffer.byteLength(JSON.stringify(page))<=32768);
  return page;
}
async function allMessages(id) {
  let after=0;const events=[];
  for (let i=0;i<100;i++) {
    const page=await messages(id,after);
    assert.ok(page.next>=after);
    assert.ok(page.events.every(event=>event.sequence>after));
    events.push(...page.events);
    if(!page.truncated) return {events,next:page.next};
    assert.ok(page.next>after,'truncated pages must advance');after=page.next;
  }
  throw Error('message page bound exceeded');
}
async function matchEvents(id,page) {
  const events=await rpc(broker,'workshop.events',{task_id:id,after:0});
  const selected=events.filter(e=>['crew.message','crew.read','state','acceptance'].includes(e.kind));
  assert.deepEqual(page.events.map(e=>e.sequence),selected.map(e=>e.sequence));
  assert.deepEqual(page.events.filter(e=>e.kind==='crew.message').map(e=>e.message),selected.filter(e=>e.kind==='crew.message').map(e=>e.message));
  assert.equal(new Set(page.events.map(e=>e.sequence)).size,page.events.length);
  return events;
}

async function acceptanceEvidence(task) {
  const runID=task.runs.at(-1).id;
  const [list]=await tools([tool('workshop_evidence',{task_id:task.id,run_id:runID})]);
  assert.deepEqual(list,await rpc(broker,'workshop.evidence',{task_id:task.id,run_id:runID}));
  assert.equal(list.run_id,runID);assert.equal(list.acceptance_state,task.runs.at(-1).acceptance_state);
  const outputs={};
  for(const item of list.evidence) {
    let offset=0,text='';
    for(let i=0;i<1000;i++) {
      const [page]=await tools([tool('workshop_evidence',{task_id:task.id,run_id:runID,evidence_id:item.id,offset,limit:64})]);
      assert.equal(page.offset,offset);assert.ok(Buffer.byteLength(page.text)<=64);
      text+=page.text;assert.equal(page.next_offset,Buffer.byteLength(text));
      if(page.eof){assert.equal(page.total_bytes,Buffer.byteLength(text));break;}
      assert.ok(page.next_offset>offset);offset=page.next_offset;
      assert.ok(i<999,'evidence pagination exceeded bound');
    }
    const direct=await rpc(broker,'workshop.evidence',{task_id:task.id,run_id:runID,evidence_id:item.id,limit:32768});
    assert.equal(direct.eof,true);assert.equal(text,direct.text);
    outputs[item.check]=text;
  }
  return {list,outputs};
}

// Independent Node implementation of the documented tree digest wire format.
// No workshop implementation is invoked. Persist entries alongside the digest
// so a reviewer can reproduce or challenge individual filesystem measurements.
async function independentWorkspaceHash(directory) {
  const entries=[];
  async function walk(relative='') {
    for(const name of await readdir(join(directory,relative))) {
      if(!relative&&name==='.workshop-home')continue;
      const path=relative?`${relative}/${name}`:name;
      const absolute=join(directory,path),info=await lstat(absolute);
      const entry={Path:path,Type:'',Size:info.size,SHA256:''};
      if(info.isDirectory()){entry.Type='directory';await walk(path);}
      else if(info.isFile()){entry.Type='file';const bytes=await readFile(absolute);assert.equal(bytes.length,info.size);entry.SHA256=createHash('sha256').update(bytes).digest('hex');}
      else if(info.isSymbolicLink()){entry.Type='symlink';entry.SHA256=createHash('sha256').update(await readlink(absolute)).digest('hex');}
      else throw Error(`Unsupported workspace entry: ${path}`);
      entries.push(entry);
    }
  }
  await walk();
  entries.sort((a,b)=>Buffer.compare(Buffer.from(a.Path),Buffer.from(b.Path)));
  const encoded=entries.map(entry=>JSON.stringify(entry).replace(/[<>&\u2028\u2029]/g,c=>`\\u${c.charCodeAt(0).toString(16).padStart(4,'0')}`)+'\n').join('');
  return {entries,sha256:createHash('sha256').update(encoded).digest('hex')};
}

async function ownChecks() {
  const result=await exec(docker,['-H',endpoint,'ps','-aq','--filter',`label=ai.easygo.workshop.owner=${owner}`,'--filter','name=easygo-check-']);
  const ids=result.stdout.trim().split('\n').filter(Boolean);
  if(!ids.length)return [];
  const objects=JSON.parse((await exec(docker,['-H',endpoint,'inspect',...ids])).stdout);
  return objects.map(item=>({id:item.Id,name:item.Name,state:item.State,command:[item.Path,...item.Args],labels:item.Config.Labels}));
}

async function unsealState(directory) {
  // Only our temporary state is removed. Pack snapshots deliberately seal their
  // directories; make these writable after every service/container has stopped.
  await chmod(directory,0o700);
  for(const entry of await readdir(directory,{withFileTypes:true})) {
    if(entry.isDirectory())await unsealState(join(directory,entry.name));
  }
}

async function S1() {
  const task=await submit([crew('report','S1 started'),crew('ask','S1 question'),crew('report','S1 continued'),crew('submit','--tests','pass','S1 ready')]);
  const end=await terminal(task.id);assert.equal(end.status,'succeeded');assert.equal(end.runs.at(-1).outcome,'submitted');
  const page=await allMessages(task.id);await matchEvents(task.id,page);
  const reports=page.events.filter(e=>e.kind==='crew.message');
  assert.deepEqual(reports.map(e=>e.message.kind),['report','ask','report','submit']);
  assert.deepEqual(reports.map(e=>e.message.text),['S1 started','S1 question','S1 continued','S1 ready']);
  assert.equal(new Set(reports.map(e=>e.message.id)).size,4);
  assert.equal(reports[3].message.claims.tests,'pass');
  assert.deepEqual((await messages(task.id)).events,page.events);
  assert.deepEqual((await messages(task.id,page.next)).events,[]);
}
async function S2() {
  const task=await submit([{op:'silent'}]);const end=await terminal(task.id);
  assert.equal(end.status,'succeeded');assert.equal(end.runs.at(-1).outcome,'none');
  const [summary]=await tools([tool('workshop_get',{task_id:task.id})]);assert.equal(summary.runs.at(-1).outcome,'none');
  const page=await allMessages(task.id);await matchEvents(task.id,page);
  assert.equal(page.events.filter(e=>e.kind==='crew.message').length,0);
}
async function S3() {
  const task=await submit([{op:'write',path:'artifact.txt',text:'bad'},crew('submit','--tests','pass','S3 claims green')],'artifact');
  const end=await terminal(task.id);assert.equal(end.status,'succeeded');
  assert.equal(end.runs.at(-1).outcome,'submitted');
  assert.equal(end.runs.at(-1).acceptance_state,'failed');assert.equal(end.runs.at(-1).false_green,true);
  const page=await allMessages(task.id);await matchEvents(task.id,page);
  const claimed=page.events.find(e=>e.message?.kind==='submit');assert.equal(claimed.message.claims.tests,'pass');
  assert.ok(page.events.some(e=>e.kind==='acceptance'&&e.acceptance.state==='failed'&&e.acceptance.false_green===true));
  const {list,outputs}=await acceptanceEvidence(end);
  assert.equal(list.false_green,true);assert.equal(list.evidence.length,1);assert.notEqual(list.evidence[0].exit_code,0);
  assert.match(outputs.artifact,/file content mismatch/);
}
async function S4() {
  const task=await submit([{op:'write',path:'artifact.txt',text:'good'},{op:'write',path:'nested/proof.txt',text:'独立工作区证据\n'},{op:'write',path:'.workshop-home/ignored-proof',text:'excluded'},crew('submit','--tests','pass','S4 ready')],'artifact');
  const end=await terminal(task.id);assert.equal(end.status,'succeeded');assert.equal(end.runs.at(-1).acceptance_state,'passed');
  const page=await allMessages(task.id);await matchEvents(task.id,page);
  assert.ok(page.events.some(e=>e.acceptance?.state==='passed'));
  const {list,outputs}=await acceptanceEvidence(end);assert.equal(list.false_green,false);assert.equal(list.evidence.length,1);
  const workspace=join(sandbox.host_root,'workspaces',task.id);
  assert.equal(await readFile(join(workspace,'.workshop-home/ignored-proof'),'utf8'),'excluded');
  const tree=await independentWorkspaceHash(workspace);current.independent_workspace=tree;
  assert.ok(tree.entries.some(entry=>entry.Type==='directory'));assert.ok(tree.entries.some(entry=>entry.Path==='nested/proof.txt'));
  assert.ok(tree.entries.every(entry=>!entry.Path.startsWith('.workshop-home')));
  assert.equal(list.evidence[0].workspace_sha256,tree.sha256);assert.equal(list.evidence[0].exit_code,0);
  assert.match(outputs.artifact,/file content matches/);
}
async function S5() {
  const versions=await readdir(join(sandbox.host_root,'packs'));assert.equal(versions.length,1);
  const sealed=join(sandbox.host_root,'packs',versions[0],'checks','verify');
  const before=createHash('sha256').update(await readFile(sealed)).digest('hex');
  const task=await submit([
    {op:'write-denied',path:'/pack/checks/verify'},{op:'write-denied',path:sealed},
    {op:'write-denied',path:'/usr/local/bin/fixture-check'},
    {op:'write',path:'pack/checks/verify',text:'worker forged checker'},
    {op:'write',path:'pack/checks/source.txt',text:'worker forged marker'},
    crew('submit','--tests','pass','S5 isolation proof')
  ],'isolation');
  const end=await terminal(task.id);assert.equal(end.status,'succeeded');assert.equal(end.runs.at(-1).acceptance_state,'passed');
  const [result]=await tools([tool('workshop_result',{task_id:task.id,run_id:end.runs.at(-1).id})]);
  for(const path of ['/pack/checks/verify',sealed,'/usr/local/bin/fixture-check'])assert.ok(result.text.includes(`worker write denied: ${path}`));
  const page=await allMessages(task.id);await matchEvents(task.id,page);
  const {list,outputs}=await acceptanceEvidence(end);assert.equal(list.evidence.length,2);assert.ok(list.evidence.every(e=>e.exit_code===0));
  assert.deepEqual(list.evidence.map(e=>e.command[0]),['/pack/checks/verify','/pack/checks/verify']);
  assert.match(outputs.marker,/file content matches/);
  for(const text of ['write denied /workspace/check-write: true','write denied /pack/checks/check-write: true','network denied: true','no credentials or relay: true'])assert.ok(outputs.isolation.includes(text),text);
  const after=createHash('sha256').update(await readFile(sealed)).digest('hex');assert.equal(after,before);
  current.trusted_checker={path:sealed,before_sha256:before,after_sha256:after};
}
async function S6() {
  const task=await submit([crew('ask','S6 choose an answer')]);const asked=await terminal(task.id);
  assert.equal(asked.status,'succeeded');assert.equal(asked.runs.at(-1).outcome,'asked');
  const first=await allMessages(task.id);assert.equal(first.events.filter(e=>e.kind==='crew.message').at(-1).message.kind,'ask');
  const [reply,resumed]=await tools([
    tool('workshop_reply',{task_id:task.id,text:'S6 use the blue answer'}),
    tool('workshop_resume',{task_id:task.id,input:input([{op:'echo_input'},crew('submit','--tests','pass','S6 completed')])})
  ]);
  assert.equal(reply.task_id,task.id);assert.equal(resumed.id,task.id);
  const end=await terminal(task.id);assert.equal(end.status,'succeeded');assert.equal(end.runs.at(-1).outcome,'submitted');
  assert.equal(end.run_ids.length,2);assert.notEqual(end.run_ids[0],end.run_ids[1]);
  const page=await allMessages(task.id);await matchEvents(task.id,page);
  const read=page.events.find(e=>e.kind==='crew.read'&&e.read.message_ids.includes(reply.id));assert.ok(read,'resume must persist the reply as read');
  assert.equal(read.run_id,end.run_ids[1]);
  const [result]=await tools([tool('workshop_result',{task_id:task.id,run_id:end.run_ids[1]})]);
  assert.ok(result.text.includes(`- [${reply.id}] S6 use the blue answer`),'fixture-received resume input must contain the exact unread reply ID and text');
  assert.ok(result.text.includes('Unread messages from the foreman:'));
}
async function S7() {
  const task=await submit([crew('report','S7 waiting'),{op:'inbox',text:'S7 proceed',timeout_ms:60000},crew('submit','--tests','pass','S7 received instruction')]);
  await until(async()=>{const events=await rpc(broker,'workshop.events',{task_id:task.id});return events.some(e=>e.message?.text==='S7 waiting');},'worker inbox ready');
  const running=await rpc(broker,'workshop.get',{task_id:task.id});assert.equal(running.status,'running');
  const [reply]=await tools([tool('workshop_reply',{task_id:task.id,text:'S7 proceed'})]);
  const end=await terminal(task.id);assert.equal(end.status,'succeeded');assert.equal(end.runs.at(-1).outcome,'submitted');
  const page=await allMessages(task.id);const raw=await matchEvents(task.id,page);
  const read=page.events.find(e=>e.kind==='crew.read'&&e.read.message_ids.includes(reply.id));assert.ok(read);
  const submission=page.events.find(e=>e.message?.kind==='submit');assert.ok(submission.sequence>read.sequence);
  const inbox=raw.filter(e=>e.kind==='text').map(e=>{try{return JSON.parse(e.text);}catch{return null;}}).find(value=>value?.messages?.some(m=>m.id===reply.id));
  assert.ok(inbox,'container inbox output must contain the reply ID');
  assert.equal(inbox.messages.find(m=>m.id===reply.id).text,'S7 proceed');
}
async function S8() {
  const task=await submit([crew('report','S8 started'),{op:'duplicate',client_id:'S8-same-id',kind:'report',text:'S8 exactly once'}]);
  const end=await terminal(task.id);assert.equal(end.status,'succeeded');
  const page=await allMessages(task.id);const raw=await matchEvents(task.id,page);
  const duplicates=page.events.filter(e=>e.message?.client_id==='S8-same-id');assert.equal(duplicates.length,1);
  assert.equal(duplicates[0].message.text,'S8 exactly once');
  const receipt=raw.filter(e=>e.kind==='text').map(e=>{try{return JSON.parse(e.text);}catch{return null;}}).find(value=>value?.id===duplicates[0].message.id);
  assert.equal(receipt?.sequence,duplicates[0].sequence);
}
async function S9() {
  const task=await submit([crew('submit','--tests','pass','S9 ready for checks')],'cancel-check');
  await until(async()=>{const current=await rpc(broker,'workshop.get',{task_id:task.id});return current.status==='running'&&current.runs.at(-1).acceptance_state==='running';},'acceptance running');
  const checks=await until(async()=>{const checks=await ownChecks();return checks.length===1&&checks[0].state.Running&&checks;},'check container actually running');
  current.running_check=checks[0];assert.deepEqual(checks[0].command,['/usr/local/bin/fixture-check','sleep','60']);
  await tools([tool('workshop_cancel',{task_id:task.id})]);
  const end=await terminal(task.id);assert.equal(end.status,'cancelled');assert.equal(end.runs.at(-1).acceptance_state,'cancelled');
  const page=await allMessages(task.id);await matchEvents(task.id,page);
  assert.ok(page.events.some(e=>e.kind==='acceptance'&&e.acceptance.state==='cancelled'));
  const {list}=await acceptanceEvidence(end);assert.equal(list.acceptance_state,'cancelled');assert.equal(list.false_green,false);
  await until(async()=>(await ownChecks()).length===0,'cancelled check container cleanup');
  current.remaining_checks=await ownChecks();assert.deepEqual(current.remaining_checks,[]);
}

try {
  report.commit=(await exec('git',['rev-parse','HEAD'],{cwd:root})).stdout.trim();
  report.harness_sha256=createHash('sha256').update(await readFile(fileURLToPath(import.meta.url))).digest('hex');
  report.image_id=(await command('image-inspect',docker,['-H',endpoint,'image','inspect',image,'--format','{{.Id}}'])).trim();
  await command('dev-pki','bash',[join(root,'scripts/dev-pki.sh'),join(state,'pki')]);
  await command('gateway-build',go,['build','-p','1','-o',join(state,'gateway'),'./cmd/server'],join(root,'services/ai-gateway'));
  await command('workshop-build',go,['build','-p','1','-o',join(state,'workshop'),'./cmd/server'],join(root,'services/workshop'));
  await command('loop-build','npm',['run','build'],join(root,'services/agent-loop'));
  const packDir=join(state,'pack');
  await mkdir(join(packDir,'checks'),{recursive:true});await mkdir(join(packDir,'roles'));
  await copyFile(join(root,'packs/base/roles/worker.md'),join(packDir,'roles/worker.md'));
  await writeFile(join(packDir,'checks/source.txt'),'trusted-original');
  await command('check-build',go,['build','-p','1','-o',join(packDir,'checks/verify'),'./cmd/fixture-check'],join(root,'services/workshop'),{CGO_ENABLED:'0'});
  const fixtureKey=randomBytes(24).toString('hex');
  provider=http.createServer(async(req,res)=>{
    try {
      assert.ok(req.headers.authorization===`Bearer ${fixtureKey}`,'fixture authentication failed');
      assert.equal(req.url,'/chat');
      let body='';for await(const part of req){body+=part;assert.ok(Buffer.byteLength(body)<=2**20);}
      const request=JSON.parse(body);assert.equal(request.model,'fixture-model');report.model_requests++;
      const messages=request.messages??[];
      const user=messages.find(m=>m.role==='user');
      const plan=JSON.parse(textOf(user?.content)).harness_plan;
      assert.ok(Array.isArray(plan)&&plan.length>0&&plan.length<=16);
      const finished=messages.some(m=>m.role==='tool');
      const calls=plan.map((call,index)=>{
        assert.ok(request.tools?.some(t=>t.function.name===call.name),'planned tool must be registered');
        return {id:`harness-${index}`,type:'function',function:{name:call.name,arguments:JSON.stringify(call.arguments)}};
      });
      const message=finished?{role:'assistant',content:'Fixture finished; inspect RPC evidence.'}:{role:'assistant',content:null,tool_calls:calls};
      res.writeHead(200,{'Content-Type':'application/json'});
      res.end(JSON.stringify({id:`fixture-${report.model_requests}`,choices:[{index:0,message,finish_reason:finished?'stop':'tool_calls'}],usage:{prompt_tokens:8,completion_tokens:4}}));
    } catch(error) {
      report.fixture_error=error.message;
      if(!res.headersSent)res.writeHead(500);res.end();
    }
  });
  provider.listen(0,'127.0.0.1');await once(provider,'listening');
  const gp=await port(),wp=await port(),ap=await port();
  const identity=name=>({cert_file:join(state,'pki',name,'tls.crt'),key_file:join(state,'pki',name,'tls.key'),ca_file:join(state,'pki/public/ca.crt')});
  const cert=name=>join(state,'pki/public',`${name}.crt`);
  const ep=(name,port)=>({url:`https://127.0.0.1:${port}/rpc`,peer_certificate_file:cert(name)});
  const grant=(id,methods)=>({id,cert_file:cert(id),methods,namespaces:[namespace]});
  const workshopMethods=['workshop.workflows','workshop.submit','workshop.get','workshop.list','workshop.cancel','workshop.resume','workshop.result','workshop.events','workshop.message','workshop.evidence'];
  const gateway={listen:`127.0.0.1:${gp}`,tls:identity('ai-gateway'),authorization:[grant('client',['health']),grant('agent-loop',['gateway.generate']),grant('workshop',['gateway.native'])],models:{chat:{protocol:'chat_completions',endpoint:`http://127.0.0.1:${provider.address().port}/chat`,model:'fixture-model',api_key_env:'HARNESS_FIXTURE_KEY'},responses:{protocol:'responses',endpoint:`http://127.0.0.1:${provider.address().port}/responses`,model:'fixture-model',api_key_env:'HARNESS_FIXTURE_KEY'}}};
  const baseWorkflow={version:'1',instructions:'Run the supplied deterministic crew script.',runtime:'fixture',policy:'workspace-write',timeout_seconds:120,artifacts:[]};
  const check=(name,command,timeout_seconds=30)=>({name,command,timeout_seconds});
  const workflows=[
    {...baseWorkflow,name:'channel'},
    {...baseWorkflow,name:'artifact',artifacts:['artifact.txt'],acceptance:{checks:[check('artifact',['/usr/local/bin/fixture-check','file','/workspace/artifact.txt','good'])]}},
    {...baseWorkflow,name:'isolation',acceptance:{checks:[check('marker',['/pack/checks/verify','file','/pack/checks/source.txt','trusted-original']),check('isolation',['/pack/checks/verify','isolation'])]}},
    {...baseWorkflow,name:'cancel-check',acceptance:{checks:[check('pause',['/usr/local/bin/fixture-check','sleep','60'],90)]}}
  ];
  const workshop={listen:`127.0.0.1:${wp}`,tls:identity('workshop'),authorization:[grant('client',['health','workshop.get','workshop.events','workshop.evidence']),grant('agent-loop',workshopMethods)],workshop:{root:sandbox.host_root,pack_dir:packDir,concurrency:1,queue_capacity:2,max_output_bytes:1024*1024,model_gateway:{...ep('ai-gateway',gp),tls:identity('workshop')},sandbox,engines:{codex:{binary:'codex'}},runtime_profiles:{fixture:{engine:'codex',protocol:'responses',gateway_model:'responses'}},workflows}};
  const loop={listen:`127.0.0.1:${ap}`,tls:identity('agent-loop'),authorization:[grant('client',['health','agent.session.create','agent.session.history','agent.run.start','agent.run.get'])],database:join(state,'agent.sqlite'),gateway:ep('ai-gateway',gp),workshop:ep('workshop',wp),model:'chat',streaming:false,max_steps:4,context_bytes:128000,concurrency:1,pack_dir:join(root,'packs/base')};
  for(const [name,value] of Object.entries({gateway,workshop,loop}))await writeFile(join(state,`${name}.json`),JSON.stringify(value),{mode:0o600});
  start('gateway',join(state,'gateway'),['--config',join(state,'gateway.json')],{HARNESS_FIXTURE_KEY:fixtureKey});
  start('workshop',join(state,'workshop'),['--config',join(state,'workshop.json')]);
  start('loop',process.execPath,[join(root,'services/agent-loop/dist/server.js'),'--config',join(state,'loop.json')]);
  for(const [name,port]of [['ai-gateway',gp],['workshop',wp],['agent-loop',ap]]) {
    const tls=identity('client');
    const client=createRPCClient({url:ep(name,port).url,certFile:tls.cert_file,keyFile:tls.key_file,caFile:tls.ca_file,peerCertificateFile:cert(name),timeoutMs:10000});clients.push(client);
    await until(async()=>{try{return await client.health();}catch{return false;}},`${name} health`);
    if(name==='workshop')broker=client;if(name==='agent-loop')agent=client;
  }
  for(const scenario of selected.map(id=>({S1,S2,S3,S4,S5,S6,S7,S8,S9})[id])) {
    current={id:scenario.name,started_at:new Date().toISOString(),rpc:[],pass:false};
    try {await scenario();current.pass=true;console.log(`PASS ${scenario.name}`);}
    catch(error){current.error=error.stack;throw error;}
    finally {current.finished_at=new Date().toISOString();await writeFile(join(evidenceDir,`${scenario.name}.json`),JSON.stringify(current,null,2));report.scenarios.push({id:current.id,pass:current.pass,evidence:`${scenario.name}.json`});current=undefined;}
  }
  report.pass=true;
} catch(error) {
  report.error=error.stack;process.exitCode=1;console.error(error.message);
} finally {
  for(const client of clients)client.close();
  report.shutdown=[];
  for(const entry of children.reverse())report.shutdown.push({name:entry.name,...await stop(entry)});
  if(report.shutdown.some(entry=>entry.code!==0)){report.pass=false;process.exitCode=1;}
  if(provider){provider.closeAllConnections();await new Promise(resolve=>provider.close(resolve));}
  try {
    const ids=(await exec(docker,['-H',endpoint,'ps','-aq','--filter',`label=ai.easygo.workshop.owner=${owner}`])).stdout.trim();
    report.remaining_containers=ids?ids.split('\n'):[];
    assert.equal(report.remaining_containers.length,0,'owned containers must be removed on shutdown');
  } catch(error){report.pass=false;report.cleanup_error=error.message;process.exitCode=1;}
  for(const entry of children)await copyFile(join(state,`${entry.name}.log`),join(evidenceDir,`${entry.name}.log`)).catch(()=>{});
  try {await unsealState(state);await rm(state,{recursive:true,force:true});report.state_removed=true;}
  catch(error){report.pass=false;report.state_removed=false;report.cleanup_error=error.message;process.exitCode=1;}
  await writeFile(join(evidenceDir,'report.json'),JSON.stringify(report,null,2)+'\n');
  console.log(`REPORT ${join(evidenceDir,'report.json')}`);
}
