# Provider and runtime compatibility analysis
assignee: research; status: done
## 1. Anchors
Read services/workshop current runner/types; local installed Codex and Claude versions/help without reading credentials. Research official Codex custom providers (Responses requirement), Claude third-party Anthropic API (DeepSeek official guide), original Pi coding agent (badlogic/pi-mono now redirects earendil-works/pi), OpenClaw local agent mode/custom providers. Use web or curl official primary docs/source only.
## 2. Deliverable
Write doc/runtime-provider-compatibility.md in your worktree, concise cited matrix with exact CLI invocation, config/env, API protocol, structured event/session handling and supported isolation controls. Distinguish actual native CLI fixture-tested compatibility from documented support and unknown. Critical: does direct DeepSeek Responses exist? How does Codex custom provider config work with --ignore-user-config? How can Pi/OpenClaw run noninteractive with task-scoped HOME/session and restricted tools? Verify installed version flags vs current docs. Provide a recommended minimal adapter for each and realistic constraints. Do not design a generic proxy or claim safe sandbox from tool policy.
## 3. Exclusions
No app changes, no credentials inspection, no paid requests, no other worktrees changes. No process launch using real HOME/provider config. Temporary isolated HOME required for any actual CLI probe.
## 4. Proof
Primary citations and source snippets/versions; if CLI probes, report command/exit/log. Probe missing binaries only via separate /tmp install if useful; do not mutate global tools.
## 5. Delivery
One commit for doc only and full report to board/worklog-research.md; ACK bootstrap id there. Five-section handoff at completion; foreman owns further tests/implementation.
