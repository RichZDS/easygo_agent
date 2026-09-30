// The certificates made by scripts/dev-pki.sh: file paths, config fragments for
// services on loopback, and mTLS clients.
import { join } from 'node:path';
import { createRPCClient } from '../rpc-call.mjs';
import { exec, root } from './procs.mjs';

// Runs dev-pki.sh into dir (a fresh CA plus the service and client identities).
export async function createPKI(dir, options) {
  await exec('bash', [join(root, 'scripts/dev-pki.sh'), dir]);
  return pki(dir, options);
}

// grant() allows these namespaces unless a call passes its own.
export function pki(dir, { namespaces = ['*'] } = {}) {
  const cert = (name) => join(dir, 'public', `${name}.crt`);
  const identity = (name) => ({
    cert_file: join(dir, name, 'tls.crt'),
    key_file: join(dir, name, 'tls.key'),
    ca_file: join(dir, 'public/ca.crt'),
  });
  const endpoint = (name, port) => ({ url: `https://127.0.0.1:${port}/rpc`, peer_certificate_file: cert(name) });
  const grant = (id, methods, scope = namespaces) => ({ id, cert_file: cert(id), methods, namespaces: scope });
  // Presents the `as` identity to `target` on port and pins the target's certificate.
  const client = (as, target, port, options = {}) => {
    const tls = identity(as);
    return createRPCClient({
      url: endpoint(target, port).url,
      certFile: tls.cert_file,
      keyFile: tls.key_file,
      caFile: tls.ca_file,
      peerCertificateFile: cert(target),
      ...options,
    });
  };
  return { dir, cert, identity, endpoint, grant, client };
}
