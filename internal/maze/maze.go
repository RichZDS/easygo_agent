// Package maze is a text maze. Each cell is one Chinese entity: 墙, 路, 起, 终.
// 人 is a runtime overlay for the runner, not a stored tile.
package maze

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	Wall  = "墙"
	Path  = "路"
	Start = "起"
	Goal  = "终"
	Man   = "人"

	MoveUp    = 1
	MoveDown  = 2
	MoveLeft  = 3
	MoveRight = 4
)

// Record is the durable maze. Grid is stored as a 2D array of entity glyphs.
type Record struct {
	ID       string     `json:"id"`
	Name     string     `json:"name"`
	Describe string     `json:"describe"`
	Grid     [][]string `json:"grid"`
}

// Summary is the list view: identity only, no grid.
type Summary struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Describe string `json:"describe"`
}

// RunResult is one walk. Grid is the maze with 人 on the last cell.
type RunResult struct {
	Reached  bool     `json:"reached"`
	Stopped  string   `json:"stopped"`
	Steps    int      `json:"steps"`
	Reason   string   `json:"reason,omitempty"`
	Trace    []string `json:"trace"`
	Position [2]int   `json:"position"`
	Grid     string   `json:"grid"`
}

func validate(grid [][]string) (start [2]int, err error) {
	if len(grid) == 0 {
		return start, errors.New("maze grid cannot be empty")
	}
	width := len(grid[0])
	if width == 0 {
		return start, errors.New("maze row cannot be empty")
	}
	starts, goals := 0, 0
	for r, row := range grid {
		if len(row) != width {
			return start, errors.New("maze rows must have equal length")
		}
		for c, cell := range row {
			glyph := normalize(cell)
			grid[r][c] = glyph
			switch glyph {
			case Wall, Path:
			case Start:
				starts++
				start = [2]int{r, c}
			case Goal:
				goals++
			default:
				return start, fmt.Errorf("unknown maze entity %q", cell)
			}
		}
	}
	if starts != 1 || goals != 1 {
		return start, errors.New("maze must contain exactly one 起 and one 终")
	}
	return start, nil
}

func normalize(cell string) string {
	cell = strings.TrimSpace(cell)
	if cell == "" || cell == "." || cell == "空" {
		return Path
	}
	if utf8.RuneCountInString(cell) == 0 {
		return Path
	}
	return cell
}

func run(grid [][]string, moves []int) (RunResult, error) {
	clone := copyGrid(grid)
	start, err := validate(clone)
	if err != nil {
		return RunResult{}, err
	}
	row, col := start[0], start[1]
	result := RunResult{Trace: []string{Start}, Position: start, Stopped: Start}
	deltas := map[int][2]int{
		MoveUp:    {-1, 0},
		MoveDown:  {1, 0},
		MoveLeft:  {0, -1},
		MoveRight: {0, 1},
	}
	for i, move := range moves {
		delta, ok := deltas[move]
		if !ok {
			result.Reason = fmt.Sprintf("unknown move %d at step %d; use 1上 2下 3左 4右", move, i+1)
			result.Grid = render(clone, row, col)
			return result, nil
		}
		nextRow, nextCol := row+delta[0], col+delta[1]
		if nextRow < 0 || nextRow >= len(clone) || nextCol < 0 || nextCol >= len(clone[0]) {
			result.Reason = fmt.Sprintf("step %d walked off the maze", i+1)
			result.Grid = render(clone, row, col)
			return result, nil
		}
		cell := clone[nextRow][nextCol]
		if cell == Wall {
			result.Reason = fmt.Sprintf("step %d hit 墙", i+1)
			result.Grid = render(clone, row, col)
			return result, nil
		}
		row, col = nextRow, nextCol
		result.Steps++
		result.Position = [2]int{row, col}
		result.Stopped = cell
		result.Trace = append(result.Trace, cell)
		if cell == Goal {
			result.Reached = true
			result.Grid = render(clone, row, col)
			return result, nil
		}
	}
	if result.Stopped != Goal {
		result.Reason = "instructions ended before 终"
	} else {
		result.Reached = true
	}
	result.Grid = render(clone, row, col)
	return result, nil
}

func copyGrid(grid [][]string) [][]string {
	out := make([][]string, len(grid))
	for i := range grid {
		out[i] = append([]string{}, grid[i]...)
	}
	return out
}

func render(grid [][]string, row, col int) string {
	var b strings.Builder
	for r, line := range grid {
		for c, cell := range line {
			if r == row && c == col {
				b.WriteString(Man)
				continue
			}
			b.WriteString(cell)
		}
		if r+1 < len(grid) {
			b.WriteByte('\n')
		}
	}
	return b.String()
}
