// Package skillsync atomically publishes versioned builtin skills for runtime use.
package skillsync

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"easygo-agent/internal/config"
	"easygo-agent/internal/platform/logger"
	"easygo-agent/internal/skill/manifest"
	"go.uber.org/zap"
)

const demoSkillID = "easygo-agent-skill"

// Syncer refreshes versioned builtin content and atomically publishes its runtime mirror.
type Syncer struct {
	rootDir    string
	readmeSrc  string
	builtinSrc string
	builtinDir string
}

// New validates paths and constructs a builtin skill synchronizer.
func New(cfg config.Skills) (*Syncer, error) {
	if strings.TrimSpace(cfg.RootDir) == "" || strings.TrimSpace(cfg.ReadmeSrc) == "" {
		err := fmt.Errorf("skills root_dir and readme_src cannot be empty")
		logger.Error("create builtin skill syncer failed", zap.Error(err))
		return nil, err
	}
	rootDir, err := filepath.Abs(filepath.Clean(cfg.RootDir))
	if err != nil {
		wrappedErr := fmt.Errorf("resolve skills root directory: %w", err)
		logger.Error("create builtin skill syncer failed", zap.String("root_dir", cfg.RootDir), zap.Error(wrappedErr))
		return nil, wrappedErr
	}
	readmeSrc, err := filepath.Abs(filepath.Clean(cfg.ReadmeSrc))
	if err != nil {
		wrappedErr := fmt.Errorf("resolve README source: %w", err)
		logger.Error("create builtin skill syncer failed", zap.String("readme_src", cfg.ReadmeSrc), zap.Error(wrappedErr))
		return nil, wrappedErr
	}
	return &Syncer{
		rootDir:    rootDir,
		readmeSrc:  readmeSrc,
		builtinSrc: filepath.Join(rootDir, "builtin-src"),
		builtinDir: filepath.Join(rootDir, "builtin"),
	}, nil
}

// Sync refreshes the demo reference and publishes a complete builtin source mirror.
func (s *Syncer) Sync(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		logger.ErrorContext(ctx, "sync builtin skills failed", zap.Error(err))
		return err
	}
	readme, err := os.ReadFile(s.readmeSrc)
	if err != nil {
		wrappedErr := fmt.Errorf("read builtin README source: %w", err)
		logger.ErrorContext(ctx, "sync builtin skills failed", zap.String("readme_src", s.readmeSrc), zap.Error(wrappedErr))
		return wrappedErr
	}
	referencePath := filepath.Join(s.builtinSrc, demoSkillID, "references", "README.md")
	if err := atomicWriteFile(ctx, referencePath, readme, 0o600); err != nil {
		logger.ErrorContext(ctx, "sync builtin skills failed", zap.String("reference_path", referencePath), zap.Error(err))
		return err
	}
	if err := os.MkdirAll(s.rootDir, 0o750); err != nil {
		wrappedErr := fmt.Errorf("create skills root directory: %w", err)
		logger.ErrorContext(ctx, "sync builtin skills failed", zap.String("root_dir", s.rootDir), zap.Error(wrappedErr))
		return wrappedErr
	}
	temporaryDir, err := os.MkdirTemp(s.rootDir, ".builtin-sync-")
	if err != nil {
		wrappedErr := fmt.Errorf("create builtin mirror staging directory: %w", err)
		logger.ErrorContext(ctx, "sync builtin skills failed", zap.String("root_dir", s.rootDir), zap.Error(wrappedErr))
		return wrappedErr
	}
	// cleanMirrorStage removes the temporary path after publication or failure.
	defer func() {
		if removeErr := os.RemoveAll(temporaryDir); removeErr != nil {
			logger.ErrorContext(ctx, "clean builtin mirror staging directory failed", zap.String("directory", temporaryDir), zap.Error(removeErr))
		}
	}()

	if err := copyBuiltinTree(ctx, s.builtinSrc, temporaryDir); err != nil {
		logger.ErrorContext(ctx, "sync builtin skills failed", zap.String("builtin_src", s.builtinSrc), zap.Error(err))
		return err
	}
	if err := publishDirectory(ctx, temporaryDir, s.builtinDir); err != nil {
		logger.ErrorContext(ctx, "sync builtin skills failed", zap.String("builtin_dir", s.builtinDir), zap.Error(err))
		return err
	}
	return nil
}

// atomicWriteFile replaces one regular file only after its complete content is durable in a sibling temporary file.
func atomicWriteFile(ctx context.Context, target string, content []byte, mode fs.FileMode) error {
	if err := ctx.Err(); err != nil {
		logger.ErrorContext(ctx, "write builtin reference failed", zap.String("target", target), zap.Error(err))
		return err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
		wrappedErr := fmt.Errorf("create builtin reference directory: %w", err)
		logger.ErrorContext(ctx, "write builtin reference failed", zap.String("target", target), zap.Error(wrappedErr))
		return wrappedErr
	}
	temporary, err := os.CreateTemp(filepath.Dir(target), ".README-")
	if err != nil {
		wrappedErr := fmt.Errorf("create temporary builtin reference: %w", err)
		logger.ErrorContext(ctx, "write builtin reference failed", zap.String("target", target), zap.Error(wrappedErr))
		return wrappedErr
	}
	temporaryPath := temporary.Name()
	// cleanReferenceTemp removes any unpublished README temporary file.
	defer func() {
		if removeErr := os.Remove(temporaryPath); removeErr != nil && !os.IsNotExist(removeErr) {
			logger.ErrorContext(ctx, "clean temporary builtin reference failed", zap.String("path", temporaryPath), zap.Error(removeErr))
		}
	}()
	if err := temporary.Chmod(mode); err != nil {
		_ = temporary.Close()
		wrappedErr := fmt.Errorf("set temporary builtin reference mode: %w", err)
		logger.ErrorContext(ctx, "write builtin reference failed", zap.String("target", target), zap.Error(wrappedErr))
		return wrappedErr
	}
	if _, err := temporary.Write(content); err != nil {
		_ = temporary.Close()
		wrappedErr := fmt.Errorf("write temporary builtin reference: %w", err)
		logger.ErrorContext(ctx, "write builtin reference failed", zap.String("target", target), zap.Error(wrappedErr))
		return wrappedErr
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		wrappedErr := fmt.Errorf("sync temporary builtin reference: %w", err)
		logger.ErrorContext(ctx, "write builtin reference failed", zap.String("target", target), zap.Error(wrappedErr))
		return wrappedErr
	}
	if err := temporary.Close(); err != nil {
		wrappedErr := fmt.Errorf("close temporary builtin reference: %w", err)
		logger.ErrorContext(ctx, "write builtin reference failed", zap.String("target", target), zap.Error(wrappedErr))
		return wrappedErr
	}
	if err := os.Rename(temporaryPath, target); err != nil {
		wrappedErr := fmt.Errorf("publish builtin reference: %w", err)
		logger.ErrorContext(ctx, "write builtin reference failed", zap.String("target", target), zap.Error(wrappedErr))
		return wrappedErr
	}
	return nil
}

// copyBuiltinTree validates builtin skill roots and copies only directories and regular files.
func copyBuiltinTree(ctx context.Context, sourceDir, targetDir string) error {
	entries, err := os.ReadDir(sourceDir)
	if err != nil {
		wrappedErr := fmt.Errorf("read builtin source directory: %w", err)
		logger.ErrorContext(ctx, "copy builtin skill tree failed", zap.String("source", sourceDir), zap.Error(wrappedErr))
		return wrappedErr
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			logger.ErrorContext(ctx, "copy builtin skill tree failed", zap.String("source", sourceDir), zap.Error(err))
			return err
		}
		if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			err := fmt.Errorf("builtin source entry %q must be a directory", entry.Name())
			logger.ErrorContext(ctx, "copy builtin skill tree failed", zap.String("source", sourceDir), zap.Error(err))
			return err
		}
		if err := manifest.ValidateID(entry.Name()); err != nil {
			wrappedErr := fmt.Errorf("validate builtin skill directory %q: %w", entry.Name(), err)
			logger.ErrorContext(ctx, "copy builtin skill tree failed", zap.String("skill_id", entry.Name()), zap.Error(wrappedErr))
			return wrappedErr
		}
		frontMatter, err := manifest.Read(ctx, filepath.Join(sourceDir, entry.Name(), "SKILL.md"))
		if err != nil {
			logger.ErrorContext(ctx, "copy builtin skill tree failed", zap.String("skill_id", entry.Name()), zap.Error(err))
			return err
		}
		if frontMatter.Name != entry.Name() {
			err := fmt.Errorf("builtin manifest name %q does not match directory %q", frontMatter.Name, entry.Name())
			logger.ErrorContext(ctx, "copy builtin skill tree failed", zap.String("skill_id", entry.Name()), zap.Error(err))
			return err
		}
	}

	// walkBuiltinSource copies each validated source entry into the unpublished mirror.
	walkBuiltinSource := func(filePath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			wrappedErr := fmt.Errorf("walk builtin source %q: %w", filePath, walkErr)
			logger.ErrorContext(ctx, "copy builtin skill tree failed", zap.String("path", filePath), zap.Error(wrappedErr))
			return wrappedErr
		}
		if err := ctx.Err(); err != nil {
			logger.ErrorContext(ctx, "copy builtin skill tree failed", zap.String("path", filePath), zap.Error(err))
			return err
		}
		relative, err := filepath.Rel(sourceDir, filePath)
		if err != nil {
			wrappedErr := fmt.Errorf("resolve builtin source relative path: %w", err)
			logger.ErrorContext(ctx, "copy builtin skill tree failed", zap.String("path", filePath), zap.Error(wrappedErr))
			return wrappedErr
		}
		if relative == "." {
			return nil
		}
		target := filepath.Join(targetDir, relative)
		info, err := entry.Info()
		if err != nil {
			wrappedErr := fmt.Errorf("inspect builtin source entry: %w", err)
			logger.ErrorContext(ctx, "copy builtin skill tree failed", zap.String("path", filePath), zap.Error(wrappedErr))
			return wrappedErr
		}
		if info.Mode()&os.ModeSymlink != 0 {
			err := fmt.Errorf("builtin source symlink %q is not allowed", filePath)
			logger.ErrorContext(ctx, "copy builtin skill tree failed", zap.String("path", filePath), zap.Error(err))
			return err
		}
		if info.IsDir() {
			if err := os.MkdirAll(target, 0o750); err != nil {
				wrappedErr := fmt.Errorf("create builtin mirror directory: %w", err)
				logger.ErrorContext(ctx, "copy builtin skill tree failed", zap.String("path", target), zap.Error(wrappedErr))
				return wrappedErr
			}
			return nil
		}
		if !info.Mode().IsRegular() {
			err := fmt.Errorf("builtin source entry %q is not a regular file", filePath)
			logger.ErrorContext(ctx, "copy builtin skill tree failed", zap.String("path", filePath), zap.Error(err))
			return err
		}
		if err := copyRegularFile(ctx, filePath, target); err != nil {
			logger.ErrorContext(ctx, "copy builtin skill tree failed", zap.String("path", filePath), zap.Error(err))
			return err
		}
		return nil
	}
	if err := filepath.WalkDir(sourceDir, walkBuiltinSource); err != nil {
		logger.ErrorContext(ctx, "copy builtin skill tree failed", zap.String("source", sourceDir), zap.Error(err))
		return err
	}
	return nil
}

// copyRegularFile streams one regular builtin source file into the unpublished mirror.
func copyRegularFile(ctx context.Context, source, target string) error {
	if err := ctx.Err(); err != nil {
		logger.ErrorContext(ctx, "copy builtin file failed", zap.String("source", source), zap.Error(err))
		return err
	}
	input, err := os.Open(source)
	if err != nil {
		wrappedErr := fmt.Errorf("open builtin source file: %w", err)
		logger.ErrorContext(ctx, "copy builtin file failed", zap.String("source", source), zap.Error(wrappedErr))
		return wrappedErr
	}
	// closeBuiltinSource records a source close failure after the copy attempt.
	defer func() {
		if closeErr := input.Close(); closeErr != nil {
			logger.ErrorContext(ctx, "close builtin source file failed", zap.String("source", source), zap.Error(closeErr))
		}
	}()
	output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		wrappedErr := fmt.Errorf("create builtin mirror file: %w", err)
		logger.ErrorContext(ctx, "copy builtin file failed", zap.String("target", target), zap.Error(wrappedErr))
		return wrappedErr
	}
	if _, err := io.Copy(output, input); err != nil {
		_ = output.Close()
		wrappedErr := fmt.Errorf("copy builtin source file: %w", err)
		logger.ErrorContext(ctx, "copy builtin file failed", zap.String("source", source), zap.String("target", target), zap.Error(wrappedErr))
		return wrappedErr
	}
	if err := output.Close(); err != nil {
		wrappedErr := fmt.Errorf("close builtin mirror file: %w", err)
		logger.ErrorContext(ctx, "copy builtin file failed", zap.String("target", target), zap.Error(wrappedErr))
		return wrappedErr
	}
	return nil
}

// publishDirectory swaps a complete sibling directory into the active runtime location.
func publishDirectory(ctx context.Context, temporaryDir, targetDir string) error {
	if err := ctx.Err(); err != nil {
		logger.ErrorContext(ctx, "publish builtin directory failed", zap.String("target", targetDir), zap.Error(err))
		return err
	}
	backupDir, err := os.MkdirTemp(filepath.Dir(targetDir), ".builtin-backup-")
	if err != nil {
		wrappedErr := fmt.Errorf("reserve builtin backup path: %w", err)
		logger.ErrorContext(ctx, "publish builtin directory failed", zap.String("target", targetDir), zap.Error(wrappedErr))
		return wrappedErr
	}
	if err := os.Remove(backupDir); err != nil {
		wrappedErr := fmt.Errorf("prepare builtin backup path: %w", err)
		logger.ErrorContext(ctx, "publish builtin directory failed", zap.String("backup", backupDir), zap.Error(wrappedErr))
		return wrappedErr
	}
	hadTarget := false
	if _, err := os.Lstat(targetDir); err == nil {
		if err := os.Rename(targetDir, backupDir); err != nil {
			wrappedErr := fmt.Errorf("move active builtin to backup: %w", err)
			logger.ErrorContext(ctx, "publish builtin directory failed", zap.String("target", targetDir), zap.Error(wrappedErr))
			return wrappedErr
		}
		hadTarget = true
	} else if !os.IsNotExist(err) {
		wrappedErr := fmt.Errorf("inspect active builtin directory: %w", err)
		logger.ErrorContext(ctx, "publish builtin directory failed", zap.String("target", targetDir), zap.Error(wrappedErr))
		return wrappedErr
	}
	if err := os.Rename(temporaryDir, targetDir); err != nil {
		if hadTarget {
			if restoreErr := os.Rename(backupDir, targetDir); restoreErr != nil {
				logger.ErrorContext(ctx, "restore builtin directory failed", zap.String("target", targetDir), zap.Error(restoreErr))
			}
		}
		wrappedErr := fmt.Errorf("publish new builtin directory: %w", err)
		logger.ErrorContext(ctx, "publish builtin directory failed", zap.String("target", targetDir), zap.Error(wrappedErr))
		return wrappedErr
	}
	if hadTarget {
		if err := os.RemoveAll(backupDir); err != nil {
			wrappedErr := fmt.Errorf("remove previous builtin directory: %w", err)
			logger.ErrorContext(ctx, "publish builtin directory failed", zap.String("backup", backupDir), zap.Error(wrappedErr))
			return wrappedErr
		}
	}
	return nil
}
