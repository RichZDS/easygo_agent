package maze

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/google/uuid"
)

const DefaultDir = "data/maze"

// Store persists mazes as one JSON file per id under a directory.
type Store struct {
	dir string
}

func NewStore(dir string) *Store {
	return &Store{dir: dir}
}

func (s *Store) Make(name, describe string, grid [][]string) (Record, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Record{}, errors.New("maze name cannot be empty")
	}
	clone := copyGrid(grid)
	if _, err := validate(clone); err != nil {
		return Record{}, err
	}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return Record{}, err
	}
	record := Record{ID: uuid.NewString(), Name: name, Describe: strings.TrimSpace(describe), Grid: clone}
	if err := s.write(record); err != nil {
		return Record{}, err
	}
	return record, nil
}

func (s *Store) List() ([]Summary, error) {
	records, err := s.loadAll()
	if err != nil {
		return nil, err
	}
	out := make([]Summary, 0, len(records))
	for _, record := range records {
		out = append(out, Summary{ID: record.ID, Name: record.Name, Describe: record.Describe})
	}
	return out, nil
}

func (s *Store) Detail(id string) (Record, error) {
	return s.read(id)
}

func (s *Store) Run(id string, moves []int) (RunResult, error) {
	record, err := s.read(id)
	if err != nil {
		return RunResult{}, err
	}
	return run(record.Grid, moves)
}

func (s *Store) read(id string) (Record, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return Record{}, errors.New("maze id cannot be empty")
	}
	data, err := os.ReadFile(s.path(id))
	if err != nil {
		if os.IsNotExist(err) {
			return Record{}, fmt.Errorf("maze %q not found", id)
		}
		return Record{}, err
	}
	var record Record
	if err = json.Unmarshal(data, &record); err != nil {
		return Record{}, err
	}
	return record, nil
}

func (s *Store) write(record Record) error {
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path(record.ID), data, 0o644)
}

func (s *Store) path(id string) string {
	return filepath.Join(s.dir, id+".json")
}

func (s *Store) loadAll() ([]Record, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var records []Record
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".json")
		record, readErr := s.read(id)
		if readErr != nil {
			return nil, readErr
		}
		records = append(records, record)
	}
	sort.Slice(records, func(i, j int) bool {
		if records[i].Name == records[j].Name {
			return records[i].ID < records[j].ID
		}
		return records[i].Name < records[j].Name
	})
	return records, nil
}
