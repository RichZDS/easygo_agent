# Remote TUI PTY proof

Run against the local fixture stack only. The script uses Python's standard-library PTY with a fixed 120×40 terminal, real `easygo-remote`, and real authenticated TS APIs. It installs no dependencies and calls no external provider.

Start the stack from the same isolated checkout whose Go client you want to test:

```bash
EASYGO_GO_BIN=/home/ubuntu/sdk/go/bin/go \
EASYGO_DOCKER_TEST_BINARY=/tmp/easygo-platform-docker/docker/docker \
EASYGO_DOCKER_TEST_ENDPOINT=unix:///tmp/easygo-platform-docker/docker.sock \
EASYGO_DOCKER_TEST_IMAGE=easygo-sandbox-fixture:platform-load \
EASYGO_PLATFORM_KEEP=1 node scripts/test-platform.mjs
```

After `READY {"state":"...","origin":"..."}`, use the printed origin and `<state>/remote` binary in another terminal:

```bash
export EASYGO_TUI_TEST_PASSWORD=alice-test-password # local fixture only
python3 scripts/test-remote-tui.py \
  --origin http://127.0.0.1:PORT --binary /tmp/egp-STATE/remote \
  --evidence /tmp/crew/platform-b928/tui-proof/positive
python3 scripts/test-remote-tui.py \
  --origin http://127.0.0.1:PORT --binary /tmp/egp-STATE/remote \
  --evidence /tmp/crew/platform-b928/tui-proof/negative --bad-password
unset EASYGO_TUI_TEST_PASSWORD
```

Both commands should exit zero. The negative test asserts the application's exit is 1 and the PTY renders `platform HTTP 401`. Start with a fresh fixture stack or allow a quiet minute before repeating the sequence: the server intentionally limits authentication by email/IP, and several successive runs can receive HTTP 429. The proof does not bypass or reset those limits.

The positive proof asserts:

- Existing sessions list through `--sessions`; a real interactive PTY then creates a new session and renders `EASY GO`.
- Sending a message through terminal input renders `assistant: PLATFORM_OK`.
- `HANG_MODEL` reaches running state. Its actual gateway `waiting` delta must appear in the server's events before the script sends Ctrl+C.
- The PTY renders cancellation and no running work; the authenticated API confirms the same run is `canceled`.
- Killing the TUI and reopening it with `--session` restores its previous messages and `PLATFORM_OK`.
- Existing memory/skill CLI modes return the same populated data as the authenticated API.

Evidence includes current plain-text terminal frames (`*.txt`), raw ANSI captures, provider-event and run receipts, and `report.json` with assertions, elapsed time and limitations. A small VT renderer applies cursor movement and erasure before capturing a frame; it is tailored to this Bubble Tea renderer, not a general terminal emulator. Each frame is drained across PTY chunks before it is checked/saved. Any missing assertion produces nonzero exit and a failed-frame artifact.

Session picking and memory/skill panels do **not** exist inside this TUI. Listing/selection use `--sessions` / `--session`, and knowledge access uses `--rpc`, which accepts only the ten memory/skills methods listed in [cli.md](cli.md); those CLI artifacts are explicitly labeled rather than presented as interactive views. The proof does not add product functionality. Canceling a model whose final usage never arrived can leave a pending hold by design; the script does not resolve that hold administratively. Stop the owned fixture stack with SIGTERM after collecting evidence.
