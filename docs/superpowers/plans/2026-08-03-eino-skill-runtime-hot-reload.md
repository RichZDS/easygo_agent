# Eino Skill Runtime and Next-Turn Hot Reload Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Connect builtin and user-scoped Eino skills to every chat Turn, expose safe user upload/list/delete APIs, and guarantee completed mutations are visible on the next Turn.

**Architecture:** Startup atomically mirrors versioned builtin skill sources into runtime storage. A workspace-scoped backend is rebuilt for every Turn and feeds explicit read-only filesystem and Eino Skill handlers. User uploads and deletes are staged outside discoverable workspaces and atomically renamed at publication boundaries, so a new Runner sees either the old or new complete catalog.

**Tech Stack:** Go 1.25, Eino v0.9.13 ADK/DeepAgent, Gin, Wire, YAML v3, standard-library ZIP/filesystem APIs, zap.

## Global Constraints

- Work in the current user-owned working tree; do not create branches or worktrees.
- Do not stage files, create or amend commits, push, merge, rebase, or publish pull requests.
- Do not add raw SQL or any SQL call inside a loop.
- Log every returned error at every layer with typed zap fields and no secrets or file contents.
- Add an immediately preceding purpose comment to every named function, method, test function, and function literal.
- User skill changes are guaranteed only for the next Turn; an already-running Turn has no hot-switch guarantee.
- Runtime file tools are exactly `ls`, `read_file`, `glob`, and `grep`; never expose `write_file` or `edit_file`.
- Only inline uploaded skills are accepted; non-empty `context`, `agent`, or `model` frontmatter fields are invalid.

---

### Task 1: Active Skills Configuration and Shared Manifest Validation

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`
- Modify: `configs/config.yaml`
- Create: `internal/skill/manifest/manifest.go`
- Create: `internal/skill/manifest/manifest_test.go`

**Interfaces:**
- Produces: `manifest.FrontMatter`
- Produces: `manifest.ValidateID(string) error`
- Produces: `manifest.Parse([]byte) (FrontMatter, error)`
- Produces: `manifest.Read(context.Context, string) (FrontMatter, error)`

- [ ] **Step 1: Write failing configuration tests**

  Remove the `SyncCron` assertion from `TestLoadSkillsDefaults`, remove the empty-cron validation case, and add a strict decode case:

  ```go
  // TestLoadRejectsRemovedSyncCron verifies that inactive configuration is not silently accepted.
  func TestLoadRejectsRemovedSyncCron(t *testing.T) {
      path := writeConfigFile(t, "skills:\n  sync_cron: \"0 0 3 * * *\"\n")
      if _, err := Load(path); err == nil {
          t.Fatal("Load() error = nil, want unknown-field error")
      }
  }
  ```

- [ ] **Step 2: Run configuration RED**

  Run: `go test ./internal/config -run 'TestLoadSkillsDefaults|TestSkillsValidate|TestLoadRejectsRemovedSyncCron' -v`

  Expected: FAIL because `sync_cron` is still accepted.

- [ ] **Step 3: Remove inactive cron configuration**

  Delete `Skills.SyncCron`, its default, its validation branch, and `skills.sync_cron` from `configs/config.yaml`. Preserve these exact defaults:

  ```go
  Skills: Skills{
      RootDir:           "skills",
      ReadmeSrc:         "README.md",
      MaxZipBytes:       5 << 20,
      MaxExtractedBytes: 20 << 20,
      MaxFiles:          200,
  },
  ```

- [ ] **Step 4: Write failing manifest tests**

  Cover valid inline frontmatter, invalid IDs, missing delimiters, empty name/description, name mismatch handled by the caller, and rejection of non-empty `context`, `agent`, or `model`:

  ```go
  // TestParseAcceptsInlineSkill verifies the supported minimal Eino manifest.
  func TestParseAcceptsInlineSkill(t *testing.T) {
      got, err := Parse([]byte("---\nname: weather-guide\ndescription: Answers weather workflow questions.\n---\nRead references."))
      if err != nil {
          t.Fatalf("Parse() error = %v", err)
      }
      if got.Name != "weather-guide" || got.Description == "" {
          t.Fatalf("Parse() = %#v", got)
      }
  }
  ```

- [ ] **Step 5: Run manifest RED**

  Run: `go test ./internal/skill/manifest -v`

  Expected: FAIL because the package does not exist.

- [ ] **Step 6: Implement manifest parsing and ID validation**

  Implement the exact schema and grammar:

  ```go
  type FrontMatter struct {
      Name        string `yaml:"name"`
      Description string `yaml:"description"`
      Context     string `yaml:"context"`
      Agent       string `yaml:"agent"`
      Model       string `yaml:"model"`
  }

  var idPattern = regexp.MustCompile(`^[a-z0-9-]{2,64}$`)
  ```

  `Parse` must require opening and closing `---`, use `yaml.Decoder.KnownFields(true)`, trim name/description, validate the ID grammar, require a description, and reject all non-inline execution fields. `Read` checks `ctx.Err()`, reads the file, delegates to `Parse`, and logs every returned error.

- [ ] **Step 7: Verify Task 1**

  Run: `gofmt -w internal/config/config.go internal/config/config_test.go internal/skill/manifest/*.go && go test ./internal/config ./internal/skill/manifest -v`

  Expected: PASS.

---

### Task 2: Managed Staging and Atomic Builtin Startup Sync

**Files:**
- Modify: `.gitignore`
- Modify: `internal/skill/workspace/manager.go`
- Modify: `internal/skill/workspace/manager_test.go`
- Create: `internal/skill/sync/sync.go`
- Create: `internal/skill/sync/sync_test.go`
- Create: `skills/builtin-src/easygo-agent-skill/SKILL.md`
- Create: `skills/builtin-src/easygo-agent-skill/references/README.md`

**Interfaces:**
- Produces: `(*workspace.Manager).StagingDir(context.Context) (string, error)`
- Produces: `skillsync.New(config.Skills) (*Syncer, error)`
- Produces: `(*Syncer).Sync(context.Context) error`

- [ ] **Step 1: Write failing workspace staging test**

  Assert `NewManager` creates a non-symlink `0750` staging directory and `StagingDir` returns its cleaned absolute path.

- [ ] **Step 2: Write failing synchronization tests**

  Tests must build a temporary `builtin-src`, source README, and old `builtin`, then assert:

  - current README replaces the demo reference;
  - runtime builtin exactly mirrors source files;
  - removed source files disappear from the mirror;
  - invalid manifests and source symlinks fail;
  - any pre-publication failure leaves the old builtin intact.

- [ ] **Step 3: Run Task 2 RED**

  Run: `go test ./internal/skill/workspace ./internal/skill/sync -v`

  Expected: FAIL because staging and sync APIs do not exist.

- [ ] **Step 4: Extend the workspace manager**

  Add `staging string` to `Manager`, initialize it as `filepath.Join(rootDir, "staging")`, create it with the other managed directories, and validate it with `validateManagedDirectory` before returning it.

- [ ] **Step 5: Add the builtin demo source**

  Create this exact entry skill:

  ```markdown
  ---
  name: easygo-agent-skill
  description: Use when the user asks which framework, technology stack, or project architecture powers the EasyGo Agent service.
  ---

  Read `/easygo-agent-skill/references/README.md` with `read_file`, then answer only from that document. Do not guess details absent from the document.
  ```

  Seed `references/README.md` from the current repository README. Ignore only runtime state:

  ```gitignore
  skills/builtin/
  skills/staging/
  skills/workspaces/
  ```

- [ ] **Step 6: Implement atomic startup sync**

  `Sync` must:

  1. check context cancellation;
  2. atomically refresh the demo README through a sibling temporary file;
  3. validate every first-level source directory ID and `SKILL.md` through `manifest.Read`;
  4. reject symlinks and special files while copying to a sibling temporary mirror;
  5. rename old `builtin` to a backup, rename the complete temporary mirror to `builtin`, restore the backup if publication fails, and remove the backup only after success.

  Every copy loop checks `ctx.Err()` and streams regular files with `io.Copy` rather than loading the full tree into memory.

- [ ] **Step 7: Verify Task 2**

  Run: `gofmt -w internal/skill/workspace/*.go internal/skill/sync/*.go && go test ./internal/skill/workspace ./internal/skill/sync -v`

  Expected: PASS.

---

### Task 3: Atomic User Skill Store

**Files:**
- Create: `internal/skill/store/store.go`
- Create: `internal/skill/store/archive.go`
- Create: `internal/skill/store/store_test.go`
- Create: `internal/skill/store/archive_test.go`

**Interfaces:**
- Consumes: `workspace.Manager`, `manifest.Parse`, `config.Skills`
- Produces: `store.SkillInfo`
- Produces: `store.New(config.Skills, *workspace.Manager) (*Store, error)`
- Produces: `(*Store).Upload(context.Context, uint64, string, io.Reader) (SkillInfo, error)`
- Produces: `(*Store).List(context.Context, uint64) ([]SkillInfo, error)`
- Produces: `(*Store).Delete(context.Context, uint64, string) error`
- Produces: `(*Store).CleanupStaging(context.Context) error`
- Produces sentinel errors: `ErrInvalidSkill`, `ErrArchiveTooLarge`, `ErrConflict`, `ErrBuiltinReadOnly`, `ErrNotFound`

- [ ] **Step 1: Write archive validation tests**

  Use an in-memory ZIP helper and table tests for root layout, unique-top-directory layout, missing `SKILL.md`, ambiguous layout, traversal, absolute paths, backslashes, symlinks, special modes, too many files, and extracted-byte overflow.

- [ ] **Step 2: Write store behavior tests**

  Cover upload/list/delete, builtin conflicts, user conflicts, manifest name mismatch, readonly builtin listing, deterministic sorting, cross-user isolation, failed-upload cleanup, and cleanup of only Store-owned staging prefixes.

  Add a next-scan assertion using `manager.NewBackend` plus Eino `skill.NewBackendFromFilesystem`: list before upload, upload, create a fresh backend and list, delete, create another fresh backend and list again.

- [ ] **Step 3: Run Task 3 RED**

  Run: `go test ./internal/skill/store -v`

  Expected: FAIL because the package does not exist.

- [ ] **Step 4: Implement bounded ZIP staging**

  Read at most `MaxZipBytes + 1` bytes and return `ErrArchiveTooLarge` when exceeded. Normalize ZIP names with slash semantics, reject any `..`, leading slash, backslash, symlink, or non-regular/non-directory entry, detect exactly one supported layout, and stream extracted files while enforcing `MaxExtractedBytes` and `MaxFiles`.

  Extract only below a directory created with `os.MkdirTemp(stagingDir, "skill-upload-")`; validate the staged `SKILL.md` and exact name match before publication.

- [ ] **Step 5: Implement Store publication, listing, and deletion**

  Use one Store mutation mutex only for final builtin-conflict recheck, target `Lstat`, and rename boundaries. Never hold it during ZIP decompression.

  `List` uses `EnsureWorkspace`, exact managed builtin IDs, `manifest.Read`, and stable skill-ID sorting. `Delete` rejects builtin IDs and symlinks, renames a user directory to a `skill-delete-` staging path, then removes that path. `CleanupStaging` removes only entries beginning with `skill-upload-` or `skill-delete-`.

- [ ] **Step 6: Verify Task 3**

  Run: `gofmt -w internal/skill/store/*.go && go test -race ./internal/skill/store ./internal/skill/workspace -v`

  Expected: PASS.

---

### Task 4: Per-Turn Eino Skill and Read-Only Filesystem Handlers

**Files:**
- Modify: `internal/agent/runtime.go`
- Create: `internal/agent/runtime_test.go`
- Modify: `internal/service/chat/run.go`
- Modify: call sites that construct `RuntimeFactory`

**Interfaces:**
- Changes: `agent.NewRuntimeFactory(*Registry, *workspace.Manager) *RuntimeFactory`
- Changes: `(*RuntimeFactory).Build(context.Context, uint64, ModelSpec) (*adk.TypedRunner[*schema.AgenticMessage], error)`
- Produces internally: `(*RuntimeFactory).buildSkillHandlers(context.Context, uint64) ([]adk.TypedChatModelAgentMiddleware[*schema.AgenticMessage], error)`

- [ ] **Step 1: Write failing handler composition tests**

  Build an isolated manager with builtin and user manifests. Call `buildSkillHandlers`, apply each handler's `BeforeAgent` to an `adk.ChatModelAgentContext`, inspect every `tool.Info(ctx).Name`, and assert the exact sorted names:

  ```text
  glob, grep, ls, read_file, skill
  ```

  Assert `write_file` and `edit_file` are absent and the skill tool description contains both available skill names.

- [ ] **Step 2: Write next-Turn catalog test**

  Build handlers, inspect catalog, publish a new user skill through Store, build handlers again and inspect the new catalog, delete it, then build a third time and assert it is absent.

- [ ] **Step 3: Run Task 4 RED**

  Run: `go test ./internal/agent -run 'TestRuntimeSkillHandlers|TestRuntimeSkillsReloadOnNextBuild' -v`

  Expected: FAIL because RuntimeFactory has no workspace dependency or handler builder.

- [ ] **Step 4: Implement scoped handlers**

  Construct:

  ```go
  skillBackend, err := skill.NewBackendFromFilesystem(ctx, &skill.BackendFromFilesystemConfig{
      Backend: backend,
      BaseDir: "/",
  })
  ```

  Then construct `skill.NewTyped[*schema.AgenticMessage]` and `filesystem.NewTyped[*schema.AgenticMessage]`. Set `WriteFileToolConfig` and `EditFileToolConfig` to disabled. The typed filesystem handler does not configure legacy large-result offloading; pass both handlers to `deep.TypedConfig.Handlers` and leave `deep.TypedConfig.Backend` nil.

- [ ] **Step 5: Thread authenticated user ID into Runner construction**

  Change `RunService.Prepare` from `s.runtime.Build(ctx, spec)` to `s.runtime.Build(ctx, userID, spec)`. Preserve Runner construction outside the GORM transaction.

- [ ] **Step 6: Verify Task 4**

  Run: `gofmt -w internal/agent/runtime.go internal/agent/runtime_test.go internal/service/chat/run.go && go test -race ./internal/agent ./internal/service/chat -v`

  Expected: PASS.

---

### Task 5: Authenticated Skill HTTP API and Stable Errors

**Files:**
- Modify: `internal/platform/errorcode/error.go`
- Create: `internal/controller/skill.go`
- Create: `internal/controller/skill_test.go`
- Modify: `internal/controller/init.go`
- Modify: `internal/server/router.go`
- Create or modify: `internal/server/router_test.go`

**Interfaces:**
- Produces: `errorcode.PayloadTooLarge` with HTTP 413
- Produces: `controller.NewSkillController(*store.Store) *SkillController`
- Changes: `server.NewRouter(..., skillCtl *controller.SkillController, issuer *auth.Issuer)`

- [ ] **Step 1: Write failing controller and route tests**

  With a real temporary Store and Gin test context, cover successful multipart upload, list, delete, invalid archive, conflict, builtin delete, missing target, and oversized archive. Assert unauthenticated requests to all three routes return 401.

- [ ] **Step 2: Run Task 5 RED**

  Run: `go test ./internal/controller ./internal/server -run 'TestSkill|TestSkills' -v`

  Expected: FAIL because the controller and routes do not exist.

- [ ] **Step 3: Add stable error mapping**

  Add a distinct 413 code and map Store sentinels as follows:

  ```text
  ErrInvalidSkill       -> InvalidParameter (400)
  ErrArchiveTooLarge    -> PayloadTooLarge (413)
  ErrConflict           -> Conflict (409)
  ErrBuiltinReadOnly    -> Forbidden (403)
  ErrNotFound           -> NotFound (404)
  all other errors      -> Internal (500)
  ```

- [ ] **Step 4: Implement the thin controller**

  `Upload` reads `skill_id`, opens multipart field `file`, delegates all validation to Store, and returns 201 with `SkillInfo`. `List` returns 200. `Delete` returns 200 with the deleted ID. Every method obtains the user only through `middleware.UserID(c)`.

- [ ] **Step 5: Register authenticated routes**

  Add the three routes only inside the existing `authorized` Gin group and extend `AllControllers` with `Skill *SkillController`.

- [ ] **Step 6: Verify Task 5**

  Run: `gofmt -w internal/platform/errorcode/error.go internal/controller/*.go internal/server/*.go && go test ./internal/controller ./internal/server -v`

  Expected: PASS.

---

### Task 6: Application Bootstrap and Wire Composition

**Files:**
- Modify: `internal/app/app.go`
- Modify: `internal/wire/wire.go`
- Regenerate: `internal/wire/wire_gen.go`
- Modify: constructor call sites and their tests

**Interfaces:**
- Changes: `wire.InitControllers(..., runtime *agent.RuntimeFactory, skills *store.Store) *controller.AllControllers`

- [ ] **Step 1: Add compile-time bootstrap expectations**

  Update constructor call sites only after tests from Tasks 1-5 are green, then run the full repository to capture compilation failures caused by the intentionally changed RuntimeFactory, Wire, and router signatures.

- [ ] **Step 2: Run bootstrap RED**

  Run: `go test ./...`

  Expected: FAIL at old constructor signatures until bootstrap and generated Wire code are updated.

- [ ] **Step 3: Implement startup ordering**

  In `app.Run`, after logger initialization and before starting HTTP:

  1. create `workspace.NewManager(cfg.Skills)`;
  2. create `skillsync.New(cfg.Skills)` and call `Sync(ctx)`;
  3. create `store.New(cfg.Skills, manager)` and call `CleanupStaging(ctx)`;
  4. construct `agent.NewRuntimeFactory(agent.NewRegistry(), manager)`;
  5. pass Store through Wire to SkillController and Router.

  Any failure logs and aborts startup. Do not register a skill cron job.

- [ ] **Step 4: Regenerate Wire**

  Run: `go generate ./internal/wire`

  Expected: `internal/wire/wire_gen.go` contains the new Store parameter and SkillController construction, with no unrelated generated changes.

- [ ] **Step 5: Verify Task 6**

  Run: `gofmt -w internal/app/app.go internal/wire/wire.go && go test ./...`

  Expected: PASS.

---

### Task 7: Documentation, Hot-Reload Acceptance, and Final Verification

**Files:**
- Modify: `README.md`
- Modify: `CONTEXT.md` if its runtime dependency map needs the new Skill boundary
- Modify: design or plan only if implementation reveals a necessary factual correction

**Interfaces:**
- Documents: active Skill configuration, API examples, read-only runtime behavior, startup sync, and next-Turn reload semantics

- [ ] **Step 1: Add deterministic end-to-end hot-reload coverage**

  Ensure one test performs this exact sequence without a real model:

  ```text
  Build catalog for user 77 -> builtin only
  Upload user skill         -> next Build contains builtin + user
  Delete user skill         -> next Build contains builtin only
  ```

  The assertion must inspect the real Eino skill tool description or real Eino Skill Backend list, not an application-owned cache substitute.

- [ ] **Step 2: Document operator and API behavior**

  Add the five active YAML fields, curl-shaped request forms for POST/GET/DELETE, the no-overwrite rule, inline-only manifests, and the exact guarantee: completed changes are visible on the next Turn in the same Session; an active Turn is unspecified.

- [ ] **Step 3: Run focused race verification**

  Run: `go test -race ./internal/skill/... ./internal/agent`

  Expected: PASS with no race reports.

- [ ] **Step 4: Run complete verification**

  Run: `go test ./... && go vet ./... && git diff --check`

  Expected: all commands exit 0 and `git diff --check` prints no errors.

- [ ] **Step 5: Inspect final working tree**

  Run: `git status --short && git diff --stat`

  Expected: only task-related unstaged/untracked files; no staged changes and no commits created by Codex.
