# Worklog deploy

## ACK 2026-09-25T14:29:17+08:00
bootstrap_message_id: 31abe0c9-de78-425a-adb2-c1b5cd27e6e9
checkpoint_message_id: 27987d42-a0d9-4315-8234-f6bf7b55f906
from: rpc-foreman-c926
seat: rpc-deploy-c926
channel: file
status: accepted
pwd: /home/ubuntu/Projects/easygo-rpc-deploy
branch: chore/rpc-deploy-c926
HEAD: 7a936615990c361d38cc14c2c0a183bdc69a1c6a
upstream: none
working_tree_at_ack: clean
push: no
uid: 1000(ubuntu) gid=1001(ubuntu)
docker: command -v docker exit 1, absent
pki_script: /home/ubuntu/Projects/easygo-rpc-services/scripts/dev-pki.sh exists, mode 0775, 1800 bytes, read only
note: Application receipt for bootstrap 31abe0c9-de78-425a-adb2-c1b5cd27e6e9. Checkpoint 27987d42 says this file looked empty; this block is the receipt. PKI layout is taken from that script and the contract. No PKI question is open.

## STATUS 2026-09-25T14:31:24+08:00
checkpoint_message_id: 27987d42-a0d9-4315-8234-f6bf7b55f906
bootstrap_message_id: 31abe0c9-de78-425a-adb2-c1b5cd27e6e9
pwd: /home/ubuntu/Projects/easygo-rpc-deploy
branch: chore/rpc-deploy-c926
HEAD: 10ab59170a7416885d9425a44e6a0230dd66765e
parent_of_HEAD: 7a936615990c361d38cc14c2c0a183bdc69a1c6a
working_tree: clean
push: no
open_questions: none

Docs commit 10ab591 records the dev-pki layout from /home/ubuntu/Projects/easygo-rpc-services/scripts/dev-pki.sh. That script was read, not copied and not executed. This branch still does not contain scripts/.

Blockers, concrete:
- command -v docker exits 1. Docker build NOT RUN. Static YAML check is not container behavior.
- This worktree has Dockerfiles and READMEs only. It does not contain services source, package-lock.json, config.example.json, or scripts/dev-pki.sh, so compose up --build cannot succeed on this checkout alone.

git diff --check HEAD~1 HEAD after the docs commit: exit 0.

## COMPLETE 2026-09-25T14:28:34+08:00
message_id: 31abe0c9-de78-425a-adb2-c1b5cd27e6e9
from: rpc-deploy-c926
to: rpc-foreman-c926
channel: file
status: complete
commit: 7a936615990c361d38cc14c2c0a183bdc69a1c6a
branch: chore/rpc-deploy-c926
parent: a00c1327f0e12cdb5eb1f5e71483b0ec0a45cd31
push: no
working_tree: clean
foreman_ui: not contacted

### Files in the commit
- .dockerignore (replaced the sandbox whitelist with the specified denylist)
- compose.services.yaml
- services/README.md
- services/ai-gateway/Dockerfile
- services/ai-gateway/README.md
- services/agent-loop/Dockerfile
- services/agent-loop/README.md
- services/workshop/Dockerfile
- services/workshop/README.md

No app source, configs, keys, Go modules, package.json, or scripts were edited.

### Commands and exit codes
- python3 /tmp/check-deploy-c926.py (PyYAML 5.4.1): exit 0. Checked service names, build context `.`, dockerfile paths, init true, restart unless-stopped, ports 127.0.0.1:8441/8442/8443, identity/trust/config mounts `:ro`, data volumes, env placement, and that compose text has no docker.sock, Bearer, or 0.0.0.0 host bind.
- git diff --check (before commit): exit 0
- git diff --check HEAD~1 HEAD (after commit): exit 0
- npm view @openai/codex@0.156.0 version: exit 0, printed 0.156.0 (latest tag on the registry is newer; pin left at 0.156.0)
- npm view @anthropic-ai/claude-code@2.1.281 version: exit 0, printed 2.1.281
- command -v docker: exit 1, absent
- command -v docker-compose: absent
- command -v hadolint: absent
- node -v: exit 0, v22.23.2 (host only)
- git commit: exit 0, 7a936615990c361d38cc14c2c0a183bdc69a1c6a
- git status --short after commit: empty, exit 0

Docker build NOT RUN. Static checks are not container behavior.

### Assumptions, not signed
- config mounts are services/<name>/config.example.json, from the Go/loop blueprints. Those files are not on this branch.
- Gateway env names OPENAI_API_KEY, ANTHROPIC_API_KEY, CUSTOM_API_KEY match configs/ai-gateway.example.json api_key_env. agent-loop receives none of them. Workshop receives only OPENAI_API_KEY and ANTHROPIC_API_KEY.
- PKI subdirectories are ai-gateway, agent-loop, workshop, and public. tls.key must be readable by uid 1000. scripts/dev-pki.sh is not in the tree.
- workshop.root should be absolute /data so /data/workshop.db is on workshop-data. Empty root is rejected by the workshop package. Container workdir is /.
- agent-loop database default /data/agent.sqlite is the loop server default. Volume agent-data covers /data.
- A new named volume is usually root-owned and hides the image chown of /data. No root entrypoint was added.

### Questions
None. The three items previously listed here are withdrawn. PKI is fixed by contracts/rpc-v1.md and /home/ubuntu/Projects/easygo-rpc-services/scripts/dev-pki.sh.

### Not done
- No image build, no compose up, no container health, no migration of the legacy Go conversation DB.
- scripts/rpc-call.mjs is absent. READMEs point at contracts/rpc-v1.md and do not invent flags.
- This branch alone cannot build: service source, package-lock.json, and config.example.json are on the other seats' branches.

## ACK 2026-09-25T14:17:59+08:00
message_id: 31abe0c9-de78-425a-adb2-c1b5cd27e6e9
from: rpc-foreman-c926
seat: rpc-deploy-c926
channel: file
status: accepted
note: Application receipt. Foreman UI was not contacted. Blueprint execution continues in /home/ubuntu/Projects/easygo-rpc-deploy on chore/rpc-deploy-c926.
