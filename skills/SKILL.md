# Skill Catalog

Use the Directory descriptions to select procedures. Load every explicitly requested `$skill-name` with `load_skill(name)`; combine relevant skills when the task needs several procedures. For implicit selection, load only descriptions that match the user's request or a tool failure.

Follow the loaded instructions and acceptance criteria. Read linked reference files through `read_skill_resource(name, path)` when their stated condition applies. Use `list_skills(query)` to rediscover the full directory after compression; an empty query returns all entries.

When skills conflict, preserve the user's explicit requirements and explain any remaining conflict. If no skill applies, answer normally without loading one. A failed load is an error to report, not permission to invent its instructions.
