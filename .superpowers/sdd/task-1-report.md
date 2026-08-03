# Task 1 Report: Skills Configuration and Workspace-Sandboxed Backend

## Status

DONE

No commits were created, no files were staged, and no branch or remote operations were performed.

## Implementation Summary

- Added `config.Skills` with the exact YAML fields and defaults from the Task 1 brief.
- Applied defaults before strict YAML decoding and validation, so omitted values receive defaults while explicit empty, zero, or negative values remain invalid.
- Added the skills configuration block to `configs/config.yaml`.
- Promoted `github.com/bmatcuk/doublestar/v4` from an indirect to a direct dependency.
- Added a workspace `Manager` that:
  - Stores cleaned absolute paths for the skills root, `builtin-src`, `builtin`, and `workspaces`.
  - Creates the directory layout and per-user directories with mode `0750`.
  - Enumerates immediate builtin directories.
  - Creates exact relative builtin links using `../../builtin/{skillID}`.
  - Preserves correct links, repairs broken/wrong links, and rejects user-owned files or directories at builtin IDs.
- Added a read-only Eino filesystem `Backend` that:
  - Normalizes model paths to a virtual absolute root.
  - Rejects `..` traversal.
  - Resolves existing symlinks and allows resolved targets only under the selected workspace or configured builtin root.
  - Requires access into the builtin root to pass through the exact manager-owned top-level link.
  - Supports deterministic virtual-path `LsInfo`, line-paginated `Read`, regexp/context `GrepRaw`, and doublestar `GlobInfo`.
  - Explicitly traverses allowed builtin directory links during discovery.
  - Returns the `ErrReadOnly` sentinel from `Write` and `Edit`.
- Added structured zap error logging at every new or modified returned-error path, without logging secrets.
- Added immediate purpose comments for every new function, method, test function, and function literal.

## TDD Evidence

### Configuration RED

Command:

```text
go test ./internal/config -run 'TestLoadSkillsDefaults|TestSkillsValidate' -v
```

Exact output:

```text
# easygo-agent/internal/config [easygo-agent/internal/config.test]
internal/config/config_test.go:19:9: cfg.Skills undefined (type Config has no field or method Skills)
internal/config/config_test.go:20:48: cfg.Skills undefined (type Config has no field or method Skills)
internal/config/config_test.go:22:9: cfg.Skills undefined (type Config has no field or method Skills)
internal/config/config_test.go:23:49: cfg.Skills undefined (type Config has no field or method Skills)
internal/config/config_test.go:25:9: cfg.Skills undefined (type Config has no field or method Skills)
internal/config/config_test.go:26:50: cfg.Skills undefined (type Config has no field or method Skills)
internal/config/config_test.go:28:9: cfg.Skills undefined (type Config has no field or method Skills)
internal/config/config_test.go:29:52: cfg.Skills undefined (type Config has no field or method Skills)
internal/config/config_test.go:31:9: cfg.Skills undefined (type Config has no field or method Skills)
internal/config/config_test.go:32:58: cfg.Skills undefined (type Config has no field or method Skills)
internal/config/config_test.go:32:58: too many errors
FAIL	easygo-agent/internal/config [build failed]
FAIL
```

Exit code: `1`.

This was the expected RED because the tests referenced the required `Config.Skills` API before it existed.

### Workspace/Backend RED

Command:

```text
go test ./internal/skill/workspace -v
```

Exact output:

```text
# easygo-agent/internal/skill/workspace [easygo-agent/internal/skill/workspace.test]
internal/skill/workspace/manager_test.go:164:37: undefined: Manager
internal/skill/workspace/manager_test.go:168:18: undefined: NewManager
internal/skill/workspace/backend_test.go:183:26: undefined: ErrReadOnly
internal/skill/workspace/backend_test.go:187:25: undefined: ErrReadOnly
FAIL	easygo-agent/internal/skill/workspace [build failed]
FAIL
```

Exit code: `1`.

This was the expected RED because the tests established the missing manager/backend API before production files existed.

## Required GREEN Verification

Command:

```text
go test ./internal/config ./internal/skill/workspace -v
```

Exact output:

```text
=== RUN   TestLoadSkillsDefaults
--- PASS: TestLoadSkillsDefaults (0.00s)
=== RUN   TestSkillsValidate
--- PASS: TestSkillsValidate (0.00s)
PASS
ok  	easygo-agent/internal/config	0.689s
=== RUN   TestBackendReadIsUserScoped
--- PASS: TestBackendReadIsUserScoped (0.00s)
=== RUN   TestBackendReadFollowsManagedBuiltinLink
--- PASS: TestBackendReadFollowsManagedBuiltinLink (0.00s)
=== RUN   TestBackendRejectsUnsafePaths
--- PASS: TestBackendRejectsUnsafePaths (0.00s)
=== RUN   TestBackendGlobFindsUserAndBuiltinSkills
--- PASS: TestBackendGlobFindsUserAndBuiltinSkills (0.00s)
=== RUN   TestBackendReturnsVirtualPaths
--- PASS: TestBackendReturnsVirtualPaths (0.00s)
=== RUN   TestBackendReadPaginatesLines
--- PASS: TestBackendReadPaginatesLines (0.00s)
=== RUN   TestBackendIsReadOnly
--- PASS: TestBackendIsReadOnly (0.00s)
=== RUN   TestEnsureWorkspaceCreatesUserDirectory
--- PASS: TestEnsureWorkspaceCreatesUserDirectory (0.00s)
=== RUN   TestEnsureWorkspaceCreatesRelativeBuiltinLinks
--- PASS: TestEnsureWorkspaceCreatesRelativeBuiltinLinks (0.00s)
=== RUN   TestEnsureWorkspaceIsIdempotent
--- PASS: TestEnsureWorkspaceIsIdempotent (0.00s)
=== RUN   TestEnsureWorkspaceRepairsManagedSymlink
--- PASS: TestEnsureWorkspaceRepairsManagedSymlink (0.00s)
=== RUN   TestEnsureWorkspaceRejectsBuiltinDirectoryConflict
--- PASS: TestEnsureWorkspaceRejectsBuiltinDirectoryConflict (0.00s)
=== RUN   TestBuiltinIDsReturnsImmediateDirectories
--- PASS: TestBuiltinIDsReturnsImmediateDirectories (0.00s)
PASS
ok  	easygo-agent/internal/skill/workspace	0.851s
```

Exit code: `0`. All 15 Task 1 tests passed.

## Additional Verification

### Full repository

Command:

```text
go test ./...
```

Result: exit code `0`; every repository package passed or reported no test files.

### Static analysis and race detector

Command:

```text
go vet ./internal/config ./internal/skill/workspace && go test -race ./internal/config ./internal/skill/workspace
```

Exact output:

```text
ok  	easygo-agent/internal/config	1.319s
ok  	easygo-agent/internal/skill/workspace	1.469s
```

Exit code: `0`.

### Diff hygiene

Command:

```text
git diff --check
```

Result: exit code `0`, with no whitespace errors.

## Files Changed

- `go.mod`
- `configs/config.yaml`
- `internal/config/config.go`
- `internal/config/config_test.go`
- `internal/skill/workspace/manager.go`
- `internal/skill/workspace/backend.go`
- `internal/skill/workspace/manager_test.go`
- `internal/skill/workspace/backend_test.go`
- `.superpowers/sdd/task-1-report.md`

Unrelated existing changes in `internal/framejob/register.go`, `docs/superpowers`, and `easygo_agent_frontend` were not modified.

## Self-Review

- Confirmed the implementation matches every Task 1 interface and checklist item; no later-task code was added.
- Confirmed there is no raw SQL, database access, Redis access, or SQL-in-loop behavior.
- Confirmed all new and modified returned errors are logged with the project logger and typed zap fields.
- Confirmed no passwords, tokens, file contents, or credentials are logged.
- Confirmed every new function, method, test, and anonymous comparator has an immediate purpose comment.
- Confirmed paths exposed by list, grep, and glob operations are virtual, absolute, slash-separated, and deterministically sorted.
- Confirmed traversal, physical absolute paths, cross-user paths, and symlinks outside the two allowed physical roots fail.
- Confirmed shared builtin access additionally requires the exact manager-owned relative link.
- Confirmed correct symlinks are not replaced and user-owned content is never deleted.
- Confirmed the Eino backend interface assertion compiles.
- Confirmed all edits remain unstaged in the working tree and no commits exist for this task.

## Concerns

None known within Task 1 scope.

## Fix Round: Workspace Symlink Hardening and Builtin ID Validation

### Review Findings Addressed

1. Existing managed workspace directories are no longer allowed to be symlinks. `EnsureWorkspace` validates the configured skills root and `workspaces` directory with `Lstat`, then separately inspects the per-user workspace before creating or using it. A per-user path that aliases another user's directory is rejected.
2. Backend path resolution now inspects every existing virtual path component with `Lstat`. The only permitted symlink is the first virtual component when it is the exact manager-owned `../../builtin/{skillID}` link. Arbitrary leaf links, directory links, and nested links are rejected even if their final target remains inside the same user workspace.
3. `BuiltinIDs` now fails safely unless every immediate builtin directory name matches the exact regex `^[a-z0-9-]{2,64}$`. Invalid directories are rejected before any ID map or user link can be returned.

### Fix Round TDD RED: Workspace and Internal Symlinks

Focused command:

```text
go test ./internal/skill/workspace -run 'TestEnsureWorkspaceRejectsUserDirectorySymlink|TestBackendRejectsArbitraryInternalSymlinks' -v
```

Exact output:

```text
=== RUN   TestBackendRejectsArbitraryInternalSymlinks
    backend_test.go:107: Read("/leaf-link.md") error = nil, want arbitrary symlink rejection
    backend_test.go:107: Read("/directory-link/SKILL.md") error = nil, want arbitrary symlink rejection
--- FAIL: TestBackendRejectsArbitraryInternalSymlinks (0.00s)
=== RUN   TestEnsureWorkspaceRejectsUserDirectorySymlink
    manager_test.go:152: EnsureWorkspace() error = nil, want workspace symlink rejection
--- FAIL: TestEnsureWorkspaceRejectsUserDirectorySymlink (0.00s)
FAIL
FAIL	easygo-agent/internal/skill/workspace	0.845s
FAIL
```

Exit code: `1`.

This was the expected RED: the previous implementation followed both arbitrary in-workspace symlinks and accepted a user workspace root that resolved to another user's directory.

### Fix Round TDD RED: Invalid Builtin IDs

Focused command:

```text
go test ./internal/skill/workspace -run 'TestBuiltinIDsRejectsInvalidNames' -v
```

Exact output:

```text
=== RUN   TestBuiltinIDsRejectsInvalidNames
    manager_test.go:194: BuiltinIDs() with "Uppercase" error = nil, want invalid ID error
    manager_test.go:194: BuiltinIDs() with "a" error = nil, want invalid ID error
    manager_test.go:194: BuiltinIDs() with "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" error = nil, want invalid ID error
    manager_test.go:194: BuiltinIDs() with "under_score" error = nil, want invalid ID error
    manager_test.go:194: BuiltinIDs() with "dotted.name" error = nil, want invalid ID error
--- FAIL: TestBuiltinIDsRejectsInvalidNames (0.01s)
FAIL
FAIL	easygo-agent/internal/skill/workspace	0.234s
FAIL
```

Exit code: `1`.

This was the expected RED: the previous implementation treated every immediate directory name as a valid builtin skill ID.

### Fix Round Focused GREEN

Command:

```text
go test ./internal/skill/workspace -run 'TestEnsureWorkspaceRejectsUserDirectorySymlink|TestBackendRejectsArbitraryInternalSymlinks|TestBuiltinIDsRejectsInvalidNames' -v
```

Exact output:

```text
=== RUN   TestBackendRejectsArbitraryInternalSymlinks
--- PASS: TestBackendRejectsArbitraryInternalSymlinks (0.00s)
=== RUN   TestEnsureWorkspaceRejectsUserDirectorySymlink
--- PASS: TestEnsureWorkspaceRejectsUserDirectorySymlink (0.00s)
=== RUN   TestBuiltinIDsRejectsInvalidNames
--- PASS: TestBuiltinIDsRejectsInvalidNames (0.01s)
PASS
ok  	easygo-agent/internal/skill/workspace	0.855s
```

Exit code: `0`.

### Fix Round Required Workspace GREEN

Command:

```text
go test ./internal/skill/workspace -v
```

Exact output:

```text
=== RUN   TestBackendReadIsUserScoped
--- PASS: TestBackendReadIsUserScoped (0.00s)
=== RUN   TestBackendReadFollowsManagedBuiltinLink
--- PASS: TestBackendReadFollowsManagedBuiltinLink (0.00s)
=== RUN   TestBackendRejectsUnsafePaths
--- PASS: TestBackendRejectsUnsafePaths (0.00s)
=== RUN   TestBackendRejectsArbitraryInternalSymlinks
--- PASS: TestBackendRejectsArbitraryInternalSymlinks (0.00s)
=== RUN   TestBackendGlobFindsUserAndBuiltinSkills
--- PASS: TestBackendGlobFindsUserAndBuiltinSkills (0.00s)
=== RUN   TestBackendReturnsVirtualPaths
--- PASS: TestBackendReturnsVirtualPaths (0.00s)
=== RUN   TestBackendReadPaginatesLines
--- PASS: TestBackendReadPaginatesLines (0.00s)
=== RUN   TestBackendIsReadOnly
--- PASS: TestBackendIsReadOnly (0.00s)
=== RUN   TestEnsureWorkspaceCreatesUserDirectory
--- PASS: TestEnsureWorkspaceCreatesUserDirectory (0.00s)
=== RUN   TestEnsureWorkspaceCreatesRelativeBuiltinLinks
--- PASS: TestEnsureWorkspaceCreatesRelativeBuiltinLinks (0.00s)
=== RUN   TestEnsureWorkspaceIsIdempotent
--- PASS: TestEnsureWorkspaceIsIdempotent (0.00s)
=== RUN   TestEnsureWorkspaceRepairsManagedSymlink
--- PASS: TestEnsureWorkspaceRepairsManagedSymlink (0.00s)
=== RUN   TestEnsureWorkspaceRejectsBuiltinDirectoryConflict
--- PASS: TestEnsureWorkspaceRejectsBuiltinDirectoryConflict (0.00s)
=== RUN   TestEnsureWorkspaceRejectsUserDirectorySymlink
--- PASS: TestEnsureWorkspaceRejectsUserDirectorySymlink (0.00s)
=== RUN   TestBuiltinIDsReturnsImmediateDirectories
--- PASS: TestBuiltinIDsReturnsImmediateDirectories (0.00s)
=== RUN   TestBuiltinIDsRejectsInvalidNames
--- PASS: TestBuiltinIDsRejectsInvalidNames (0.01s)
PASS
ok  	easygo-agent/internal/skill/workspace	0.247s
```

Exit code: `0`. All 16 workspace/backend tests passed.

### Fix Round Required Cross-Package GREEN

Command:

```text
go test ./internal/config ./internal/skill/workspace -v
```

Exact output:

```text
=== RUN   TestLoadSkillsDefaults
--- PASS: TestLoadSkillsDefaults (0.00s)
=== RUN   TestSkillsValidate
--- PASS: TestSkillsValidate (0.00s)
PASS
ok  	easygo-agent/internal/config	(cached)
=== RUN   TestBackendReadIsUserScoped
--- PASS: TestBackendReadIsUserScoped (0.00s)
=== RUN   TestBackendReadFollowsManagedBuiltinLink
--- PASS: TestBackendReadFollowsManagedBuiltinLink (0.00s)
=== RUN   TestBackendRejectsUnsafePaths
--- PASS: TestBackendRejectsUnsafePaths (0.00s)
=== RUN   TestBackendRejectsArbitraryInternalSymlinks
--- PASS: TestBackendRejectsArbitraryInternalSymlinks (0.00s)
=== RUN   TestBackendGlobFindsUserAndBuiltinSkills
--- PASS: TestBackendGlobFindsUserAndBuiltinSkills (0.00s)
=== RUN   TestBackendReturnsVirtualPaths
--- PASS: TestBackendReturnsVirtualPaths (0.00s)
=== RUN   TestBackendReadPaginatesLines
--- PASS: TestBackendReadPaginatesLines (0.00s)
=== RUN   TestBackendIsReadOnly
--- PASS: TestBackendIsReadOnly (0.00s)
=== RUN   TestEnsureWorkspaceCreatesUserDirectory
--- PASS: TestEnsureWorkspaceCreatesUserDirectory (0.00s)
=== RUN   TestEnsureWorkspaceCreatesRelativeBuiltinLinks
--- PASS: TestEnsureWorkspaceCreatesRelativeBuiltinLinks (0.00s)
=== RUN   TestEnsureWorkspaceIsIdempotent
--- PASS: TestEnsureWorkspaceIsIdempotent (0.00s)
=== RUN   TestEnsureWorkspaceRepairsManagedSymlink
--- PASS: TestEnsureWorkspaceRepairsManagedSymlink (0.00s)
=== RUN   TestEnsureWorkspaceRejectsBuiltinDirectoryConflict
--- PASS: TestEnsureWorkspaceRejectsBuiltinDirectoryConflict (0.00s)
=== RUN   TestEnsureWorkspaceRejectsUserDirectorySymlink
--- PASS: TestEnsureWorkspaceRejectsUserDirectorySymlink (0.00s)
=== RUN   TestBuiltinIDsReturnsImmediateDirectories
--- PASS: TestBuiltinIDsReturnsImmediateDirectories (0.00s)
=== RUN   TestBuiltinIDsRejectsInvalidNames
--- PASS: TestBuiltinIDsRejectsInvalidNames (0.01s)
PASS
ok  	easygo-agent/internal/skill/workspace	(cached)
```

Exit code: `0`.

### Fix Round Additional Verification

Command:

```text
go vet ./internal/config ./internal/skill/workspace && go test -race -count=1 ./internal/config ./internal/skill/workspace && go test -count=1 ./... && git diff --check
```

Exit code: `0`. Vet completed cleanly, both targeted packages passed under the race detector, the complete repository test suite passed without cache, and the diff has no whitespace errors.

### Fix Round Files Changed

- `internal/skill/workspace/manager.go`
- `internal/skill/workspace/backend.go`
- `internal/skill/workspace/manager_test.go`
- `internal/skill/workspace/backend_test.go`
- `.superpowers/sdd/task-1-report.md`

No unrelated file was modified, staged, or committed during the fix round.

### Fix Round Self-Review

- Confirmed `Lstat`, not `Stat`, is used before accepting the managed root, `workspaces`, or per-user workspace directory.
- Confirmed a pre-existing per-user symlink is rejected before builtin enumeration or link creation.
- Confirmed every model-facing read path checks every lexical component for symlinks before physical resolution.
- Confirmed the exact top-level managed builtin link remains usable and nested/arbitrary symlinks are rejected.
- Confirmed invalid builtin directories fail the entire enumeration before a partial ID map can be returned.
- Confirmed all new returned-error paths log typed zap fields and safe path/ID parameters.
- Confirmed every new function and test has an immediate purpose comment.
- Confirmed no raw SQL, Redis, database operations, commits, or staging were introduced.

### Fix Round Concerns

None known within the requested fix scope.
