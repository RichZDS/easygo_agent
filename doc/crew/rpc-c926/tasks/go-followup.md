Foreman status: done (2026-09-28); see ../closure.md. Docker runtime validation remains explicitly unexecuted.

# Go final integration fixes
assignee: go
status: done
## 1. Site
Foreman collected ba3221b as root 3bf3464. Review found three bounded integration gaps in owned code.
## 2. Fix
A. services/workshop/config.example.json workshop.root is /var/lib/easygo/workshop, but deployment volume and UID1000 writable directory are /data. Change to /data/workshop. This is required for the independent container config to boot.
B. rpc.Envelope Result has omitempty: successful nil result omits result entirely and violates client envelope validation. Ensure encoding ALWAYS includes result (even null) on success and NEVER includes result on error. Keep Envelope decoding usable for rpctest. A custom MarshalJSON is acceptable. Prove both unary and stream nil-result success.
C. Preserve observability across migration. Old gateway command had JSON observer, new server.New currently constructs gateway with Observer=nil. Restore concurrency-safe JSON metadata-only observation (request id, model/protocol/status/latency/first delta/usage/cost/error) to stderr in production. Also add shared RPC audit hook/logger carrying request id, verified principal id, method, namespace, duration, error code/status; no body or secrets. Library hook may be optional but both production Go servers must enable JSON audit. Tests must capture actual output and verify correlation, success/failure and absence of prompts/keys. Go gateway RequestID already equals RPC id, preserve it for joining model and RPC records.
## 3. Do not
No TS, Dockerfiles, root scripts/README, no contract changes beyond null correctness and observability. No push, no peers. Preserve delivered commit; new followup commit only.
## 4. Verify
Focused shared RPC/gateway/workshop command tests, race and vet. Existing auth/negative tests remain green. No live models. Root independently runs process proof.
## 5. Deliver
New commit and exact evidence in worklog; stop only after file report.
