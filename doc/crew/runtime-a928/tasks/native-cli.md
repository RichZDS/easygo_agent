# Verify actual CLI adapters, including Pi and OpenClaw
assignee: research; status: done
## 1. Anchors
Read main implementation commits f05376c and the following commit titled Wire runtime selection through durable runs and native CLI adapters (resolve hash). Cherry-pick these two into your research branch after your doc commit. Foreman will cherry-pick only YOUR new tests/report commit, not its own ancestors. No implementation edits.
## 2. Deliverable
Independently test workshop CommandRunner with ACTUAL installed Codex/Claude plus isolated /tmp installs of current pinned Pi and OpenClaw (verify npm publisher/package against official upstream). No real HOME/provider credentials or paid API calls. Use local protocol fixture and task RuntimeSpec direct profiles. New tests must exercise actual generated args/config/env/parser via runner.Run, not merely reproduce your own standalone CLI config.
For Pi/OpenClaw first verify runtime_config.go against official docs and source. Report incorrect flags/schema/event parsing as soon as reproduced; don't silently fix implementation or weaken tests. Successful local text turn then native resume if feasible. File-tools/policy only claim what you test; no OS sandbox assertion. Check OpenClaw JSON outcome rejects error/aborted, Pi requires agent_settled. No external channels/delivery. Fresh isolated HOME for every task.
## 3. Scope
New test file(s)/script(s) under services/workshop or scripts, plus your compatibility doc updated with exact versions and results. No implementation/Go module/deployment edits. No push/peer contacts. Installation only into /tmp and never global; no host authentication access.
## 4. Proof
GO_BIN=/home/ubuntu/sdk/go/bin/go. Runtime binaries can be opt-in env vars for a reproducible test, default skip allowed only for explicit external native prerequisites; report actual command with all four binary paths (no secrets). Distinguish CLI fixture pass vs provider live pass. Cite findings using files/lines, actual exit/log.
## 5. Delivery
New commit + ACK message id/worklog + five-section handoff. Tell foreman npm pinned versions so Docker matches tested runtimes. If >5min installer/probe blocked, report bounded progress with exact reason; keep useful source review moving.

## Execution channel correction
Your managed environment currently blocks DNS/listeners; do not try to circumvent or change permissions. Foreman has authorized full-access execution tools and will run installs/tests. Continue writing test/fixture and static review; provide commands/files for foreman to execute. Foreman npm view verified Pi 0.87.1. Node22 installed. No blocker to source work.
