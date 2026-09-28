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
    "tmpfs_bytes": 67108864,
    "disk_quota_bytes": 2147483648,
    "disk_quota_files": 200000,
    "disk_poll_ms": 2000
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
Persistent workspace limits are **soft aggregate limits plus a hard per-file
limit**, not filesystem project quotas or VM isolation. `disk_quota_bytes` defaults
to 2 GiB (16 MiB..1 TiB), `disk_quota_files` to 200000 (1000..10000000), and
`disk_poll_ms` to 2000 (200..60000). The whole task workspace, including native HOME,
is scanned before admission/resume, periodically while running and after exit.
Allocated bytes are `st_blocks * 512`; sparse holes are not charged. Hard-linked
inodes are charged once for bytes; every directory entry (including directories,
symlinks and hard links) counts toward the entry limit, plus the workspace root.
The scanner does not follow symlinks and reads bounded batches; it stops as soon
as a limit is observed. Counters at rejection are therefore lower bounds, not an
exhaustive post-stop census. Scan permission/IO/race failures fail closed as
`disk_quota_scan_failed`; active-tree metadata churn can conservatively fail a run.

Docker also gets `--ulimit fsize=<bytes>:<bytes>`. Docker performs no byte conversion,
and Linux RLIMIT_FSIZE is measured in bytes; writes extending a file beyond that
hard limit get EFBIG or SIGXFSZ. This also limits a sparse file's logical extent.
See [Docker ulimit documentation](https://docs.docker.com/reference/cli/docker/container/run/#set-ulimits-in-container---ulimit)
and [Linux getrlimit](https://man7.org/linux/man-pages/man2/getrlimit.2.html).

An observed aggregate overage cancels/removes the task container, marks its run
failed with `disk_quota_exceeded used_bytes=... used_files=... limit_bytes=...
limit_files=...`, and publishes no artifacts. An already over-quota workspace
cannot resume. Resume RPC uses error.data.code=disk_quota_exceeded (-32014) and a
sanitized counter-only message; scan failure uses disk_quota_scan_failed (-32015).
Host-mode behavior is unchanged. Other task containers are not canceled.

Fast writers can overshoot during the polling interval, scan and container-removal
latency. Deleted-but-open files and filesystem metadata not reachable in the tree
are not an enforceable aggregate quota. Do not promise protection against filling
a shared host disk using this monitor alone. For adversarial/production tenants
or strict host-capacity guarantees, use a dedicated filesystem with per-task XFS
project quotas (and inode limits), or independently bounded VM/task disks; retain
host free-space alarms and capacity headroom. Large entry limits also increase
controller scan CPU/memory costs.

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

Codex's nested bwrap helper cannot create user namespaces under the constrained
container policy on some hosts. After successful Docker initialization and mount
validation, DockerRunner replaces only Codex's inner `sandbox_mode` with
`danger-full-access`, retaining `approval_policy=never`. The process is still
inside the mandatory network-none, nonroot, read-only-rootfs, capability-free,
no-new-privileges, resource-limited Docker container. Read-only workflows retain
the OS-enforced read-only workspace and only task-private native HOME is writable.
This override is internal to Docker command construction, not an RPC option. It
cannot activate through the host CommandRunner, whose sandbox policy is unchanged.
The real-native fixture also asks Codex's exec tool to write under a read-only
workflow and verifies the filesystem rejects it.


## Small-quota Docker proof

Build a separate fixture tag; do not overwrite existing load-test images:

```bash
docker --host unix:///operator/dedicated/docker.sock build \
  -f deploy/runtime/Dockerfile.fixture -t easygo-task-fixture:quota .
EASYGO_DOCKER_TEST_ENDPOINT=unix:///operator/dedicated/docker.sock \
EASYGO_DOCKER_QUOTA_IMAGE=easygo-task-fixture:quota \
EASYGO_DOCKER_TEST_BINARY=/absolute/path/to/docker \
go -C services/workshop test ./workshop -run '^TestDockerDiskQuotaIntegration$' -count=1 -v
```

Cases use 16 MiB quota, at most 128 MiB target writes per case (fixture hard ceiling
256 MiB), and remove their workspaces immediately. They prove per-file EFBIG,
aggregate byte/file termination, rejection before resume creates a container,
unaffected concurrent normal task, final scan of a fast exit, and sparse block
accounting. Logs distinguish the first observed counters from full post-stop usage
and measured overshoot/latency. No test writes the 2 GiB default.
