// Package store manages atomically published user skill directories.
package store

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"easygo-agent/internal/config"
	"easygo-agent/internal/platform/logger"
	"easygo-agent/internal/skill/manifest"
	"easygo-agent/internal/skill/workspace"
	"go.uber.org/zap"
)

const (
	// SourceBuiltin identifies a shared read-only skill.
	SourceBuiltin = "builtin"
	// SourceUser identifies a private user-managed skill.
	SourceUser = "user"
)

var (
	// ErrInvalidSkill indicates malformed identifiers, manifests, archives, or filesystem entries.
	ErrInvalidSkill = errors.New("invalid skill")
	// ErrArchiveTooLarge indicates a configured compressed, extracted, or file-count limit was exceeded.
	ErrArchiveTooLarge = errors.New("skill archive exceeds configured limits")
	// ErrConflict indicates a requested skill ID already exists.
	ErrConflict = errors.New("skill already exists")
	// ErrBuiltinReadOnly indicates a mutation targeted a shared builtin skill.
	ErrBuiltinReadOnly = errors.New("builtin skill is read-only")
	// ErrNotFound indicates a requested user skill does not exist.
	ErrNotFound = errors.New("skill not found")
)

// SkillInfo is the stable metadata returned by the skill management API.
type SkillInfo struct {
	SkillID     string `json:"skill_id"`
	Source      string `json:"source"`
	Readonly    bool   `json:"readonly"`
	Description string `json:"description"`
}

// Store owns validated user skill publication and deletion boundaries.
type Store struct {
	manager           *workspace.Manager
	stagingDir        string
	maxZipBytes       int64
	maxExtractedBytes int64
	maxFiles          int
	mutationMu        sync.Mutex
}

// New constructs a user skill store bound to one managed skills root.
func New(cfg config.Skills, manager *workspace.Manager) (*Store, error) {
	if manager == nil {
		err := fmt.Errorf("workspace manager is required")
		logger.Error("create skill store failed", zap.Error(err))
		return nil, err
	}
	if cfg.MaxZipBytes <= 0 || cfg.MaxExtractedBytes <= 0 || cfg.MaxFiles <= 0 {
		err := fmt.Errorf("skill archive limits must be greater than zero")
		logger.Error("create skill store failed", zap.Error(err))
		return nil, err
	}
	stagingDir, err := manager.StagingDir(context.Background())
	if err != nil {
		logger.Error("create skill store failed", zap.Error(err))
		return nil, err
	}
	return &Store{
		manager:           manager,
		stagingDir:        stagingDir,
		maxZipBytes:       cfg.MaxZipBytes,
		maxExtractedBytes: cfg.MaxExtractedBytes,
		maxFiles:          cfg.MaxFiles,
	}, nil
}

// Upload validates an archive and atomically publishes one private user skill.
func (s *Store) Upload(ctx context.Context, userID uint64, skillID string, archive io.Reader) (SkillInfo, error) {
	if err := manifest.ValidateID(skillID); err != nil {
		wrappedErr := fmt.Errorf("%w: %v", ErrInvalidSkill, err)
		logger.ErrorContext(ctx, "upload user skill failed", zap.Uint64("user_id", userID), zap.String("skill_id", skillID), zap.Error(wrappedErr))
		return SkillInfo{}, wrappedErr
	}
	if archive == nil {
		err := fmt.Errorf("%w: archive is required", ErrInvalidSkill)
		logger.ErrorContext(ctx, "upload user skill failed", zap.Uint64("user_id", userID), zap.String("skill_id", skillID), zap.Error(err))
		return SkillInfo{}, err
	}
	staged, err := s.stageArchive(ctx, archive)
	if err != nil {
		logger.ErrorContext(ctx, "upload user skill failed", zap.Uint64("user_id", userID), zap.String("skill_id", skillID), zap.Error(err))
		return SkillInfo{}, err
	}
	// cleanPublishedStage removes the old staging path after publication or any pre-publication failure.
	defer func() {
		if removeErr := os.RemoveAll(staged.directory); removeErr != nil {
			logger.ErrorContext(ctx, "clean staged user skill failed", zap.Uint64("user_id", userID), zap.String("skill_id", skillID), zap.Error(removeErr))
		}
	}()
	if staged.manifest.Name != skillID {
		err := fmt.Errorf("%w: manifest name %q does not match requested ID %q", ErrInvalidSkill, staged.manifest.Name, skillID)
		logger.ErrorContext(ctx, "upload user skill failed", zap.Uint64("user_id", userID), zap.String("skill_id", skillID), zap.Error(err))
		return SkillInfo{}, err
	}

	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	builtinIDs, err := s.manager.BuiltinIDs(ctx)
	if err != nil {
		logger.ErrorContext(ctx, "upload user skill failed", zap.Uint64("user_id", userID), zap.String("skill_id", skillID), zap.Error(err))
		return SkillInfo{}, err
	}
	if _, builtin := builtinIDs[skillID]; builtin {
		err := fmt.Errorf("%w: skill ID %q is builtin", ErrConflict, skillID)
		logger.ErrorContext(ctx, "upload user skill failed", zap.Uint64("user_id", userID), zap.String("skill_id", skillID), zap.Error(err))
		return SkillInfo{}, err
	}
	workspaceDir, err := s.manager.EnsureWorkspace(ctx, userID)
	if err != nil {
		logger.ErrorContext(ctx, "upload user skill failed", zap.Uint64("user_id", userID), zap.String("skill_id", skillID), zap.Error(err))
		return SkillInfo{}, err
	}
	target := filepath.Join(workspaceDir, skillID)
	if _, err := os.Lstat(target); err == nil {
		conflictErr := fmt.Errorf("%w: skill ID %q already exists", ErrConflict, skillID)
		logger.ErrorContext(ctx, "upload user skill failed", zap.Uint64("user_id", userID), zap.String("skill_id", skillID), zap.Error(conflictErr))
		return SkillInfo{}, conflictErr
	} else if !os.IsNotExist(err) {
		wrappedErr := fmt.Errorf("inspect user skill target: %w", err)
		logger.ErrorContext(ctx, "upload user skill failed", zap.Uint64("user_id", userID), zap.String("skill_id", skillID), zap.Error(wrappedErr))
		return SkillInfo{}, wrappedErr
	}
	if err := os.Rename(staged.directory, target); err != nil {
		wrappedErr := fmt.Errorf("publish user skill: %w", err)
		logger.ErrorContext(ctx, "upload user skill failed", zap.Uint64("user_id", userID), zap.String("skill_id", skillID), zap.Error(wrappedErr))
		return SkillInfo{}, wrappedErr
	}
	return SkillInfo{SkillID: skillID, Source: SourceUser, Readonly: false, Description: staged.manifest.Description}, nil
}

// List returns deterministic metadata for builtin and private skills visible to one user.
func (s *Store) List(ctx context.Context, userID uint64) ([]SkillInfo, error) {
	workspaceDir, err := s.manager.EnsureWorkspace(ctx, userID)
	if err != nil {
		logger.ErrorContext(ctx, "list user skills failed", zap.Uint64("user_id", userID), zap.Error(err))
		return nil, err
	}
	builtinIDs, err := s.manager.BuiltinIDs(ctx)
	if err != nil {
		logger.ErrorContext(ctx, "list user skills failed", zap.Uint64("user_id", userID), zap.Error(err))
		return nil, err
	}
	entries, err := os.ReadDir(workspaceDir)
	if err != nil {
		wrappedErr := fmt.Errorf("read user skill workspace: %w", err)
		logger.ErrorContext(ctx, "list user skills failed", zap.Uint64("user_id", userID), zap.Error(wrappedErr))
		return nil, wrappedErr
	}
	result := make([]SkillInfo, 0, len(entries))
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			logger.ErrorContext(ctx, "list user skills failed", zap.Uint64("user_id", userID), zap.Error(err))
			return nil, err
		}
		_, builtin := builtinIDs[entry.Name()]
		if builtin {
			info, err := os.Lstat(filepath.Join(workspaceDir, entry.Name()))
			if err != nil || info.Mode()&os.ModeSymlink == 0 {
				wrappedErr := fmt.Errorf("builtin workspace entry %q is not a managed link", entry.Name())
				logger.ErrorContext(ctx, "list user skills failed", zap.Uint64("user_id", userID), zap.String("skill_id", entry.Name()), zap.Error(wrappedErr))
				return nil, wrappedErr
			}
		} else if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			err := fmt.Errorf("%w: user skill entry %q is not a directory", ErrInvalidSkill, entry.Name())
			logger.ErrorContext(ctx, "list user skills failed", zap.Uint64("user_id", userID), zap.String("skill_id", entry.Name()), zap.Error(err))
			return nil, err
		}
		frontMatter, err := manifest.Read(ctx, filepath.Join(workspaceDir, entry.Name(), "SKILL.md"))
		if err != nil {
			logger.ErrorContext(ctx, "list user skills failed", zap.Uint64("user_id", userID), zap.String("skill_id", entry.Name()), zap.Error(err))
			return nil, err
		}
		if frontMatter.Name != entry.Name() {
			err := fmt.Errorf("%w: manifest name %q does not match directory %q", ErrInvalidSkill, frontMatter.Name, entry.Name())
			logger.ErrorContext(ctx, "list user skills failed", zap.Uint64("user_id", userID), zap.String("skill_id", entry.Name()), zap.Error(err))
			return nil, err
		}
		source := SourceUser
		if builtin {
			source = SourceBuiltin
		}
		result = append(result, SkillInfo{SkillID: entry.Name(), Source: source, Readonly: builtin, Description: frontMatter.Description})
	}
	sort.Slice(result,
		// orderSkillInfo sorts public skill metadata by stable identifier.
		func(i, j int) bool {
			return result[i].SkillID < result[j].SkillID
		},
	)
	return result, nil
}

// Delete atomically removes one private skill from discovery before cleaning its files.
func (s *Store) Delete(ctx context.Context, userID uint64, skillID string) error {
	if err := manifest.ValidateID(skillID); err != nil {
		wrappedErr := fmt.Errorf("%w: %v", ErrInvalidSkill, err)
		logger.ErrorContext(ctx, "delete user skill failed", zap.Uint64("user_id", userID), zap.String("skill_id", skillID), zap.Error(wrappedErr))
		return wrappedErr
	}
	s.mutationMu.Lock()
	builtinIDs, err := s.manager.BuiltinIDs(ctx)
	if err != nil {
		s.mutationMu.Unlock()
		logger.ErrorContext(ctx, "delete user skill failed", zap.Uint64("user_id", userID), zap.String("skill_id", skillID), zap.Error(err))
		return err
	}
	if _, builtin := builtinIDs[skillID]; builtin {
		s.mutationMu.Unlock()
		err := fmt.Errorf("%w: skill ID %q is builtin", ErrBuiltinReadOnly, skillID)
		logger.ErrorContext(ctx, "delete user skill failed", zap.Uint64("user_id", userID), zap.String("skill_id", skillID), zap.Error(err))
		return err
	}
	workspaceDir, err := s.manager.EnsureWorkspace(ctx, userID)
	if err != nil {
		s.mutationMu.Unlock()
		logger.ErrorContext(ctx, "delete user skill failed", zap.Uint64("user_id", userID), zap.String("skill_id", skillID), zap.Error(err))
		return err
	}
	target := filepath.Join(workspaceDir, skillID)
	info, err := os.Lstat(target)
	if os.IsNotExist(err) {
		s.mutationMu.Unlock()
		notFoundErr := fmt.Errorf("%w: skill ID %q", ErrNotFound, skillID)
		logger.ErrorContext(ctx, "delete user skill failed", zap.Uint64("user_id", userID), zap.String("skill_id", skillID), zap.Error(notFoundErr))
		return notFoundErr
	}
	if err != nil {
		s.mutationMu.Unlock()
		wrappedErr := fmt.Errorf("inspect user skill target: %w", err)
		logger.ErrorContext(ctx, "delete user skill failed", zap.Uint64("user_id", userID), zap.String("skill_id", skillID), zap.Error(wrappedErr))
		return wrappedErr
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		s.mutationMu.Unlock()
		invalidErr := fmt.Errorf("%w: user skill target is not a regular directory", ErrInvalidSkill)
		logger.ErrorContext(ctx, "delete user skill failed", zap.Uint64("user_id", userID), zap.String("skill_id", skillID), zap.Error(invalidErr))
		return invalidErr
	}
	detached, err := os.MkdirTemp(s.stagingDir, "skill-delete-")
	if err != nil {
		s.mutationMu.Unlock()
		wrappedErr := fmt.Errorf("reserve deleted skill staging path: %w", err)
		logger.ErrorContext(ctx, "delete user skill failed", zap.Uint64("user_id", userID), zap.String("skill_id", skillID), zap.Error(wrappedErr))
		return wrappedErr
	}
	if err := os.Remove(detached); err != nil {
		s.mutationMu.Unlock()
		wrappedErr := fmt.Errorf("prepare deleted skill staging path: %w", err)
		logger.ErrorContext(ctx, "delete user skill failed", zap.Uint64("user_id", userID), zap.String("skill_id", skillID), zap.Error(wrappedErr))
		return wrappedErr
	}
	if err := os.Rename(target, detached); err != nil {
		s.mutationMu.Unlock()
		wrappedErr := fmt.Errorf("detach user skill from workspace: %w", err)
		logger.ErrorContext(ctx, "delete user skill failed", zap.Uint64("user_id", userID), zap.String("skill_id", skillID), zap.Error(wrappedErr))
		return wrappedErr
	}
	s.mutationMu.Unlock()
	if err := os.RemoveAll(detached); err != nil {
		wrappedErr := fmt.Errorf("remove detached user skill: %w", err)
		logger.ErrorContext(ctx, "delete user skill failed", zap.Uint64("user_id", userID), zap.String("skill_id", skillID), zap.String("staging_path", detached), zap.Error(wrappedErr))
		return wrappedErr
	}
	return nil
}

// CleanupStaging removes only transient directories owned by user skill Store operations.
func (s *Store) CleanupStaging(ctx context.Context) error {
	entries, err := os.ReadDir(s.stagingDir)
	if err != nil {
		wrappedErr := fmt.Errorf("read skill staging directory: %w", err)
		logger.ErrorContext(ctx, "clean skill staging directory failed", zap.String("directory", s.stagingDir), zap.Error(wrappedErr))
		return wrappedErr
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			logger.ErrorContext(ctx, "clean skill staging directory failed", zap.String("directory", s.stagingDir), zap.Error(err))
			return err
		}
		if !strings.HasPrefix(entry.Name(), "skill-upload-") && !strings.HasPrefix(entry.Name(), "skill-delete-") {
			continue
		}
		entryPath := filepath.Join(s.stagingDir, entry.Name())
		if err := os.RemoveAll(entryPath); err != nil {
			wrappedErr := fmt.Errorf("remove owned skill staging entry: %w", err)
			logger.ErrorContext(ctx, "clean skill staging directory failed", zap.String("path", entryPath), zap.Error(wrappedErr))
			return wrappedErr
		}
	}
	return nil
}
