# Durable task service: six-file Node project

Implement the following project in the task workspace. Use Node.js 22+ and Node
built-ins only: no npm installs, third-party imports, model calls or external
network access. The independent oracle starts `bin/server.mjs` directly and never
imports or runs your smoke test.

## Files

Deliver these six application files:

1. `package.json`: `"type":"module"`; scripts `"start":"node bin/server.mjs"` and
   `"test":"node --test tests/smoke.test.mjs"`. No dependencies, devDependencies or
   optionalDependencies (empty objects are also acceptable).
2. `src/store.mjs`: durable task/idempotency storage and mutation logic.
3. `src/server.mjs`: HTTP routing, authentication and request validation.
4. `bin/server.mjs`: executable entry point using the two source modules.
5. `README.md`: startup/configuration, API examples, persistence and test commands.
6. `tests/smoke.test.mjs`: meaningful tests using `node:test` and Node assertions.

All six must be nonempty regular files of at most 1 MiB each, not symlinks.
The `src`, `bin`, and `tests` directories must also be real directories, not symlinks. Do not create application
files outside this set or a node_modules directory. Managed runtime state such as
`.workshop-home` is not an application file. The application must work with its
project directory mounted read-only; runtime data belongs outside the project.

## Startup and persistence

- Environment: `PORT` is an integer port, including `0` for OS allocation;
  `APP_DATA_FILE` is an absolute path to the state file; `APP_TOKEN` is a nonempty
  bearer token. Tests supply only a dummy token and a temporary data file.
- Bind HTTP on `127.0.0.1`. After listening, write exactly one newline-terminated
  JSON line to stdout: `{"port":<actual positive integer port>}`. No other stdout
  output, including per-request logging. Errors may use bounded stderr output.
- Start with no tasks if the state file does not exist. Keep tasks, stable creation
  ordering, versions and idempotency records across process restart. Finish
  persisting every successful mutation before sending its HTTP response. The
  oracle uses SIGKILL after acknowledged writes, not just graceful shutdown.
- Persist atomically (for example, write a sibling temporary file and rename).
  Concurrent requests must not lose writes or corrupt the state file. If persistence
  fails, do not acknowledge a successful mutation. Never store or log APP_TOKEN.
- Task IDs are unique nonempty strings of 1..128 ASCII letters, digits, `_` or `-`.
  Do not reuse an ID after deletion. IDs are opaque; clients need not sort them.

## Common HTTP rules

Every response, including errors, is JSON with `Content-Type: application/json`
(an optional charset is fine). Success objects have exactly the fields specified
below. Error objects are exactly `{"error":"<code>"}`. HTTP headers are not
case-sensitive; authorization values must equal `Bearer ` followed by APP_TOKEN.

`GET /health` is public and returns 200 `{"ok":true}` even without/with incorrect
authorization. Every other route requires the bearer token, otherwise return 401
`unauthorized` before processing the request body. An authenticated unknown path
returns 404 `not_found`; an unsupported method on a known path returns 405
`method_not_allowed`. Do not add extra application routes.

POST/PATCH/DELETE routes require `Content-Type: application/json`, optionally with
`charset=utf-8`; unsupported media types return 415 `unsupported_media_type`.
Read at most **8192 UTF-8 bytes** of body, including whitespace, regardless of
Content-Length versus chunked encoding. A larger body returns 413
`payload_too_large`. Empty/malformed JSON, null/array/primitive bodies, wrong field
names/types, missing fields, or unknown fields return 400 `invalid_request`.
Rejected requests must not mutate state or reserve an idempotency key. Validation
precedes checking a known task's version. There are no query parameters on mutation
routes or individual-task routes; unexpected query parameters return 400.

A task is exactly:

```json
{"id":"opaque-id","title":"trimmed title","done":false,"version":1}
```

A title must be a string. Apply JavaScript `String.prototype.trim()`, then require
1..80 UTF-16 code units (`title.length` in JavaScript). Preserve the trimmed title
exactly, including case and internal whitespace. `done` starts false. Versions are
positive safe integers and start at 1.

## Routes

### POST /tasks

Header `Idempotency-Key` is required and must match `[A-Za-z0-9._:-]{1,128}`.
Missing/invalid keys return 400 `invalid_request`. Body has exactly `{title}`.

- Unused key: create one task and return 201 with the task object.
- Existing key with the same trimmed title: return 200 with the **current** task
  object, including any updated done/version. Do not change ordering/version.
- Existing key with a different trimmed title: return 409 `conflict`.
- A key whose task was deleted remains reserved: any replay returns 409 `conflict`,
  including after restart. Never recreate a deleted task through that key.
- Different keys may create distinct tasks with identical titles.
- Concurrent valid requests with the same key/title produce exactly one 201;
  every other response is 200 with the same ID and no additional task.

### GET /tasks

Only `done`, `offset`, `limit` query parameters are accepted; each may occur once.
`done`, if supplied, must be exactly `true` or `false`. `offset` defaults to 0 and
must be a canonical decimal integer 0..1000000. `limit` defaults to 20 and must be
a canonical decimal integer 1..100. No signs, leading zeros (except `0`), decimals,
exponents, whitespace or empty strings. Invalid/duplicate/unknown parameters return
400 `invalid_request`.

Return 200 `{"items":[...],"total":N}`. Exclude deleted tasks. Filter by `done`
first, preserve original creation order, then apply offset/limit. `total` counts all
matching tasks **before** pagination. An offset beyond the matching tasks returns
an empty items array with the correct total. PATCH must not change creation order.

### GET /tasks/:id

Return 200 with the current task, or 404 `not_found` if it does not exist.

### PATCH /tasks/:id

Body is exactly `{"done":<boolean>,"expected_version":<positive safe integer>}`.
Return 404 `not_found` for an unknown/deleted ID. A version mismatch returns 409
`conflict` without changes. Otherwise set done, increment version by exactly 1
(even if done already has that value), persist, and return 200 with the updated
task. Concurrent PATCH requests with one expected version permit only one success;
the remainder conflict.

### DELETE /tasks/:id

Body is exactly `{"expected_version":<positive safe integer>}`. Unknown/deleted ID
returns 404 `not_found`; a stale version returns 409 `conflict`. Otherwise delete,
persist the deletion and the reserved idempotency key, and return 200
`{"id":"deleted-id","deleted":true}`. Other tasks retain their creation order.

## Independent acceptance

The oracle checks required files/package metadata, clean startup, authentication,
JSON/body limits and strict validation, CRUD and no-mutation-on-errors, stable
filtering/pagination, current-state idempotency, concurrent create/version updates,
deleted-key replay and persistence. It kills/restarts the actual process twice and
checks state after each restart. It never imports generated tests or trusts a
self-reported success flag.

From the repository containing the oracle, set `PROJECT_DIR` to the generated
project directory and use the already built runtime image on a dedicated daemon:

```bash
docker --host unix:///operator/dedicated/docker.sock run --rm \
  --network none --read-only --user 1000:1000 --cap-drop ALL \
  --security-opt no-new-privileges=true --pids-limit 128 \
  --memory 512m --memory-swap 512m --cpus 1 \
  --tmpfs /tmp:rw,nosuid,nodev,noexec,size=67108864,mode=1777 \
  --mount type=bind,src="$PROJECT_DIR",dst=/project,readonly \
  --mount type=bind,src="$PWD/scripts/platform-project-oracle.mjs",dst=/oracle.mjs,readonly \
  --entrypoint /usr/local/bin/node easygo-task-runtime:platform \
  /oracle.mjs /project
```

The application receives no Docker socket, host HOME or credentials. Loopback is
available inside the network-none container. Oracle limits: 3 seconds per startup,
2 seconds per HTTP request, 128 KiB per response, 16 KiB child stdout and 64 KiB
child stderr per process. Overall test budget is 45 seconds with a hard 50-second
exit guard that kills child process groups. It prints one compact JSON result and
exits 0 only when all assertions pass. A failed round and a repaired round must use
the same specification/oracle; do not weaken acceptance to match generated code.
