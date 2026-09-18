---
name: playing-maze
description: Use when the user wants to create, list, inspect, walk, or play a text maze of 墙 路 起 终.
---
# Playing Maze

1. Read `references/tools.md` through `read_skill_resource` for the parameters of the requested maze operation.
2. List or inspect the requested maze before proposing a route. Use native maze tools when registered; otherwise invoke their name and arguments through `call_tool`.
3. Verify a proposed route with `runmaze`. Report success only when its result says the endpoint was reached.

Acceptance: name the maze and report the tool's verified outcome. Creating a maze is a write and must not be retried automatically after an uncertain failure. Only use capabilities present in the current role's registered tools.
