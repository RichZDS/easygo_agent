package workspace

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"easygo-agent/internal/config"
	"easygo-agent/internal/platform/logger"
	"go.uber.org/zap"
)

var builtinIDPattern = regexp.MustCompile(`^[a-z0-9-]{2,64}$`)

// Manager owns the physical directories used for builtin and per-user skills.
type Manager struct {
	rootDir    string
	builtinSrc string
	builtinDir string
	workspaces string
}

// NewManager creates the skills directory layout and returns its workspace manager.
func NewManager(cfg config.Skills) (*Manager, error) {
	if strings.TrimSpace(cfg.RootDir) == "" {
		err := fmt.Errorf("skills root directory cannot be empty")
		logger.Error("create workspace manager failed", zap.Error(err))
		return nil, err
	}
	rootDir, err := filepath.Abs(filepath.Clean(cfg.RootDir))
	if err != nil {
		wrappedErr := fmt.Errorf("resolve skills root directory: %w", err)
		logger.Error("create workspace manager failed", zap.String("root_dir", cfg.RootDir), zap.Error(wrappedErr))
		return nil, wrappedErr
	}
	manager := &Manager{
		rootDir:    rootDir,
		builtinSrc: filepath.Join(rootDir, "builtin-src"),
		builtinDir: filepath.Join(rootDir, "builtin"),
		workspaces: filepath.Join(rootDir, "workspaces"),
	}
	for _, dir := range []string{manager.rootDir, manager.builtinSrc, manager.builtinDir, manager.workspaces} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			wrappedErr := fmt.Errorf("create skills directory %q: %w", dir, err)
			logger.Error("create workspace manager failed", zap.String("directory", dir), zap.Error(wrappedErr))
			return nil, wrappedErr
		}
	}
	return manager, nil
}

// EnsureWorkspace creates a user's workspace and synchronizes its managed builtin links.
func (m *Manager) EnsureWorkspace(ctx context.Context, userID uint64) (string, error) {
	if err := ctx.Err(); err != nil {
		logger.ErrorContext(ctx, "ensure workspace failed", zap.Uint64("user_id", userID), zap.Error(err))
		return "", err
	}
	for _, managedDir := range []string{m.rootDir, m.workspaces} {
		if err := validateManagedDirectory(ctx, managedDir); err != nil {
			logger.ErrorContext(ctx, "ensure workspace failed",
				zap.Uint64("user_id", userID),
				zap.String("directory", managedDir),
				zap.Error(err),
			)
			return "", err
		}
	}
	workspaceDir := filepath.Join(m.workspaces, strconv.FormatUint(userID, 10))
	workspaceInfo, err := os.Lstat(workspaceDir)
	if err == nil {
		if workspaceInfo.Mode()&os.ModeSymlink != 0 {
			symlinkErr := fmt.Errorf("user workspace cannot be a symlink")
			logger.ErrorContext(ctx, "ensure workspace failed",
				zap.Uint64("user_id", userID),
				zap.String("workspace", workspaceDir),
				zap.Error(symlinkErr),
			)
			return "", symlinkErr
		}
		if !workspaceInfo.IsDir() {
			typeErr := fmt.Errorf("user workspace path is not a directory")
			logger.ErrorContext(ctx, "ensure workspace failed",
				zap.Uint64("user_id", userID),
				zap.String("workspace", workspaceDir),
				zap.Error(typeErr),
			)
			return "", typeErr
		}
	} else if os.IsNotExist(err) {
		if err := os.Mkdir(workspaceDir, 0o750); err != nil {
			wrappedErr := fmt.Errorf("create user workspace: %w", err)
			logger.ErrorContext(ctx, "ensure workspace failed",
				zap.Uint64("user_id", userID),
				zap.String("workspace", workspaceDir),
				zap.Error(wrappedErr),
			)
			return "", wrappedErr
		}
	} else {
		wrappedErr := fmt.Errorf("inspect user workspace: %w", err)
		logger.ErrorContext(ctx, "ensure workspace failed",
			zap.Uint64("user_id", userID),
			zap.String("workspace", workspaceDir),
			zap.Error(wrappedErr),
		)
		return "", wrappedErr
	}

	builtinIDs, err := m.BuiltinIDs(ctx)
	if err != nil {
		logger.ErrorContext(ctx, "ensure workspace failed", zap.Uint64("user_id", userID), zap.Error(err))
		return "", err
	}
	sortedIDs := make([]string, 0, len(builtinIDs))
	for skillID := range builtinIDs {
		sortedIDs = append(sortedIDs, skillID)
	}
	sort.Strings(sortedIDs)
	for _, skillID := range sortedIDs {
		if err := ctx.Err(); err != nil {
			logger.ErrorContext(ctx, "ensure workspace failed", zap.Uint64("user_id", userID), zap.Error(err))
			return "", err
		}
		if err := m.ensureBuiltinLink(ctx, workspaceDir, userID, skillID); err != nil {
			logger.ErrorContext(ctx, "ensure workspace failed",
				zap.Uint64("user_id", userID),
				zap.String("skill_id", skillID),
				zap.Error(err),
			)
			return "", err
		}
	}
	return workspaceDir, nil
}

// NewBackend creates a read-only Eino backend scoped to one user's workspace.
func (m *Manager) NewBackend(ctx context.Context, userID uint64) (*Backend, error) {
	workspaceDir, err := m.EnsureWorkspace(ctx, userID)
	if err != nil {
		logger.ErrorContext(ctx, "create workspace backend failed", zap.Uint64("user_id", userID), zap.Error(err))
		return nil, err
	}
	workspaceRoot, err := filepath.EvalSymlinks(workspaceDir)
	if err != nil {
		wrappedErr := fmt.Errorf("resolve user workspace: %w", err)
		logger.ErrorContext(ctx, "create workspace backend failed",
			zap.Uint64("user_id", userID),
			zap.String("workspace", workspaceDir),
			zap.Error(wrappedErr),
		)
		return nil, wrappedErr
	}
	builtinRoot, err := filepath.EvalSymlinks(m.builtinDir)
	if err != nil {
		wrappedErr := fmt.Errorf("resolve builtin directory: %w", err)
		logger.ErrorContext(ctx, "create workspace backend failed",
			zap.Uint64("user_id", userID),
			zap.String("builtin_dir", m.builtinDir),
			zap.Error(wrappedErr),
		)
		return nil, wrappedErr
	}
	return &Backend{
		workspaceDir:  workspaceDir,
		workspaceRoot: filepath.Clean(workspaceRoot),
		builtinDir:    m.builtinDir,
		builtinRoot:   filepath.Clean(builtinRoot),
	}, nil
}

// BuiltinIDs returns the names of immediate directories in the active builtin tree.
func (m *Manager) BuiltinIDs(ctx context.Context) (map[string]struct{}, error) {
	if err := ctx.Err(); err != nil {
		logger.ErrorContext(ctx, "list builtin skill IDs failed", zap.Error(err))
		return nil, err
	}
	entries, err := os.ReadDir(m.builtinDir)
	if err != nil {
		wrappedErr := fmt.Errorf("read builtin directory: %w", err)
		logger.ErrorContext(ctx, "list builtin skill IDs failed",
			zap.String("builtin_dir", m.builtinDir),
			zap.Error(wrappedErr),
		)
		return nil, wrappedErr
	}
	ids := make(map[string]struct{})
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			logger.ErrorContext(ctx, "list builtin skill IDs failed", zap.Error(err))
			return nil, err
		}
		if !entry.IsDir() {
			continue
		}
		if !builtinIDPattern.MatchString(entry.Name()) {
			err := fmt.Errorf("builtin skill ID %q must match %s", entry.Name(), builtinIDPattern.String())
			logger.ErrorContext(ctx, "list builtin skill IDs failed",
				zap.String("skill_id", entry.Name()),
				zap.Error(err),
			)
			return nil, err
		}
		ids[entry.Name()] = struct{}{}
	}
	return ids, nil
}

// validateManagedDirectory verifies that a manager-owned directory exists without being a symlink.
func validateManagedDirectory(ctx context.Context, directory string) error {
	info, err := os.Lstat(directory)
	if err != nil {
		wrappedErr := fmt.Errorf("inspect managed directory %q: %w", directory, err)
		logger.ErrorContext(ctx, "validate managed directory failed",
			zap.String("directory", directory),
			zap.Error(wrappedErr),
		)
		return wrappedErr
	}
	if info.Mode()&os.ModeSymlink != 0 {
		err := fmt.Errorf("managed directory %q cannot be a symlink", directory)
		logger.ErrorContext(ctx, "validate managed directory failed",
			zap.String("directory", directory),
			zap.Error(err),
		)
		return err
	}
	if !info.IsDir() {
		err := fmt.Errorf("managed path %q is not a directory", directory)
		logger.ErrorContext(ctx, "validate managed directory failed",
			zap.String("directory", directory),
			zap.Error(err),
		)
		return err
	}
	return nil
}

// ensureBuiltinLink installs or repairs one manager-owned builtin symlink.
func (m *Manager) ensureBuiltinLink(ctx context.Context, workspaceDir string, userID uint64, skillID string) error {
	linkPath := filepath.Join(workspaceDir, skillID)
	wantTarget := filepath.Join("..", "..", "builtin", skillID)
	info, err := os.Lstat(linkPath)
	if err == nil {
		if info.Mode()&os.ModeSymlink == 0 {
			conflictErr := fmt.Errorf("workspace path for builtin skill %q contains user-owned content", skillID)
			logger.ErrorContext(ctx, "synchronize builtin link failed",
				zap.Uint64("user_id", userID),
				zap.String("skill_id", skillID),
				zap.Error(conflictErr),
			)
			return conflictErr
		}
		currentTarget, readErr := os.Readlink(linkPath)
		if readErr != nil {
			wrappedErr := fmt.Errorf("read managed builtin link %q: %w", skillID, readErr)
			logger.ErrorContext(ctx, "synchronize builtin link failed",
				zap.Uint64("user_id", userID),
				zap.String("skill_id", skillID),
				zap.Error(wrappedErr),
			)
			return wrappedErr
		}
		if currentTarget == wantTarget {
			return nil
		}
		if removeErr := os.Remove(linkPath); removeErr != nil {
			wrappedErr := fmt.Errorf("remove incorrect managed builtin link %q: %w", skillID, removeErr)
			logger.ErrorContext(ctx, "synchronize builtin link failed",
				zap.Uint64("user_id", userID),
				zap.String("skill_id", skillID),
				zap.Error(wrappedErr),
			)
			return wrappedErr
		}
	} else if !os.IsNotExist(err) {
		wrappedErr := fmt.Errorf("inspect managed builtin link %q: %w", skillID, err)
		logger.ErrorContext(ctx, "synchronize builtin link failed",
			zap.Uint64("user_id", userID),
			zap.String("skill_id", skillID),
			zap.Error(wrappedErr),
		)
		return wrappedErr
	}

	if err := os.Symlink(wantTarget, linkPath); err != nil {
		wrappedErr := fmt.Errorf("create managed builtin link %q: %w", skillID, err)
		logger.ErrorContext(ctx, "synchronize builtin link failed",
			zap.Uint64("user_id", userID),
			zap.String("skill_id", skillID),
			zap.Error(wrappedErr),
		)
		return wrappedErr
	}
	return nil
}
