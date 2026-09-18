# Continuing work

- `queued` and `running`: query progress; they reject a concurrent resume.
- `completed`, `failed`, `blocked`, or `canceled`: `resume_task` keeps the task ID, retains history, and opens a new version with a fresh step/time budget. Include the user's new constraints in `instructions`.
- An uncertain tool call requires a decision keyed by its call ID. Use `{"action":"retry"}` only after the user explicitly chooses retry; use `{"action":"result","result":"verified raw result"}` only when execution was independently verified. Preserve the exact call ID.
- A changed checkpoint format, prompt, skill, or tool configuration blocks automatic recovery. Restore the recorded compatible configuration before continuing.

Example: if call `write-3` timed out and the user verified the operation succeeded, supply `decisions: {"write-3":{"action":"result","result":"{\"ok\":true}"}}`. Do not invent the verified result.
