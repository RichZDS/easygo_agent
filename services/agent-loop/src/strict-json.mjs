// @ts-check
// Kept as standalone ESM so operators and the compiled service use one parser.

/**
 * Parse JSON after checking decoded object-key uniqueness and a maximum of 64
 * nested containers. Callers must decode raw bytes with fatal UTF-8 validation.
 * All failures are constant messages; never include input, keys or positions.
 * @param {string} text
 * @returns {unknown}
 */
export function parseJSON(text) {
  let cursor = 0;
  /** @returns {never} */
  function invalid() {
    throw new SyntaxError('Invalid JSON');
  }
  function whitespace() {
    while (
      cursor < text.length &&
      (text[cursor] === ' ' || text[cursor] === '\t' || text[cursor] === '\r' || text[cursor] === '\n')
    )
      cursor++;
  }
  /** @returns {string} */
  function string() {
    const start = cursor;
    if (text[cursor++] !== '"') invalid();
    while (cursor < text.length) {
      const character = text[cursor++];
      if (character === '"') return JSON.parse(text.slice(start, cursor));
      if (character === '\\') {
        const escape = text[cursor++];
        if (escape === 'u') {
          const digits = text.slice(cursor, cursor + 4);
          if (!/^[0-9a-fA-F]{4}$/.test(digits)) invalid();
          cursor += 4;
        } else if (escape === undefined || !'"\\/bfnrt'.includes(escape)) invalid();
      } else if (character === undefined || character.charCodeAt(0) < 0x20) invalid();
    }
    return invalid();
  }
  const number = /-?(?:0|[1-9][0-9]*)(?:\.[0-9]+)?(?:[eE][+-]?[0-9]+)?/y;
  /** @param {number} depth @returns {void} */
  function value(depth) {
    whitespace();
    const character = text[cursor];
    if (character === '{' || character === '[') {
      if (depth >= 64) invalid();
      const isObject = character === '{';
      const end = isObject ? '}' : ']';
      const keys = new Set();
      cursor++;
      whitespace();
      if (text[cursor] === end) {
        cursor++;
        return;
      }
      while (true) {
        if (isObject) {
          const key = string();
          if (keys.has(key)) invalid();
          keys.add(key);
          whitespace();
          if (text[cursor++] !== ':') invalid();
        }
        value(depth + 1);
        whitespace();
        if (text[cursor] === end) {
          cursor++;
          return;
        }
        if (text[cursor++] !== ',') invalid();
        whitespace();
      }
    }
    if (character === '"') {
      string();
      return;
    }
    for (const literal of ['true', 'false', 'null']) {
      if (text.startsWith(literal, cursor)) {
        cursor += literal.length;
        return;
      }
    }
    number.lastIndex = cursor;
    if (!number.exec(text)) invalid();
    cursor = number.lastIndex;
  }
  try {
    if (typeof text !== 'string') invalid();
    value(0);
    whitespace();
    if (cursor !== text.length) invalid();
    return JSON.parse(text);
  } catch {
    return invalid();
  }
}
