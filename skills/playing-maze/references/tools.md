# Maze tool parameters

| Tool | Arguments | Result |
| --- | --- | --- |
| listmaze | `{}` | Maze IDs, names and descriptions |
| detailmaze | `{"id":"..."}` | Full grid |
| makemaze | `{"name":"...","describe":"...","grid":[["墙","起"],["路","终"]]}` | Created maze |
| runmaze | `{"id":"...","moves":[2,4]}` or `{"id":"...","path":"2 4"}` | Route outcome |

Moves: 1 up, 2 down, 3 left, 4 right. A maze has exactly one 起 and one 终. 墙 blocks movement; 路 is walkable.

For hidden tools, wrap the arguments in `call_tool({"name":"runmaze","arguments":{"id":"...","moves":[2,4]}})`.
