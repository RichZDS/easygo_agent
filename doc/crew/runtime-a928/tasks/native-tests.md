# Native gateway and relay independent tests
assignee: validation; status: done
## 1. Anchors
Cherry-pick foreman current commit from feat/workshop-agent-runtimes (the one titled Add operator runtime profiles and scoped native model relay) into your test branch; record exact hash first. GO_BIN=/home/ubuntu/sdk/go/bin/go (installed, use absolute path). Inspect gateway.Native, server gateway.native, workshop/startModelRelay. No need TS deps.
## 2. Test deliverable
Write actual Go tests in new *_test.go files ONLY for new native gateway and task-local relay boundaries. Cover real TLS RPC + loopback HTTP where meaningful: selected gateway model forced over child supplied model; same-protocol only and unsupported direct combination rejection; server certificate/method/namespace auth; local relay wrong token/path cannot call upstream; no caller URL/headers overrides; real provider dummy key appears only at provider (child gets ephemeral token); cancel closes upstream; bounded/truncated response; gateway provider errors sanitized; usage/cost observation if recognized, unknown not free. Verify Native currently buffers SSE and does NOT claim live deltas. Distinguish observed issues from speculation; report bugs before changing implementation. You may implement tests demonstrating expected correct behavior even if red; foreman fixes implementation.
## 3. Exclusions
No implementation edits, no paid calls, no external state/credentials, no push/peer communication. Only tests in services/ai-gateway and services/workshop. No unrelated root sources/docs.
## 4. Proof
Run new tests, report actual exits and failures with reproducer. If red due real code report immediately in worklog; do not weaken assertions. Paths and token counts/keys in tests use dummy values.
## 5. Delivery
New commit of tests + worklog receipt message id. Reference exact parent. Final full feature review comes after foreman finishes runtime CLI adapters.
