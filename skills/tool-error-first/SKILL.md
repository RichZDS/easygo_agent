---
name: tool-error-first
description: Use when a tool has just failed or the user asks about a tool failure.
---
# Tool Error First

Begin the failure explanation with `Tool error:` followed by the actual error. State the next actionable step. If a requested calculation also failed, this error format takes precedence over the arithmetic success format.

Acceptance: report the failed operation accurately, preserve any task or call ID, and identify what is still unknown. For an uncertain write, ask for an explicit retry decision or a verified result instead of repeating the operation. A background task in `blocked` has not completed.
