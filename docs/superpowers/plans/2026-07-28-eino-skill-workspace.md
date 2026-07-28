# Eino Skill Workspace Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add Eino Skill middleware to EasyGo DeepAgent with a shared read-only builtin skill, isolated per-user workspaces, authenticated skill CRUD APIs, user-registration compensation, and startup/daily builtin synchronization.

**Architecture:** A `workspace.Manager` owns the configured directory layout and creates per-user builtin symlinks. Its Eino filesystem backend exposes virtual absolute paths rooted at one user workspace, follows only the shared builtin symlinks, and is read-only to the model. A separate `store.Store` performs validated ZIP upload/list/delete operations, while `sync.Syncer` mirrors versioned builtin sources into runtime builtin storage. The agent runtime creates workspace-scoped filesystem and Skill middleware for every run.

**Tech Stack:** Go 1.25, Eino v0.9.13 ADK/DeepAgent, Gin, GORM, zap, YAML v3, standard-library ZIP/filesystem APIs, doublestar v4 for workspace globbing.

## Global Constraints

- Preserve all pre-existing uncommitted changes; do not stage, commit, push, merge, rebase, or publish a pull request.
- Use GORM only for SQL access. Do not add raw SQL and do not perform SQL calls inside loops.
- Log every error at every layer before returning it, using the project zap logger and typed fields without secrets.
- Add an immediate GoDoc-style purpose comment before every new function, method, test function, and function literal.
- `skill_id` must match `^[a-z0-9-]{2,64}$`.
- Builtin skill IDs cannot be uploaded and cannot be deleted.
- Default ZIP limits are 5 MiB compressed, 20 MiB extracted, and 200 files.
- ZIP entries must reject absolute paths, `..` traversal, symlinks, and non-regular special files.
- Eino receives a virtual absolute filesystem rooted at the authenticated user's workspace; model-facing filesystem tools are read-only.
- The only allowed read target outside the physical workspace is the configured shared `builtin/` tree reached through managed builtin symlinks.
- Do not enumerate or force business skills in `DefaultInstruction`; rely on the Skill middleware's discovery metadata.
- The existing cron manager uses six-field specs, so the daily 03:00 default is `0 0 3 * * *`.
- Startup synchronization and the cron job use the same `Syncer.Sync` function.

---

### Task 1: Skills Configuration and Workspace-Sandboxed Backend

**Files:**
- Modify: `go.mod`
- Modify: `internal/config/config.go`
- Create: `internal/config/config_test.go`
- Modify: `configs/config.yaml`
- Create: `internal/skill/workspace/manager.go`
- Create: `internal/skill/workspace/backend.go`
- Create: `internal/skill/workspace/manager_test.go`
- Create: `internal/skill/workspace/backend_test.go`

**Interfaces:**
- Produces: `config.Skills`
- Produces: `workspace.NewManager(config.Skills) (*Manager, error)`
- Produces: `(*Manager).EnsureWorkspace(context.Context, uint64) (string, error)`
- Produces: `(*Manager).NewBackend(context.Context, uint64) (*Backend, error)`
- Produces: `(*Manager).BuiltinIDs(context.Context) (map[string]struct{}, error)`
- Produces: `Backend` implementing `github.com/cloudwego/eino/adk/filesystem.Backend`

- [ ] **Step 1: Write failing configuration tests**

  Add tests that load a minimal valid config without a `skills` block and assert:

  ```go
  cfg.Skills.RootDir == "skills"
  cfg.Skills.SyncCron == "0 0 3 * * *"
  cfg.Skills.ReadmeSrc == "README.md"
  cfg.Skills.MaxZipBytes == 5<<20
  cfg.Skills.MaxExtractedBytes == 20<<20
  cfg.Skills.MaxFiles == 200
  ```

  Add table tests rejecting empty/negative explicit values after defaults are applied.

- [ ] **Step 2: Run the configuration tests and verify RED**

  Run: `go test ./internal/config -run 'TestLoadSkillsDefaults|TestSkillsValidate' -v`

  Expected: FAIL because `Config.Skills` does not exist.

- [ ] **Step 3: Implement configuration**

  Add:

  ```go
  type Skills struct {
      RootDir           string `yaml:"root_dir"`
      SyncCron          string `yaml:"sync_cron"`
      ReadmeSrc         string `yaml:"readme_src"`
      MaxZipBytes       int64  `yaml:"max_zip_bytes"`
      MaxExtractedBytes int64  `yaml:"max_extracted_bytes"`
      MaxFiles          int    `yaml:"max_files"`
  }
  ```

  Add `Skills Skills` to `Config`, apply the exact defaults before `Validate`, validate every positive limit, and add the values to `configs/config.yaml`.

- [ ] **Step 4: Write failing workspace and backend tests**

  Cover:

  - `EnsureWorkspace` creates `workspaces/{userID}`.
  - Each immediate directory in `builtin/` gets a relative symlink `../../builtin/{skillID}`.
  - Repeated calls are idempotent.
  - A broken or wrong managed symlink is repaired.
  - A real user directory at a builtin ID is rejected rather than deleted.
  - Backend `/user-skill/SKILL.md` reads from only that user's workspace.
  - Backend `/easygo-agent-skill/SKILL.md` follows the managed builtin symlink.
  - `../`, physical absolute paths, another user's workspace, and arbitrary symlinks are rejected.
  - `GlobInfo` finds `/*/SKILL.md`, including builtin symlink directories.
  - `LsInfo`, `Read`, `GrepRaw`, and `GlobInfo` return virtual paths.
  - `Write` and `Edit` always return a read-only error.

- [ ] **Step 5: Run workspace tests and verify RED**

  Run: `go test ./internal/skill/workspace -v`

  Expected: FAIL because the package does not exist.

- [ ] **Step 6: Implement workspace management and the virtual backend**

  `Manager` stores cleaned absolute paths for:

  ```go
  rootDir      = <skills.root_dir>
  builtinSrc   = <rootDir>/builtin-src
  builtinDir   = <rootDir>/builtin
  workspaces   = <rootDir>/workspaces
  ```

  `EnsureWorkspace` creates the user directory with `0750`, enumerates builtin directories once, and creates/replaces only managed symlinks using `os.Symlink(filepath.Join("..", "..", "builtin", skillID), linkPath)`.

  Backend paths are virtual absolute paths. `/foo/bar` maps to `<workspace>/foo/bar`; relative inputs are normalized to the same virtual root for middleware compatibility. Resolve existing symlinks before reads and permit the result only when it is under the physical workspace or builtin root. Reject symlink traversal to any other location.

  Implement globbing with `github.com/bmatcuk/doublestar/v4`, walking managed builtin links explicitly so discovery includes them. Implement line pagination compatible with Eino `ReadRequest`, regexp-based grep with context fields, deterministic path sorting, and read-only `Write`/`Edit`.

- [ ] **Step 7: Run Task 1 tests**

  Run: `go test ./internal/config ./internal/skill/workspace -v`

  Expected: PASS.

---

### Task 2: Builtin Source, Startup Synchronization, and Mirror Semantics

**Files:**
- Modify: `.gitignore`
- Create: `skills/builtin-src/easygo-agent-skill/SKILL.md`
- Create: `skills/builtin-src/easygo-agent-skill/references/README.md`
- Create: `internal/skill/sync/sync.go`
- Create: `internal/skill/sync/sync_test.go`

**Interfaces:**
- Consumes: `config.Skills`
- Produces: `skillsync.New(config.Skills) (*Syncer, error)`
- Produces: `(*Syncer).Sync(context.Context) error`

- [ ] **Step 1: Write failing sync tests**

  In a temporary root, create a source README and builtin-src tree, then assert:

  - `Sync` copies the current source README to `builtin-src/easygo-agent-skill/references/README.md`.
  - `builtin/` exactly mirrors `builtin-src/`.
  - A file and a skill removed from builtin-src are removed from builtin after the next sync.
  - A failed source README read leaves the previous runtime builtin tree intact.

- [ ] **Step 2: Run sync tests and verify RED**

  Run: `go test ./internal/skill/sync -v`

  Expected: FAIL because the package does not exist.

- [ ] **Step 3: Add the demo skill source**

  Create `SKILL.md` with exact frontmatter and a relative workspace read instruction:

  ```markdown
  ---
  name: easygo-agent-skill
  description: Use when the user asks which framework, technology stack, or project architecture powers the EasyGo Agent service.
  ---

  Read `/easygo-agent-skill/references/README.md` with the workspace read-file tool, then answer from that document. Do not guess details that are absent from the document.
  ```

  Seed `references/README.md` with the current repository `README.md`.

- [ ] **Step 4: Implement atomic mirror synchronization**

  `Sync` first copies `ReadmeSrc` into builtin-src using a sibling temporary file plus rename. It then copies the complete builtin-src tree into a sibling temporary directory, preserving regular files and directories but rejecting source symlinks/special files. Swap the temporary mirror into `builtin/`; restore the prior directory if the final rename fails. Remove the previous mirror only after the new mirror is active.

  Ignore only runtime data:

  ```gitignore
  skills/builtin/
  skills/workspaces/
  ```

- [ ] **Step 5: Run Task 2 tests**

  Run: `go test ./internal/skill/sync ./internal/skill/workspace -v`

  Expected: PASS.

---

### Task 3: User Skill ZIP Store

**Files:**
- Create: `internal/skill/store/store.go`
- Create: `internal/skill/store/archive.go`
- Create: `internal/skill/store/frontmatter.go`
- Create: `internal/skill/store/store_test.go`

**Interfaces:**
- Consumes: `*workspace.Manager`, `config.Skills`
- Produces:

  ```go
  type SkillInfo struct {
      SkillID     string `json:"skill_id"`
      Source      string `json:"source"`
      Readonly    bool   `json:"readonly"`
      Description string `json:"description"`
  }

  func New(cfg config.Skills, workspaces *workspace.Manager) *Store
  func (s *Store) Upload(ctx context.Context, userID uint64, skillID string, archive io.ReaderAt, archiveSize int64) (SkillInfo, error)
  func (s *Store) List(ctx context.Context, userID uint64) ([]SkillInfo, error)
  func (s *Store) Delete(ctx context.Context, userID uint64, skillID string) error
  ```

- [ ] **Step 1: Write failing validation and extraction tests**

  Cover:

  - valid root-layout ZIP installs under the request `skill_id`;
  - valid single-top-directory ZIP strips that directory;
  - invalid `skill_id`, missing `SKILL.md`, ambiguous roots, oversize compressed input, oversize extracted data, too many files, absolute paths, `..`, backslash traversal, symlinks, and special entries fail;
  - an existing user skill and a builtin name return `errorcode.Conflict`;
  - a failed extraction leaves no visible partial skill.

- [ ] **Step 2: Run upload tests and verify RED**

  Run: `go test ./internal/skill/store -run 'TestStoreUpload' -v`

  Expected: FAIL because the store does not exist.

- [ ] **Step 3: Implement safe archive installation**

  Validate `skill_id` before filesystem access. Use `zip.NewReader`, normalize entry names with slash semantics, reject unsafe or non-regular entries, identify exactly one accepted layout, and stream each file through a `LimitReader` while enforcing aggregate limits.

  Serialize store mutations with a mutex. Extract into a hidden temporary directory beneath the user's workspace, validate the extracted `SKILL.md` frontmatter enough for Eino to parse it, then rename to the final request-derived directory. Never trust an archive directory name.

- [ ] **Step 4: Write failing list/delete/isolation tests**

  Cover:

  - list merges sorted builtin and user skills with exact `source` and `readonly` values;
  - description comes from YAML frontmatter or is empty;
  - deleting a user skill succeeds;
  - deleting a builtin ID returns `errorcode.Forbidden`;
  - user A's installed skill is absent from user B's workspace, list, and backend reads.

- [ ] **Step 5: Run list/delete tests and verify RED**

  Run: `go test ./internal/skill/store -run 'TestStoreList|TestStoreDelete|TestStoreIsolation' -v`

  Expected: FAIL until list/delete are implemented.

- [ ] **Step 6: Implement list and delete**

  Always call `EnsureWorkspace` first. Derive builtin identity from `Manager.BuiltinIDs`, never from client input or symlink text. List only immediate directories containing a regular `SKILL.md`; return stable skill ID ordering. Delete only a non-symlink user directory whose validated path is directly under the current user's workspace.

- [ ] **Step 7: Run Task 3 tests**

  Run: `go test ./internal/skill/store ./internal/skill/workspace -v`

  Expected: PASS.

---

### Task 4: Authenticated Skill HTTP API

**Files:**
- Create: `internal/controller/skill.go`
- Create: `internal/controller/skill_test.go`
- Modify: `internal/controller/init.go`
- Modify: `internal/server/router.go`
- Modify: `internal/wire/wire.go`
- Regenerate: `internal/wire/wire_gen.go`

**Interfaces:**
- Consumes: `*store.Store`
- Produces: `controller.NewSkillController`
- Produces authenticated routes:

  ```text
  POST   /api/v1/skills
  GET    /api/v1/skills
  DELETE /api/v1/skills/:skill_id
  ```

- [ ] **Step 1: Write failing controller tests**

  Use Gin test mode and a fake `SkillService` to assert:

  - multipart upload passes authenticated user ID, `skill_id`, file reader, and size to the store;
  - valid upload returns HTTP 201;
  - malformed multipart and a missing file return HTTP 400;
  - list returns HTTP 200 with the store result;
  - delete passes the path `skill_id`;
  - Conflict/Forbidden store errors map to HTTP 409/403;
  - routes reject unauthenticated requests.

- [ ] **Step 2: Run controller tests and verify RED**

  Run: `go test ./internal/controller ./internal/server -run 'TestSkill' -v`

  Expected: FAIL because the controller and routes do not exist.

- [ ] **Step 3: Implement the controller and routing**

  Define a narrow controller interface matching the three store methods. Cap the request body before multipart parsing, open/close the uploaded file, and pass `middleware.UserID(c)` on every operation. Register all three routes inside the existing authorized group.

  Add `Skill *SkillController` to `AllControllers`. Update the Wire provider graph to accept the already-constructed `*store.Store`.

- [ ] **Step 4: Regenerate Wire**

  Fix the stale `internal/auth` import in `wire.go` to `internal/platform/auth`, then run:

  `go generate ./internal/wire`

  Expected: `wire_gen.go` includes `SkillController` and the new dependencies without hand-editing generated code.

- [ ] **Step 5: Run Task 4 tests**

  Run: `go test ./internal/controller ./internal/server ./internal/wire -v`

  Expected: PASS.

---

### Task 5: User Registration Workspace Creation and Compensation

**Files:**
- Modify: `internal/model/user.go`
- Modify: `internal/service/user/user.go`
- Create: `internal/service/user/user_test.go`
- Modify through regeneration: `internal/wire/wire_gen.go`

**Interfaces:**
- Consumes: `workspace.Ensurer`
- Produces: `model.HardDeleteNewUser(context.Context, *gorm.DB, uint64) error`
- Changes: `user.NewUserService(*gorm.DB, workspace.Ensurer) UserService`

- [ ] **Step 1: Write failing user lifecycle tests**

  Use an injected test repository and fake workspace ensurer to assert:

  - successful DB creation calls `EnsureWorkspace` with the assigned user ID;
  - workspace failure calls hard-delete compensation once and returns the workspace error;
  - compensation failure returns an error containing both failures;
  - no compensation runs when DB creation itself fails.

- [ ] **Step 2: Run lifecycle tests and verify RED**

  Run: `go test ./internal/service/user -run 'TestCreateUserWorkspace' -v`

  Expected: FAIL because the service has no workspace dependency.

- [ ] **Step 3: Implement repository seam and compensation**

  Introduce a private repository interface for user persistence so lifecycle behavior is testable without a database. Its GORM implementation delegates to existing model functions. Add `HardDeleteNewUser` using:

  ```go
  db.WithContext(ctx).Unscoped().Delete(&User{}, id)
  ```

  After `CreateUser` succeeds, call `EnsureWorkspace`. On failure, hard-delete only that newly created ID. Log the workspace failure and cleanup result with safe fields at model, repository, and service layers before returning.

- [ ] **Step 4: Regenerate Wire and run Task 5 tests**

  Run:

  ```bash
  go generate ./internal/wire
  go test ./internal/service/user ./internal/model ./internal/wire -v
  ```

  Expected: PASS.

---

### Task 6: Per-User Eino Skill and Read-Only Filesystem Middleware

**Files:**
- Modify: `internal/agent/runtime.go`
- Create: `internal/agent/runtime_skill_test.go`
- Modify: `internal/service/chat/run.go`
- Modify: `internal/app/app.go`
- Modify: `internal/wire/wire.go`
- Regenerate: `internal/wire/wire_gen.go`

**Interfaces:**
- Changes:

  ```go
  func NewRuntimeFactory(registry *Registry, workspaces *workspace.Manager) *RuntimeFactory
  func (f *RuntimeFactory) Build(ctx context.Context, userID uint64, spec ModelSpec) (*adk.TypedRunner[*schema.AgenticMessage], error)
  ```

- [ ] **Step 1: Write failing runtime middleware tests**

  Build a temporary workspace containing the demo skill, invoke the produced handlers' `BeforeAgent`, and assert:

  - tool names include `skill`, `ls`, `read_file`, `glob`, and `grep`;
  - tool names exclude `write_file`, `edit_file`, and `execute`;
  - the Skill tool description contains the demo skill name/description;
  - the filesystem instruction describes virtual workspace-rooted absolute paths;
  - constructing handlers for user A cannot discover user B's skill.

- [ ] **Step 2: Run runtime tests and verify RED**

  Run: `go test ./internal/agent -run 'TestRuntimeSkill' -v`

  Expected: FAIL because RuntimeFactory has no workspace dependency or handlers.

- [ ] **Step 3: Implement middleware assembly**

  For each `Build`:

  1. Call `workspaces.NewBackend(ctx, userID)`, which ensures the workspace.
  2. Create Eino `skill.NewBackendFromFilesystem` with the scoped backend and virtual `BaseDir: "/"`.
  3. Create `skill.NewTyped[*schema.AgenticMessage]`.
  4. Create `filesystem.NewTyped[*schema.AgenticMessage]` with write/edit disabled and workspace-specific descriptions.
  5. Pass both handlers to `deep.NewTyped`.

  Do not add the demo skill name to `DefaultInstruction`. Do not set a shell backend. Log each construction error before returning it.

- [ ] **Step 4: Pass authenticated user identity into RuntimeFactory**

  Change `RunService.Prepare` to call:

  ```go
  runner, err := s.runtime.Build(ctx, userID, spec)
  ```

  Update app construction and Wire signatures for the shared workspace manager.

- [ ] **Step 5: Regenerate Wire and run Task 6 tests**

  Run:

  ```bash
  go generate ./internal/wire
  go test ./internal/agent ./internal/service/chat ./internal/wire -v
  ```

  Expected: PASS.

---

### Task 7: Startup Sync, Daily Cron Registration, and Whole-Feature Verification

**Files:**
- Modify: `internal/cronjob/register.go`
- Create: `internal/cronjob/register_test.go`
- Modify: `internal/app/app.go`
- Modify: `README.md` only if configuration/run instructions are absent after implementation review

**Interfaces:**
- Produces:

  ```go
  type BuiltinSkillSyncer interface {
      Sync(context.Context) error
  }

  func RegisterBuiltinCronJobs(m *Manager, syncer BuiltinSkillSyncer, spec string) error
  ```

- [ ] **Step 1: Write failing cron registration tests**

  With the existing in-memory cron run store and a fake syncer, assert:

  - registration uses job name `sync_builtin_skills`;
  - the configured six-field cron spec is accepted;
  - `TriggerForTest` calls `Sync`;
  - a sync error produces a failed cron run and is returned by the handler;
  - duplicate/invalid registration errors are returned and logged.

- [ ] **Step 2: Run cron tests and verify RED**

  Run: `go test ./internal/cronjob -run 'TestRegisterBuiltinSkillSync' -v`

  Expected: FAIL because no builtin sync job is registered.

- [ ] **Step 3: Register startup and daily synchronization**

  In `app.Run`, after logger initialization and before constructing workspaces/runners:

  ```go
  skillSyncer, err := skillsync.New(cfg.Skills)
  // log and return on error
  err = skillSyncer.Sync(ctx)
  // log and return on error
  ```

  Construct one shared `workspace.Manager`, one `store.Store`, and one `RuntimeFactory`. Register `sync_builtin_skills` with `cfg.Skills.SyncCron` before starting the cron manager. Pass the workspace manager and store through Wire.

- [ ] **Step 4: Run focused integration tests**

  Run:

  ```bash
  go test ./internal/skill/... ./internal/config ./internal/agent ./internal/controller ./internal/server ./internal/service/user ./internal/cronjob ./internal/wire -v
  ```

  Expected: PASS.

- [ ] **Step 5: Run formatting, static analysis, and the full suite**

  Run:

  ```bash
  gofmt -w <all changed Go files>
  go vet ./...
  go test -race ./...
  go test ./...
  ```

  Expected: every command exits 0.

- [ ] **Step 6: Verify acceptance criteria against the design**

  Check:

  - startup creates `skills/builtin/easygo-agent-skill` from builtin-src;
  - `EnsureWorkspace` creates the relative builtin symlink for two test users;
  - user A upload/list/read is invisible to user B;
  - builtin upload is 409 and builtin delete is 403;
  - runtime middleware discovers the demo skill and exposes read-only file tools;
  - `DefaultInstruction` does not name any business skill;
  - no unauthorized raw SQL, Redis bypass, or SQL-in-loop was introduced;
  - all new functions and function literals have purpose comments;
  - every new returned error is logged at each returning layer.

- [ ] **Step 7: Record the manual model check as an operator follow-up**

  Do not claim a real-model conversation unless credentials and a running MySQL instance are available. Provide the exact manual prompt:

  ```text
  你用的什么框架？
  ```

  The operator should confirm SSE/log output contains a `skill` load and `read_file` of `/easygo-agent-skill/references/README.md`, and that the answer includes Eino/README technology-stack details.
