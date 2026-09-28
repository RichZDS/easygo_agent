# Task container runtime

The trusted workshop controller and disposable task image are separate. Only the
controller receives the dedicated Docker socket and service mTLS credentials.
Task containers receive their own workspace and an invocation-specific model UDS;
their loopback HTTP shim forwards generation requests to the authenticated relay.
Namespace, model route and protocol are fixed at that relay. No provider/service
credential is passed to the child. Native CLI usage remains diagnostic; the gateway
is the billing authority.

Build from the repository root (explicit dedicated endpoint):

```bash
docker --host unix:///operator/dedicated/docker.sock build \
  -f deploy/runtime/Dockerfile -t easygo-task-runtime:local .
docker --host unix:///operator/dedicated/docker.sock build \
  -f services/workshop/Dockerfile -t easygo-workshop-controller:local .
```

Runtime CLI versions are pinned in the Dockerfile. Tags resolve to the local
immutable image ID at controller initialization; automatic task pulls are disabled.
The controller image intentionally does not support host-mode CLI runs. Legacy
unmanaged host execution remains available outside this controller image.

Add to the operator-owned `workshop` configuration:

```json
{
  "root": "/data/workshop",
  "sandbox": {
    "mode": "docker",
    "endpoint": "unix:///run/docker/docker.sock",
    "image": "easygo-task-runtime:local",
    "owner": "managed-platform-test",
    "host_root": "/absolute/daemon/host/data/workshop",
    "memory_bytes": 1073741824,
    "nano_cpus": 1000000000,
    "pids_limit": 128,
    "tmpfs_bytes": 67108864
  }
}
```

`host_root` is the exact daemon-visible counterpart of `root`. For a containerized
controller, bind that host directory at `/data/workshop`, preserving UID 1000.
Use a short root path to keep per-run Unix socket paths under the OS limit.
The controller needs write access as UID 1000 and Docker socket access through the
operator-selected supplemental group. Do not mount host HOME or copy credentials
into the task root. Host root/ancestors are trusted operator provisioning and must
not be writable by other users or symlinks. Controller-visible paths are checked
component by component; a startup marker probe through the daemon proves the
mapping and UID access before admission. Never map a parent directory or `/`.

All workflows must select gateway runtime profiles, all profiles must use
`gateway_model`, and engine env allowlists must be empty. `engines` still declares
the available runtime names; task executable paths are fixed in the image.
Docker failures never fall back to the host runner. Unknown sandbox modes fail.
Omitting sandbox keeps compatibility with the previous unmanaged API; managed
deployments must explicitly configure `mode: docker`.

Read-only workflows mount `/workspace` read-only; an explicit writable submount
at `/workspace/.workshop-home` holds only that task's native state. Workspace-write
workflows retain a writable task workspace. The mount enforces the read-only
policy even if a CLI or model attempts a file write.

Each invocation uses a new UUID container. Workspace and native HOME persist across
resume; capabilities and socket directories are regenerated. Defaults: 1 GiB memory
with no extra swap, 1 CPU, 128 PIDs, 64 MiB `/tmp`, 8 MiB private `/dev/shm`. Mandatory:
all capabilities dropped, read-only rootfs, UID/GID 1000, no-new-privileges, private
IPC, network none. Docker's default seccomp/AppArmor profiles are retained.
Persistent workspace disk has no quota in this runner. This is not VM isolation.

Cancel, timeout, parser failure and normal exit force-remove the owned container,
killing descendants. Cleanup transport errors fail the run and disable new runner
admissions until operator recovery. Restart reaps only containers matching
managed=true, this owner and this root, under the exclusive database lock. Use
unique owners and separate roots. Unfinished tasks require explicit resume.

## Offline adversarial proof

No provider keys or paid APIs are needed. Build on a dedicated local daemon, then
run the opt-in integration test as UID 1000:

```bash
docker --host unix:///operator/dedicated/docker.sock build \
  -f deploy/runtime/Dockerfile.fixture -t easygo-task-fixture:local .
cd services/workshop
EASYGO_DOCKER_TEST_ENDPOINT=unix:///operator/dedicated/docker.sock \
EASYGO_DOCKER_TEST_IMAGE=easygo-task-fixture:local \
EASYGO_DOCKER_TEST_BINARY=/absolute/path/to/docker \
EASYGO_DOCKER_TEST_ROOT=/tmp \
go test ./workshop -run '^TestDockerIntegration$' -count=1 -v
```

The fixture uses generated local mTLS and checks denied sibling/host sentinel/key/
Docker socket reads, host/external networking, read-only root, UID/capabilities/
no-new-privileges, observed cgroup memory/PID/CPU limits, real UDS model roundtrip,
parent+descendant cancellation, HOME/artifact resume, and owned-only orphan cleanup.
The image contains a fake Codex protocol producer. Four actual native CLIs and
managed compose acceptance remain a separate foreman integration check.

Without explicit endpoint/image variables, the integration test skips. A normal
unit-test pass is not evidence that a container ran. The fixture only removes its
own uniquely named/labeled resources. Do not target a shared/production daemon.
No daemon installation or host network changes are performed by this test.


## Real native CLI proof

Build the pinned runtime image above, then run all four native CLIs through the
same DockerRunner and UDS relay against a generated local mTLS fixture:

```bash
EASYGO_DOCKER_TEST_ENDPOINT=unix:///operator/dedicated/docker.sock \
EASYGO_DOCKER_NATIVE_IMAGE=easygo-task-runtime:local \
EASYGO_DOCKER_TEST_BINARY=/absolute/path/to/docker \
go -C services/workshop test ./workshop -run '^TestDockerNativeRuntimes$' -count=1 -v
```

This opt-in test reports installed versions and checks native first/resume results,
session continuity, namespace/model/protocol callbacks, a deterministic native file
tool call and actual artifact, plus cancellation cleanup. Model responses are
synthetic, provider keys are unnecessary. An unsupported nested CLI sandbox must
fail visibly; never weaken the outer container isolation to pass the fixture.

With the current native Codex `workspace-write` sandbox, hosts that disallow
unprivileged user namespaces can reject its nested bwrap helper. Text generation
and resume still work, but an exec tool cannot create the requested artifact; the
native fixture reports this as a failure. Resolving that compatibility issue must
preserve the outer Docker constraints and read-only workflow mounts. The host
runner's native sandbox policy must not be changed to accommodate containers.
