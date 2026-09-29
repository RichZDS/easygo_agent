import type { RpcClient } from '../rpc.js';
import { integer, object, string } from '../validation.js';
import type { ToolEntry } from './registry.js';
import { readMessages } from './messages.js';
import { callWorkshop, recoverableToolError } from './workshop.js';

const short = { type: 'string', minLength: 1, maxLength: 128 };
const offset = { type: 'integer', minimum: 0, maximum: Number.MAX_SAFE_INTEGER };
export function harnessTools(client: RpcClient): ToolEntry[] {
  return [
    {
      definition: {name:'workshop_messages',description:'Read worker messages, read receipts, acceptance and state events. Pass next as after to continue; text_truncated marks an oversized event preview.',parameters:{type:'object',properties:{task_id:short,after:offset},required:['task_id'],additionalProperties:false}},
      roles:['assistant'], mutating:false, recoverable:recoverableToolError,
      execute: (call,run,signal) => {
        const args=object(call.arguments);
        return readMessages(client,{namespace:run.namespace,task_id:string(args.task_id),after:integer(args.after,0,Number.MAX_SAFE_INTEGER)},signal);
      }
    },
    {
      definition: {name:'workshop_reply', description:'Send a worker a reply. Resume a stopped task separately to let it act on the reply.', parameters:{type:'object',properties:{task_id:short,text:{type:'string',minLength:1,maxLength:8192}},required:['task_id','text'],additionalProperties:false}},
      roles:['assistant'], mutating:true, recoverable:recoverableToolError,
      execute: (call,run,signal) => {
        const args = object(call.arguments);
        return callWorkshop(client,'workshop.message', {namespace:run.namespace,task_id:string(args.task_id),text:string(args.text,8192),idempotency_key:`${run.id}:${string(call.id)}`},signal,true);
      }
    },
    {
      definition: {name:'workshop_evidence',description:'List platform acceptance evidence, or read one output page. Pin the returned run_id for subsequent pages.',parameters:{type:'object',properties:{task_id:short,run_id:short,evidence_id:short,offset,limit:{type:'integer',minimum:4,maximum:32768}},required:['task_id'],additionalProperties:false}},
      roles:['assistant'], mutating:false, recoverable:recoverableToolError,
      execute: (call,run,signal) => {
        const args = object(call.arguments);
        const params: Record<string,unknown> = {namespace:run.namespace,task_id:string(args.task_id),offset:integer(args.offset,0,Number.MAX_SAFE_INTEGER),limit:integer(args.limit,8192,32768,4)};
        if (args.run_id !== undefined) params.run_id=string(args.run_id);
        if (args.evidence_id !== undefined) params.evidence_id=string(args.evidence_id);
        return callWorkshop(client,'workshop.evidence',params,signal,false);
      }
    }
  ];
}
