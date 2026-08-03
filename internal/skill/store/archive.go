package store

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"easygo-agent/internal/platform/logger"
	"easygo-agent/internal/skill/manifest"
	"go.uber.org/zap"
)

// stagedSkill holds a fully validated but unpublished user skill directory.
type stagedSkill struct {
	directory string
	manifest  manifest.FrontMatter
}

// archiveEntry holds one validated ZIP entry and its final relative path.
type archiveEntry struct {
	file         *zip.File
	relativePath string
}

// stageArchive reads, validates, and extracts one bounded user skill archive.
func (s *Store) stageArchive(ctx context.Context, input io.Reader) (stagedSkill, error) {
	content, err := io.ReadAll(io.LimitReader(input, s.maxZipBytes+1))
	if err != nil {
		wrappedErr := fmt.Errorf("read skill archive: %w", err)
		logger.ErrorContext(ctx, "stage skill archive failed", zap.Error(wrappedErr))
		return stagedSkill{}, wrappedErr
	}
	if int64(len(content)) > s.maxZipBytes {
		err := fmt.Errorf("%w: compressed bytes exceed %d", ErrArchiveTooLarge, s.maxZipBytes)
		logger.ErrorContext(ctx, "stage skill archive failed", zap.Int("archive_bytes", len(content)), zap.Error(err))
		return stagedSkill{}, err
	}
	reader, err := zip.NewReader(bytes.NewReader(content), int64(len(content)))
	if err != nil {
		wrappedErr := fmt.Errorf("%w: open ZIP: %v", ErrInvalidSkill, err)
		logger.ErrorContext(ctx, "stage skill archive failed", zap.Error(wrappedErr))
		return stagedSkill{}, wrappedErr
	}
	entries, err := s.validateArchiveEntries(ctx, reader.File)
	if err != nil {
		logger.ErrorContext(ctx, "stage skill archive failed", zap.Error(err))
		return stagedSkill{}, err
	}
	stageDir, err := os.MkdirTemp(s.stagingDir, "skill-upload-")
	if err != nil {
		wrappedErr := fmt.Errorf("create user skill staging directory: %w", err)
		logger.ErrorContext(ctx, "stage skill archive failed", zap.String("staging_dir", s.stagingDir), zap.Error(wrappedErr))
		return stagedSkill{}, wrappedErr
	}
	succeeded := false
	// cleanFailedStage removes unpublished archive content after any validation or extraction failure.
	defer func() {
		if !succeeded {
			if removeErr := os.RemoveAll(stageDir); removeErr != nil {
				logger.ErrorContext(ctx, "clean failed skill archive staging failed", zap.String("directory", stageDir), zap.Error(removeErr))
			}
		}
	}()
	if err := s.extractEntries(ctx, stageDir, entries); err != nil {
		logger.ErrorContext(ctx, "stage skill archive failed", zap.String("directory", stageDir), zap.Error(err))
		return stagedSkill{}, err
	}
	frontMatter, err := manifest.Read(ctx, filepath.Join(stageDir, "SKILL.md"))
	if err != nil {
		wrappedErr := fmt.Errorf("%w: %v", ErrInvalidSkill, err)
		logger.ErrorContext(ctx, "stage skill archive failed", zap.String("directory", stageDir), zap.Error(wrappedErr))
		return stagedSkill{}, wrappedErr
	}
	succeeded = true
	return stagedSkill{directory: stageDir, manifest: frontMatter}, nil
}

// validateArchiveEntries normalizes one supported ZIP layout without touching the filesystem.
func (s *Store) validateArchiveEntries(ctx context.Context, files []*zip.File) ([]archiveEntry, error) {
	if len(files) == 0 {
		err := fmt.Errorf("%w: ZIP is empty", ErrInvalidSkill)
		logger.ErrorContext(ctx, "validate skill archive failed", zap.Error(err))
		return nil, err
	}
	normalized := make([]string, len(files))
	directManifest := false
	manifestCount := 0
	topLevel := ""
	for index, file := range files {
		if err := ctx.Err(); err != nil {
			logger.ErrorContext(ctx, "validate skill archive failed", zap.Error(err))
			return nil, err
		}
		name, err := normalizeArchivePath(file.Name)
		if err != nil {
			wrappedErr := fmt.Errorf("%w: %v", ErrInvalidSkill, err)
			logger.ErrorContext(ctx, "validate skill archive failed", zap.Error(wrappedErr))
			return nil, wrappedErr
		}
		normalized[index] = name
		if name == "SKILL.md" {
			directManifest = true
		}
		if name == "SKILL.md" || strings.HasSuffix(name, "/SKILL.md") {
			manifestCount++
		}
		first := strings.SplitN(name, "/", 2)[0]
		if topLevel == "" {
			topLevel = first
		} else if topLevel != first {
			topLevel = "*"
		}
	}
	if manifestCount != 1 {
		err := fmt.Errorf("%w: ZIP must contain exactly one SKILL.md", ErrInvalidSkill)
		logger.ErrorContext(ctx, "validate skill archive failed", zap.Int("manifest_count", manifestCount), zap.Error(err))
		return nil, err
	}
	prefix := ""
	if !directManifest {
		if topLevel == "" || topLevel == "*" {
			err := fmt.Errorf("%w: ZIP must use root layout or one top-level directory", ErrInvalidSkill)
			logger.ErrorContext(ctx, "validate skill archive failed", zap.Error(err))
			return nil, err
		}
		prefix = topLevel + "/"
		found := false
		for _, name := range normalized {
			if name == prefix+"SKILL.md" {
				found = true
				break
			}
		}
		if !found {
			err := fmt.Errorf("%w: ZIP does not contain SKILL.md", ErrInvalidSkill)
			logger.ErrorContext(ctx, "validate skill archive failed", zap.Error(err))
			return nil, err
		}
	}
	result := make([]archiveEntry, 0, len(files))
	fileCount := 0
	for index, file := range files {
		name := normalized[index]
		if prefix != "" {
			if name == topLevel && file.FileInfo().IsDir() {
				continue
			}
			if !strings.HasPrefix(name, prefix) || name == strings.TrimSuffix(prefix, "/") {
				err := fmt.Errorf("%w: ZIP contains entries outside its top-level directory", ErrInvalidSkill)
				logger.ErrorContext(ctx, "validate skill archive failed", zap.Error(err))
				return nil, err
			}
			name = strings.TrimPrefix(name, prefix)
		}
		mode := file.Mode()
		isDirectory := file.FileInfo().IsDir()
		if mode&os.ModeSymlink != 0 || (!isDirectory && !mode.IsRegular()) {
			err := fmt.Errorf("%w: ZIP entry %q is not a regular file or directory", ErrInvalidSkill, file.Name)
			logger.ErrorContext(ctx, "validate skill archive failed", zap.Error(err))
			return nil, err
		}
		if !isDirectory {
			fileCount++
			if fileCount > s.maxFiles {
				err := fmt.Errorf("%w: file count exceeds %d", ErrArchiveTooLarge, s.maxFiles)
				logger.ErrorContext(ctx, "validate skill archive failed", zap.Int("file_count", fileCount), zap.Error(err))
				return nil, err
			}
		}
		result = append(result, archiveEntry{file: file, relativePath: name})
	}
	return result, nil
}

// normalizeArchivePath rejects non-portable or escaping ZIP entry names.
func normalizeArchivePath(name string) (string, error) {
	if name == "" || strings.Contains(name, "\\") || strings.HasPrefix(name, "/") {
		err := fmt.Errorf("unsafe ZIP entry path %q", name)
		logger.Error("normalize skill archive path failed", zap.Error(err))
		return "", err
	}
	trimmed := strings.TrimSuffix(name, "/")
	for _, part := range strings.Split(trimmed, "/") {
		if part == "" || part == "." || part == ".." {
			err := fmt.Errorf("unsafe ZIP entry path %q", name)
			logger.Error("normalize skill archive path failed", zap.Error(err))
			return "", err
		}
	}
	cleaned := path.Clean(trimmed)
	if cleaned == "." || strings.HasPrefix(cleaned, "../") {
		err := fmt.Errorf("unsafe ZIP entry path %q", name)
		logger.Error("normalize skill archive path failed", zap.Error(err))
		return "", err
	}
	return cleaned, nil
}

// extractEntries writes validated regular files beneath one unpublished staging directory.
func (s *Store) extractEntries(ctx context.Context, stageDir string, entries []archiveEntry) error {
	var extractedBytes int64
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			logger.ErrorContext(ctx, "extract skill archive failed", zap.String("directory", stageDir), zap.Error(err))
			return err
		}
		target := filepath.Join(stageDir, filepath.FromSlash(entry.relativePath))
		relative, err := filepath.Rel(stageDir, target)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			unsafeErr := fmt.Errorf("%w: extracted path escapes staging", ErrInvalidSkill)
			logger.ErrorContext(ctx, "extract skill archive failed", zap.String("path", entry.relativePath), zap.Error(unsafeErr))
			return unsafeErr
		}
		if entry.file.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o750); err != nil {
				wrappedErr := fmt.Errorf("create extracted skill directory: %w", err)
				logger.ErrorContext(ctx, "extract skill archive failed", zap.String("path", entry.relativePath), zap.Error(wrappedErr))
				return wrappedErr
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
			wrappedErr := fmt.Errorf("create extracted skill parent: %w", err)
			logger.ErrorContext(ctx, "extract skill archive failed", zap.String("path", entry.relativePath), zap.Error(wrappedErr))
			return wrappedErr
		}
		input, err := entry.file.Open()
		if err != nil {
			wrappedErr := fmt.Errorf("open ZIP entry: %w", err)
			logger.ErrorContext(ctx, "extract skill archive failed", zap.String("path", entry.relativePath), zap.Error(wrappedErr))
			return wrappedErr
		}
		output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			_ = input.Close()
			wrappedErr := fmt.Errorf("create extracted skill file: %w", err)
			logger.ErrorContext(ctx, "extract skill archive failed", zap.String("path", entry.relativePath), zap.Error(wrappedErr))
			return wrappedErr
		}
		remaining := s.maxExtractedBytes - extractedBytes
		written, copyErr := io.Copy(output, io.LimitReader(input, remaining+1))
		closeOutputErr := output.Close()
		closeInputErr := input.Close()
		extractedBytes += written
		if copyErr != nil {
			wrappedErr := fmt.Errorf("extract ZIP entry: %w", copyErr)
			logger.ErrorContext(ctx, "extract skill archive failed", zap.String("path", entry.relativePath), zap.Error(wrappedErr))
			return wrappedErr
		}
		if closeOutputErr != nil {
			wrappedErr := fmt.Errorf("close extracted skill file: %w", closeOutputErr)
			logger.ErrorContext(ctx, "extract skill archive failed", zap.String("path", entry.relativePath), zap.Error(wrappedErr))
			return wrappedErr
		}
		if closeInputErr != nil {
			wrappedErr := fmt.Errorf("close ZIP entry: %w", closeInputErr)
			logger.ErrorContext(ctx, "extract skill archive failed", zap.String("path", entry.relativePath), zap.Error(wrappedErr))
			return wrappedErr
		}
		if extractedBytes > s.maxExtractedBytes {
			err := fmt.Errorf("%w: extracted bytes exceed %d", ErrArchiveTooLarge, s.maxExtractedBytes)
			logger.ErrorContext(ctx, "extract skill archive failed", zap.Int64("extracted_bytes", extractedBytes), zap.Error(err))
			return err
		}
	}
	return nil
}
