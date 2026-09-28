/** Trusted Linux operator importer. This is intentionally not reachable through RPC. */
import { openSync, closeSync, constants, fstatSync, readSync, readdirSync } from 'node:fs';
import { resolve } from 'node:path';
const files: Record<string,string> = { 'Agent.md':'agent','Memory.md':'memory','Experiment.md':'experiment','Error.md':'error','Preferences.md':'preference','Style.md':'style','Prompts.md':'prompt','Constraints.md':'constraint' };
function directory(path: string): number {
  let fd = openSync('/',constants.O_RDONLY|constants.O_DIRECTORY);
  try {
    for(const part of resolve(path).split('/').filter(Boolean)) {
      const next = openSync(`/proc/self/fd/${fd}/${part}`,constants.O_RDONLY|constants.O_DIRECTORY|constants.O_NOFOLLOW);
      closeSync(fd); fd=next;
    }
    return fd;
  } catch(e) { closeSync(fd); throw e; }
}
function read(fd: number, name: string): string {
  const child = openSync(`/proc/self/fd/${fd}/${name}`,constants.O_RDONLY|constants.O_NOFOLLOW|constants.O_NONBLOCK);
  try {
    const stat=fstatSync(child); if(!stat.isFile() || stat.size>65536) throw new Error('legacy_file_not_bounded_regular');
    const buffer=Buffer.alloc(65537); const length=readSync(child,buffer,0,buffer.length,0);
    if(length>65536) throw new Error('legacy_file_too_large');
    const bytes=buffer.subarray(0,length); const value = new TextDecoder('utf-8',{fatal:true}).decode(bytes);
    if(value.includes('\0')) throw new Error('legacy_file_not_text'); return value;
  } finally { closeSync(child); }
}
export function previewLegacy(root: string, kind: 'memory'|'skills') {
  if(root.split('/').includes('..')) throw new Error('legacy_traversal');
  const fd=directory(root);
  try {
    const entries: Record<string,unknown>[]=[];
    if(kind==='memory') {
      for(const [file,memoryKind] of Object.entries(files)) {
        let body: string; try { body=read(fd,file); } catch(e) { if((e as NodeJS.ErrnoException).code==='ENOENT') continue; throw e; }
        let current: {id:string;content:string} | undefined;
        const flush = () => { if(current) entries.push({...current,content:current.content.trimEnd(),kind:memoryKind,importance:0.5,confidence:1}); };
        for(const line of body.split('\n')) {
          const m=/^- \[([^\]]+)\] (.*)$/.exec(line);
          if(m) { flush(); current={id:m[1]!,content:m[2]!}; }
          else if(current) current.content+='\n'+line;
        }
        flush();
        if(entries.length>20) throw new Error('legacy_profile_too_large');
      }
    } else {
      const children=readdirSync(`/proc/self/fd/${fd}`,{withFileTypes:true});
      if(children.length>101) throw new Error('legacy_catalog_too_large');
      let total=0;
      for(const child of children) {
        if(child.name==='SKILL.md' && child.isFile()) continue;
        if(!/^[A-Za-z0-9][A-Za-z0-9_-]{0,62}$/.test(child.name) || !child.isDirectory()) throw new Error('legacy_invalid_skill_directory');
        const sub=openSync(`/proc/self/fd/${fd}/${child.name}`,constants.O_RDONLY|constants.O_DIRECTORY|constants.O_NOFOLLOW);
        try {
          const content=read(sub,'SKILL.md'); total+=Buffer.byteLength(content); if(total>1048576) throw new Error('legacy_catalog_too_large');
          const front=/^---\r?\n([\s\S]*?)\r?\n---(?:\r?\n|$)/.exec(content);
          const declared=/^name:\s*([A-Za-z0-9_-]+)\s*$/m.exec(front?.[1]??'')?.[1];
          const description=/^description:\s*(.+)$/m.exec(front?.[1]??'')?.[1]?.trim();
          if(declared!==child.name || !description) throw new Error('legacy_simple_frontmatter_required');
          entries.push({name:declared,description,content});
        } finally { closeSync(sub); }
      }
    }
    return {entries,dry_run:true,provenance:'legacy-markdown'};
  } finally { closeSync(fd); }
}
