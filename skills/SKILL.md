---
name: skill-catalog
description: Use when deciding whether a specialized skill applies before answering, using tools, or changing procedure.
---

# Skill Catalog

This is the only skill registered at startup. Specialized procedures stay on disk.

## Required sequence

1. Read Directory below.
2. If a row's "Use when" matches the current user request, call `load_skill` with that exact Name before any other tool and before answering.
3. Follow the loaded skill. If no row matches, answer normally and do not call `load_skill`.
4. Load at most one skill per user request unless a loaded skill explicitly requires another.

## When writing a new skill

Add `skills/<name>/SKILL.md` with YAML `name` and a "Use when..." `description`. The Directory is rebuilt from those descriptions at process start. Put the triggering condition in the description; put the procedure in the skill body.
