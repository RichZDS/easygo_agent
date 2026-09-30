// Service configurations for the end-to-end scripts. The managed platform comes from
// the same generator scripts/configure-platform.mjs uses for Compose, on loopback
// ports with databases and the workshop root under the run's state directory.
import { writeFile } from 'node:fs/promises';
import { join } from 'node:path';
import { platformConfigs } from '../configure-platform.mjs';

// certs is a pki() from ./pki.mjs; ports maps ai-gateway, agent-loop, workshop and web
// to loopback ports. options are passed on to platformConfigs().
export function localPlatform(state, certs, ports, options = {}) {
  return platformConfigs({
    layout: {
      listen: (service) => `127.0.0.1:${ports[service]}`,
      identity: certs.identity,
      cert: certs.cert,
      endpoint: (service) => certs.endpoint(service, ports[service]),
    },
    data: (file) => join(state, file),
    web: `127.0.0.1:${ports.web}`,
    origin: `http://127.0.0.1:${ports.web}`,
    workshopRoot: join(state, 'workshop-data'),
    ...options,
  });
}

// Writes each configuration to <dir>/<name>.json, readable only by the owner.
export async function writeConfigs(dir, configs) {
  for (const [name, value] of Object.entries(configs))
    await writeFile(join(dir, `${name}.json`), JSON.stringify(value), { mode: 0o600 });
}
