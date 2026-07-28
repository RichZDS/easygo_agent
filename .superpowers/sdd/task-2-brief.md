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

