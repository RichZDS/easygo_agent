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

