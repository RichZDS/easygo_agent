---
name: playing-maze
description: Use when the user wants to create, list, inspect, walk, or play a text maze of 墙 路 起 终.
---

# Playing Maze

Maze tools are hidden. Do not invent maze APIs. After loading this skill, call `call_tool`.

## Hidden tools

- `listmaze` arguments `{}` — list id, name, describe
- `detailmaze` arguments `{"id":"..."}` — full grid
- `makemaze` arguments `{"name":"...","describe":"...","grid":[["墙","起"],["路","终"]]}` — create from a 2D array of 墙 路 起 终
- `runmaze` arguments `{"id":"...","moves":[2,4]}` or `{"id":"...","path":"2 4"}` — walk 人; 1上 2下 3左 4右; report whether 终 was reached

## Rules

Call `call_tool` with `name` set to one of the four tools above. Put that tool's JSON in `arguments`. One maze has exactly one 起 and one 终. 墙 blocks. 路 is walkable.
