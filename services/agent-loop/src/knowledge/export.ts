import type { Knowledge, Memory, Skill } from './index.js';

/** Explicit bounded export through the same namespace-scoped read surface.
 * Audit provenance is retained separately; no imported run IDs become authority.
 * import contains a preview payload suitable for a fresh target namespace.
 */
export function exportKnowledge(knowledge: Knowledge, namespace: string) {
  const memories = (knowledge.dispatch('agent.memory.list', { namespace }) as { memories: Memory[] }).memories;
  const catalog = (knowledge.dispatch('agent.skills.list', { namespace }) as { skills: { name: string }[] }).skills;
  const skills = catalog.map((s) => knowledge.dispatch('agent.skills.get', { namespace, name: s.name }) as Skill);
  const snapshot = {
    format: 'easygo-knowledge-v1',
    memories,
    skills,
    import: {
      memory: {
        dry_run: true,
        provenance: 'knowledge-export',
        entries: memories.map((m) => ({
          id: m.id,
          kind: m.kind,
          content: m.content,
          importance: m.importance,
          confidence: m.confidence,
        })),
      },
      skills: {
        dry_run: true,
        provenance: 'knowledge-export',
        entries: skills.map((s) => ({ name: s.name, description: s.description, content: s.content })),
      },
    },
  };
  if (Buffer.byteLength(JSON.stringify(snapshot)) > 1048576)
    throw new Error('export_too_large_use_paginated_skill_get');
  return snapshot;
}
