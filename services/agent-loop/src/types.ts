// Independent copies of pkg/ai's wire types; no service implementation imports.
export interface Block {
  type: string; text?: string; id?: string; name?: string; arguments?: unknown;
  is_error?: boolean; url?: string; media_type?: string; data?: string;
  provider_state?: unknown;
}
export interface Message { role: string; content: Block[] }
export interface Tool { name: string; description?: string; parameters: unknown }
export interface Request {
  model: string; messages: Message[]; tools?: Tool[]; max_output_tokens?: number;
  temperature?: number; tool_choice?: string; parameters?: Record<string, unknown>; request_id?: string;
}
export interface Usage { known: boolean; input_tokens: number; output_tokens: number; cache_read_tokens?: number; cache_write_tokens?: number }
export interface Cost { known: boolean; currency?: string; amount: number }
export interface Response { id?: string; model: string; message: Message; finish_reason: string; usage: Usage; cost: Cost }
export interface Event { type: string; index?: number; delta?: string; id?: string; name?: string }
export interface TLSConfig { cert_file: string; key_file: string; ca_file: string }
export interface Authorization { id: string; cert_file: string; methods: string[]; namespaces: string[] }
export interface Endpoint { url: string; peer_certificate_file: string }
export interface Config {
  listen?: string; tls: TLSConfig; authorization: Authorization[]; database?: string;
  gateway: Endpoint; workshop: Endpoint; model?: string; streaming?: boolean;
  max_steps?: number; context_bytes?: number; concurrency?: number; system_prompt?: string;
}
export interface Run {
  id: string; namespace: string; session_id: string; status: 'queued' | 'running' | 'completed' | 'failed' | 'canceled' | 'interrupted';
  created_at: string; result?: Response; error?: { code: string; message: string; upstream?: unknown };
}
export const METHODS = ['agent.session.create', 'agent.session.list', 'agent.session.history', 'agent.run.start', 'agent.run.get', 'agent.run.cancel', 'agent.run.events'];
