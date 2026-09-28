import { isIP } from 'node:net';

/** @param {unknown} value */
function normalizeAddress(value) {
  if (typeof value !== 'string' || value.includes('%')) return undefined;
  const family = isIP(value);
  if (family === 4) return value;
  if (family !== 6) return undefined;
  // WHATWG URL serializes equivalent IPv6 literals to one canonical spelling.
  const canonical = new URL(`http://[${value}]/`).hostname.slice(1, -1);
  const mapped = /^::ffff:([a-f0-9]{1,4}):([a-f0-9]{1,4})$/.exec(canonical);
  if (!mapped) return canonical;
  const high = parseInt(mapped[1], 16), low = parseInt(mapped[2], 16);
  return `${high >>> 8}.${high & 255}.${low >>> 8}.${low & 255}`;
}

/** Validate operator input before creating storage or opening a listening socket.
 * @param {unknown} value
 * @returns {Set<string>}
 */
export function trustedProxyAddresses(value = undefined) {
  if (value === undefined) return new Set();
  if (!Array.isArray(value) || value.length > 16) throw Error('trusted_proxies must be an array of at most 16 IP literals');
  const addresses = [];
  for (const entry of value) {
    const address = normalizeAddress(entry);
    if (!address) throw Error('trusted_proxies entries must be unscoped IPv4 or IPv6 literals');
    addresses.push(address);
  }
  return new Set(addresses);
}

/** Capture an immutable trust list; request headers cannot extend it.
 * @param {unknown} value
 * @returns {(req: import('node:http').IncomingMessage) => string}
 */
export function createClientAddress(value) {
  const trusted = trustedProxyAddresses(value);
  return function clientAddress(req) {
    const socket = normalizeAddress(req.socket.remoteAddress) ?? req.socket.remoteAddress ?? 'unknown';
    if (!trusted.has(socket)) return socket;
    const header = req.headers['x-forwarded-for'];
    if (typeof header !== 'string' || Buffer.byteLength(header) > 2048) return socket;
    const chain = header.split(',');
    if (chain.length > 32) return socket;
    for (let i = chain.length - 1; i >= 0; i--) {
      const address = normalizeAddress(chain[i].trim());
      if (!address) return socket;
      // Anything to the left of this untrusted hop is attacker-controlled. Stop
      // here even if that ignored prefix contains malformed text (e.g. evil, IP).
      if (!trusted.has(address)) return address;
    }
    // An all-proxy chain did not identify a client; retain the safe socket key.
    return socket;
  };
}
