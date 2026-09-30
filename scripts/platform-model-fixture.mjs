// Test-only provider fixture. Never mount or use real provider credentials.
import http from 'node:http';
const counts = { chat: 0, responses: 0 };
const textOf = (c) => (typeof c === 'string' ? c : (c ?? []).map((b) => b.text ?? '').join(''));
http
  .createServer(async (req, res) => {
    try {
      if (req.method === 'GET' && req.url === '/metrics') {
        res.setHeader('content-type', 'application/json');
        res.end(JSON.stringify(counts));
        return;
      }
      if (req.headers.authorization !== 'Bearer fixture-key') {
        res.writeHead(401);
        res.end('{}');
        return;
      }
      let raw = '';
      for await (const c of req) {
        raw += c;
        if (Buffer.byteLength(raw) > 2 ** 24) throw Error('too large');
      }
      const p = JSON.parse(raw);
      if (p.model !== 'fixture-model') {
        res.writeHead(400);
        res.end('{}');
        return;
      }
      if (req.url === '/responses') {
        counts.responses++;
        res.setHeader('content-type', 'application/json');
        res.end(
          JSON.stringify({
            id: 'fixture-' + counts.responses,
            status: 'completed',
            error: null,
            output: [
              { type: 'message', role: 'assistant', content: [{ type: 'output_text', text: 'fixture completed' }] },
            ],
            usage: { input_tokens: 7, output_tokens: 3 },
          })
        );
        return;
      }
      if (req.url !== '/chat') {
        res.writeHead(404);
        res.end();
        return;
      }
      counts.chat++;
      const messages = p.messages ?? [],
        system = messages
          .filter((m) => m.role === 'system')
          .map((m) => textOf(m.content))
          .join('\n'),
        last = messages.at(-1),
        input = textOf(last?.content);
      let answer = 'PLATFORM_OK',
        tool;
      if (system.includes('Extract durable') || system.includes('Reconcile prior')) {
        const source = JSON.parse(input);
        const d = Array.isArray(source)
          ? {
              kind: 'preference',
              content: 'Prefers concise answers',
              importance: 0.9,
              confidence: 1,
              source_run_ids: [source[0].run_id],
            }
          : source.extracted[0];
        answer = JSON.stringify({ memories: d ? [d] : [] });
      } else if (input.includes('RECALL_CHECK'))
        answer = system.includes('concise answers') ? 'MEMORY_OK' : 'MEMORY_MISSING';
      else if (last?.role === 'tool') answer = input.includes('SKILL_SENTINEL') ? 'SKILL_OK' : 'SKILL_MISSING';
      else if (input.includes('LOAD_SKILL'))
        tool = {
          id: 'skill-' + counts.chat,
          type: 'function',
          function: { name: 'load_skill', arguments: '{"name":"note"}' },
        };
      const message = tool
          ? { role: 'assistant', content: null, tool_calls: [tool] }
          : { role: 'assistant', content: answer },
        usage = { prompt_tokens: 20, completion_tokens: 5, prompt_tokens_details: { cached_tokens: 4 } };
      if (!p.stream) {
        res.setHeader('content-type', 'application/json');
        res.end(JSON.stringify({ choices: [{ message, finish_reason: tool ? 'tool_calls' : 'stop' }], usage }));
        return;
      }
      res.writeHead(200, { 'content-type': 'text/event-stream' });
      const delta = tool ? { tool_calls: [{ index: 0, ...tool }] } : { content: answer };
      for (const v of [
        { choices: [{ index: 0, delta, finish_reason: null }] },
        { choices: [{ index: 0, delta: {}, finish_reason: tool ? 'tool_calls' : 'stop' }] },
        { choices: [], usage },
      ])
        res.write('data: ' + JSON.stringify(v) + '\n\n');
      res.end('data: [DONE]\n\n');
    } catch {
      if (!res.headersSent) res.writeHead(500);
      res.end();
    }
  })
  .listen(Number(process.env.PORT ?? 9000), '0.0.0.0');
