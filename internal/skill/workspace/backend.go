package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"easygo-agent/internal/platform/logger"
	"github.com/bmatcuk/doublestar/v4"
	einofs "github.com/cloudwego/eino/adk/filesystem"
	"go.uber.org/zap"
)

// ErrReadOnly indicates that the model-facing workspace backend cannot mutate files.
var ErrReadOnly = errors.New("workspace backend is read-only")

// Backend exposes one user's skills through virtual absolute paths.
type Backend struct {
	workspaceDir  string
	workspaceRoot string
	builtinDir    string
	builtinRoot   string
}

type virtualEntry struct {
	virtualPath  string
	physicalPath string
	info         os.FileInfo
}

var _ einofs.Backend = (*Backend)(nil)

// LsInfo lists immediate children with virtual absolute paths.
func (b *Backend) LsInfo(ctx context.Context, req *einofs.LsInfoRequest) ([]einofs.FileInfo, error) {
	if req == nil {
		err := fmt.Errorf("list request cannot be nil")
		logger.ErrorContext(ctx, "list workspace path failed", zap.Error(err))
		return nil, err
	}
	virtualDir, physicalDir, err := b.resolveExisting(ctx, req.Path)
	if err != nil {
		logger.ErrorContext(ctx, "list workspace path failed", zap.String("path", req.Path), zap.Error(err))
		return nil, err
	}
	info, err := os.Stat(physicalDir)
	if err != nil {
		wrappedErr := fmt.Errorf("inspect list path %q: %w", virtualDir, err)
		logger.ErrorContext(ctx, "list workspace path failed", zap.String("path", virtualDir), zap.Error(wrappedErr))
		return nil, wrappedErr
	}
	if !info.IsDir() {
		err := fmt.Errorf("list path %q is not a directory", virtualDir)
		logger.ErrorContext(ctx, "list workspace path failed", zap.String("path", virtualDir), zap.Error(err))
		return nil, err
	}
	entries, err := os.ReadDir(physicalDir)
	if err != nil {
		wrappedErr := fmt.Errorf("read list path %q: %w", virtualDir, err)
		logger.ErrorContext(ctx, "list workspace path failed", zap.String("path", virtualDir), zap.Error(wrappedErr))
		return nil, wrappedErr
	}
	result := make([]einofs.FileInfo, 0, len(entries))
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			logger.ErrorContext(ctx, "list workspace path failed", zap.String("path", virtualDir), zap.Error(err))
			return nil, err
		}
		childVirtual := virtualJoin(virtualDir, entry.Name())
		_, childPhysical, err := b.resolveExisting(ctx, childVirtual)
		if err != nil {
			logger.ErrorContext(ctx, "list workspace path failed", zap.String("path", childVirtual), zap.Error(err))
			return nil, err
		}
		childInfo, err := os.Stat(childPhysical)
		if err != nil {
			wrappedErr := fmt.Errorf("inspect listed path %q: %w", childVirtual, err)
			logger.ErrorContext(ctx, "list workspace path failed", zap.String("path", childVirtual), zap.Error(wrappedErr))
			return nil, wrappedErr
		}
		result = append(result, toFileInfo(childVirtual, childInfo))
	}
	sortFileInfos(result)
	return result, nil
}

// Read returns text from a sandboxed virtual file with line pagination.
func (b *Backend) Read(ctx context.Context, req *einofs.ReadRequest) (*einofs.FileContent, error) {
	if req == nil {
		err := fmt.Errorf("read request cannot be nil")
		logger.ErrorContext(ctx, "read workspace file failed", zap.Error(err))
		return nil, err
	}
	virtualPath, physicalPath, err := b.resolveExisting(ctx, req.FilePath)
	if err != nil {
		logger.ErrorContext(ctx, "read workspace file failed", zap.String("path", req.FilePath), zap.Error(err))
		return nil, err
	}
	content, err := os.ReadFile(physicalPath)
	if err != nil {
		wrappedErr := fmt.Errorf("read virtual file %q: %w", virtualPath, err)
		logger.ErrorContext(ctx, "read workspace file failed", zap.String("path", virtualPath), zap.Error(wrappedErr))
		return nil, wrappedErr
	}
	return &einofs.FileContent{Content: paginateContent(string(content), req.Offset, req.Limit)}, nil
}

// GrepRaw searches sandboxed files with a regular expression and optional context lines.
func (b *Backend) GrepRaw(ctx context.Context, req *einofs.GrepRequest) ([]einofs.GrepMatch, error) {
	if req == nil {
		err := fmt.Errorf("grep request cannot be nil")
		logger.ErrorContext(ctx, "grep workspace files failed", zap.Error(err))
		return nil, err
	}
	if req.Pattern == "" {
		err := fmt.Errorf("grep pattern cannot be empty")
		logger.ErrorContext(ctx, "grep workspace files failed", zap.Error(err))
		return nil, err
	}
	pattern := req.Pattern
	if req.CaseInsensitive {
		pattern = "(?i)" + pattern
	}
	if req.EnableMultiline {
		pattern = "(?s)" + pattern
	}
	expression, err := regexp.Compile(pattern)
	if err != nil {
		wrappedErr := fmt.Errorf("compile grep pattern: %w", err)
		logger.ErrorContext(ctx, "grep workspace files failed", zap.Error(wrappedErr))
		return nil, wrappedErr
	}

	searchPath := req.Path
	if searchPath == "" {
		searchPath = "/"
	}
	entries, err := b.collectEntries(ctx, searchPath)
	if err != nil {
		logger.ErrorContext(ctx, "grep workspace files failed", zap.String("path", searchPath), zap.Error(err))
		return nil, err
	}
	matches := make([]einofs.GrepMatch, 0)
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			logger.ErrorContext(ctx, "grep workspace files failed", zap.String("path", searchPath), zap.Error(err))
			return nil, err
		}
		if entry.info.IsDir() {
			continue
		}
		included, err := grepFileIncluded(entry.virtualPath, searchPath, req)
		if err != nil {
			logger.ErrorContext(ctx, "grep workspace files failed", zap.String("path", entry.virtualPath), zap.Error(err))
			return nil, err
		}
		if !included {
			continue
		}
		content, err := os.ReadFile(entry.physicalPath)
		if err != nil {
			wrappedErr := fmt.Errorf("read grep file %q: %w", entry.virtualPath, err)
			logger.ErrorContext(ctx, "grep workspace files failed", zap.String("path", entry.virtualPath), zap.Error(wrappedErr))
			return nil, wrappedErr
		}
		matches = append(matches, grepContent(entry.virtualPath, string(content), expression, req)...)
	}
	sort.Slice(matches,
		// orderGrepMatches orders matches deterministically by virtual path and line.
		func(i, j int) bool {
			if matches[i].Path == matches[j].Path {
				return matches[i].Line < matches[j].Line
			}
			return matches[i].Path < matches[j].Path
		},
	)
	return matches, nil
}

// GlobInfo returns deterministic virtual file information for a doublestar pattern.
func (b *Backend) GlobInfo(ctx context.Context, req *einofs.GlobInfoRequest) ([]einofs.FileInfo, error) {
	if req == nil {
		err := fmt.Errorf("glob request cannot be nil")
		logger.ErrorContext(ctx, "glob workspace files failed", zap.Error(err))
		return nil, err
	}
	if req.Pattern == "" {
		err := fmt.Errorf("glob pattern cannot be empty")
		logger.ErrorContext(ctx, "glob workspace files failed", zap.Error(err))
		return nil, err
	}
	basePath := req.Path
	if basePath == "" {
		basePath = "/"
	}
	baseVirtual, err := normalizeVirtualPath(basePath)
	if err != nil {
		logger.ErrorContext(ctx, "glob workspace files failed", zap.String("path", basePath), zap.Error(err))
		return nil, err
	}
	entries, err := b.collectEntries(ctx, baseVirtual)
	if err != nil {
		logger.ErrorContext(ctx, "glob workspace files failed", zap.String("path", baseVirtual), zap.Error(err))
		return nil, err
	}
	result := make([]einofs.FileInfo, 0)
	absolutePattern := strings.HasPrefix(req.Pattern, "/")
	for _, entry := range entries {
		matchPath := entry.virtualPath
		if !absolutePattern {
			matchPath = virtualRelative(baseVirtual, entry.virtualPath)
		}
		matched, err := doublestar.Match(req.Pattern, matchPath)
		if err != nil {
			wrappedErr := fmt.Errorf("match glob pattern: %w", err)
			logger.ErrorContext(ctx, "glob workspace files failed", zap.String("pattern", req.Pattern), zap.Error(wrappedErr))
			return nil, wrappedErr
		}
		if matched {
			result = append(result, toFileInfo(entry.virtualPath, entry.info))
		}
	}
	sortFileInfos(result)
	return result, nil
}

// Write always rejects mutations because this backend is model-facing and read-only.
func (b *Backend) Write(ctx context.Context, req *einofs.WriteRequest) error {
	fields := []zap.Field{zap.Error(ErrReadOnly)}
	if req != nil {
		fields = append(fields, zap.String("path", req.FilePath))
	}
	logger.ErrorContext(ctx, "write workspace file rejected", fields...)
	return ErrReadOnly
}

// Edit always rejects mutations because this backend is model-facing and read-only.
func (b *Backend) Edit(ctx context.Context, req *einofs.EditRequest) error {
	fields := []zap.Field{zap.Error(ErrReadOnly)}
	if req != nil {
		fields = append(fields, zap.String("path", req.FilePath))
	}
	logger.ErrorContext(ctx, "edit workspace file rejected", fields...)
	return ErrReadOnly
}

// resolveExisting converts a virtual path to an existing allowed physical path.
func (b *Backend) resolveExisting(ctx context.Context, input string) (string, string, error) {
	if err := ctx.Err(); err != nil {
		logger.ErrorContext(ctx, "resolve workspace path failed", zap.String("path", input), zap.Error(err))
		return "", "", err
	}
	virtualPath, err := normalizeVirtualPath(input)
	if err != nil {
		logger.ErrorContext(ctx, "resolve workspace path failed", zap.String("path", input), zap.Error(err))
		return "", "", err
	}
	if err := b.validatePathSymlinks(ctx, virtualPath); err != nil {
		logger.ErrorContext(ctx, "resolve workspace path failed", zap.String("path", virtualPath), zap.Error(err))
		return "", "", err
	}
	relativePath := strings.TrimPrefix(virtualPath, "/")
	candidate := filepath.Join(b.workspaceDir, filepath.FromSlash(relativePath))
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		wrappedErr := fmt.Errorf("resolve virtual path %q: %w", virtualPath, err)
		logger.ErrorContext(ctx, "resolve workspace path failed", zap.String("path", virtualPath), zap.Error(wrappedErr))
		return "", "", wrappedErr
	}
	resolved = filepath.Clean(resolved)
	switch {
	case pathWithin(resolved, b.workspaceRoot):
		return virtualPath, resolved, nil
	case pathWithin(resolved, b.builtinRoot):
		if err := b.validateManagedBuiltinPath(ctx, virtualPath); err != nil {
			logger.ErrorContext(ctx, "resolve workspace path failed", zap.String("path", virtualPath), zap.Error(err))
			return "", "", err
		}
		return virtualPath, resolved, nil
	default:
		err := fmt.Errorf("virtual path %q resolves outside the workspace sandbox", virtualPath)
		logger.ErrorContext(ctx, "resolve workspace path failed", zap.String("path", virtualPath), zap.Error(err))
		return "", "", err
	}
}

// validatePathSymlinks rejects every path-component symlink except an exact managed builtin link at the virtual root.
func (b *Backend) validatePathSymlinks(ctx context.Context, virtualPath string) error {
	relativePath := strings.TrimPrefix(virtualPath, "/")
	if relativePath == "" {
		return nil
	}
	currentPath := b.workspaceDir
	for index, part := range strings.Split(relativePath, "/") {
		currentPath = filepath.Join(currentPath, filepath.FromSlash(part))
		info, err := os.Lstat(currentPath)
		if err != nil {
			wrappedErr := fmt.Errorf("inspect virtual path component %q: %w", virtualPath, err)
			logger.ErrorContext(ctx, "validate workspace path symlinks failed",
				zap.String("path", virtualPath),
				zap.Error(wrappedErr),
			)
			return wrappedErr
		}
		if info.Mode()&os.ModeSymlink == 0 {
			continue
		}
		if index == 0 {
			if err := b.validateManagedBuiltinPath(ctx, virtualPath); err == nil {
				continue
			}
		}
		err = fmt.Errorf("virtual path %q contains an unmanaged symlink", virtualPath)
		logger.ErrorContext(ctx, "validate workspace path symlinks failed",
			zap.String("path", virtualPath),
			zap.Error(err),
		)
		return err
	}
	return nil
}

// validateManagedBuiltinPath verifies that builtin access enters through an exact manager-owned link.
func (b *Backend) validateManagedBuiltinPath(ctx context.Context, virtualPath string) error {
	firstPart := strings.SplitN(strings.TrimPrefix(virtualPath, "/"), "/", 2)[0]
	if firstPart == "" {
		err := fmt.Errorf("builtin path has no skill ID")
		logger.ErrorContext(ctx, "validate managed builtin path failed", zap.String("path", virtualPath), zap.Error(err))
		return err
	}
	linkPath := filepath.Join(b.workspaceDir, filepath.FromSlash(firstPart))
	info, err := os.Lstat(linkPath)
	if err != nil {
		wrappedErr := fmt.Errorf("inspect managed builtin link %q: %w", firstPart, err)
		logger.ErrorContext(ctx, "validate managed builtin path failed",
			zap.String("skill_id", firstPart),
			zap.Error(wrappedErr),
		)
		return wrappedErr
	}
	if info.Mode()&os.ModeSymlink == 0 {
		err := fmt.Errorf("builtin path %q does not use a managed link", virtualPath)
		logger.ErrorContext(ctx, "validate managed builtin path failed", zap.String("path", virtualPath), zap.Error(err))
		return err
	}
	target, err := os.Readlink(linkPath)
	if err != nil {
		wrappedErr := fmt.Errorf("read managed builtin link %q: %w", firstPart, err)
		logger.ErrorContext(ctx, "validate managed builtin path failed",
			zap.String("skill_id", firstPart),
			zap.Error(wrappedErr),
		)
		return wrappedErr
	}
	wantTarget := filepath.Join("..", "..", "builtin", firstPart)
	if target != wantTarget {
		err := fmt.Errorf("builtin path %q does not use the expected managed link", virtualPath)
		logger.ErrorContext(ctx, "validate managed builtin path failed",
			zap.String("skill_id", firstPart),
			zap.Error(err),
		)
		return err
	}
	return nil
}

// collectEntries recursively collects sandboxed descendants while following allowed directory links.
func (b *Backend) collectEntries(ctx context.Context, basePath string) ([]virtualEntry, error) {
	baseVirtual, basePhysical, err := b.resolveExisting(ctx, basePath)
	if err != nil {
		logger.ErrorContext(ctx, "collect workspace entries failed", zap.String("path", basePath), zap.Error(err))
		return nil, err
	}
	baseInfo, err := os.Stat(basePhysical)
	if err != nil {
		wrappedErr := fmt.Errorf("inspect collection base %q: %w", baseVirtual, err)
		logger.ErrorContext(ctx, "collect workspace entries failed", zap.String("path", baseVirtual), zap.Error(wrappedErr))
		return nil, wrappedErr
	}
	if !baseInfo.IsDir() {
		return []virtualEntry{{virtualPath: baseVirtual, physicalPath: basePhysical, info: baseInfo}}, nil
	}
	entries := make([]virtualEntry, 0)
	visited := map[string]struct{}{basePhysical: {}}
	if err := b.walkDirectory(ctx, baseVirtual, basePhysical, visited, &entries); err != nil {
		logger.ErrorContext(ctx, "collect workspace entries failed", zap.String("path", baseVirtual), zap.Error(err))
		return nil, err
	}
	sort.Slice(entries,
		// orderVirtualEntries orders collected entries by virtual path.
		func(i, j int) bool {
			return entries[i].virtualPath < entries[j].virtualPath
		},
	)
	return entries, nil
}

// walkDirectory recursively walks one already-resolved sandbox directory.
func (b *Backend) walkDirectory(ctx context.Context, virtualDir, physicalDir string, visited map[string]struct{}, result *[]virtualEntry) error {
	if err := ctx.Err(); err != nil {
		logger.ErrorContext(ctx, "walk workspace directory failed", zap.String("path", virtualDir), zap.Error(err))
		return err
	}
	entries, err := os.ReadDir(physicalDir)
	if err != nil {
		wrappedErr := fmt.Errorf("read workspace directory %q: %w", virtualDir, err)
		logger.ErrorContext(ctx, "walk workspace directory failed", zap.String("path", virtualDir), zap.Error(wrappedErr))
		return wrappedErr
	}
	for _, entry := range entries {
		childVirtual := virtualJoin(virtualDir, entry.Name())
		_, childPhysical, err := b.resolveExisting(ctx, childVirtual)
		if err != nil {
			logger.ErrorContext(ctx, "walk workspace directory failed", zap.String("path", childVirtual), zap.Error(err))
			return err
		}
		childInfo, err := os.Stat(childPhysical)
		if err != nil {
			wrappedErr := fmt.Errorf("inspect workspace entry %q: %w", childVirtual, err)
			logger.ErrorContext(ctx, "walk workspace directory failed", zap.String("path", childVirtual), zap.Error(wrappedErr))
			return wrappedErr
		}
		*result = append(*result, virtualEntry{
			virtualPath:  childVirtual,
			physicalPath: childPhysical,
			info:         childInfo,
		})
		if !childInfo.IsDir() {
			continue
		}
		if _, ok := visited[childPhysical]; ok {
			continue
		}
		visited[childPhysical] = struct{}{}
		if err := b.walkDirectory(ctx, childVirtual, childPhysical, visited, result); err != nil {
			logger.ErrorContext(ctx, "walk workspace directory failed", zap.String("path", childVirtual), zap.Error(err))
			return err
		}
	}
	return nil
}

// normalizeVirtualPath converts relative or absolute virtual input to a safe absolute path.
func normalizeVirtualPath(input string) (string, error) {
	slashed := filepath.ToSlash(input)
	for _, part := range strings.Split(slashed, "/") {
		if part == ".." {
			err := fmt.Errorf("virtual path traversal is not allowed")
			logger.Error("normalize workspace path failed", zap.String("path", input), zap.Error(err))
			return "", err
		}
	}
	if slashed == "" {
		return "/", nil
	}
	if !strings.HasPrefix(slashed, "/") {
		slashed = "/" + slashed
	}
	return path.Clean(slashed), nil
}

// pathWithin reports whether candidate is root itself or a descendant of root.
func pathWithin(candidate, root string) bool {
	relative, err := filepath.Rel(root, candidate)
	if err != nil {
		return false
	}
	return relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))
}

// virtualJoin joins a virtual directory and child name without losing the virtual root.
func virtualJoin(virtualDir, name string) string {
	if virtualDir == "/" {
		return "/" + name
	}
	return path.Join(virtualDir, name)
}

// virtualRelative returns a slash-separated descendant path relative to a virtual base.
func virtualRelative(baseVirtual, childVirtual string) string {
	if baseVirtual == "/" {
		return strings.TrimPrefix(childVirtual, "/")
	}
	return strings.TrimPrefix(childVirtual, strings.TrimSuffix(baseVirtual, "/")+"/")
}

// paginateContent applies Eino's one-based line offset and optional line limit.
func paginateContent(content string, offset, limit int) string {
	startLine := offset - 1
	if startLine < 0 {
		startLine = 0
	}
	if startLine == 0 && limit <= 0 {
		return content
	}
	start := 0
	for line := 0; line < startLine; line++ {
		index := strings.IndexByte(content[start:], '\n')
		if index == -1 {
			return ""
		}
		start += index + 1
	}
	if limit <= 0 {
		return content[start:]
	}
	end := start
	for line := 0; line < limit; line++ {
		index := strings.IndexByte(content[end:], '\n')
		if index == -1 {
			return content[start:]
		}
		end += index + 1
	}
	return content[start : end-1]
}

// grepFileIncluded applies glob and file-type filters to one virtual file.
func grepFileIncluded(virtualPath, searchPath string, req *einofs.GrepRequest) (bool, error) {
	if req.FileType != "" {
		extension := strings.TrimPrefix(path.Ext(virtualPath), ".")
		if extension != strings.TrimPrefix(req.FileType, ".") {
			return false, nil
		}
	}
	if req.Glob == "" {
		return true, nil
	}
	matchPath := path.Base(virtualPath)
	if strings.Contains(req.Glob, "/") || strings.Contains(req.Glob, "**") {
		baseVirtual, err := normalizeVirtualPath(searchPath)
		if err != nil {
			logger.Error("filter grep file failed", zap.String("path", searchPath), zap.Error(err))
			return false, err
		}
		matchPath = virtualRelative(baseVirtual, virtualPath)
	}
	matched, err := doublestar.Match(req.Glob, matchPath)
	if err != nil {
		wrappedErr := fmt.Errorf("match grep glob: %w", err)
		logger.Error("filter grep file failed", zap.String("glob", req.Glob), zap.Error(wrappedErr))
		return false, wrappedErr
	}
	return matched, nil
}

// grepContent returns matching and requested context lines from one file.
func grepContent(virtualPath, content string, expression *regexp.Regexp, req *einofs.GrepRequest) []einofs.GrepMatch {
	lines := strings.Split(content, "\n")
	selected := make(map[int]struct{})
	if req.EnableMultiline {
		for _, indexes := range expression.FindAllStringIndex(content, -1) {
			firstLine := 1 + strings.Count(content[:indexes[0]], "\n")
			lastLine := firstLine + strings.Count(content[indexes[0]:indexes[1]], "\n")
			selectLineRange(selected, firstLine, lastLine, len(lines), req.BeforeLines, req.AfterLines)
		}
	} else {
		for index, line := range lines {
			if expression.MatchString(line) {
				lineNumber := index + 1
				selectLineRange(selected, lineNumber, lineNumber, len(lines), req.BeforeLines, req.AfterLines)
			}
		}
	}
	lineNumbers := make([]int, 0, len(selected))
	for lineNumber := range selected {
		lineNumbers = append(lineNumbers, lineNumber)
	}
	sort.Ints(lineNumbers)
	matches := make([]einofs.GrepMatch, 0, len(lineNumbers))
	for _, lineNumber := range lineNumbers {
		matches = append(matches, einofs.GrepMatch{
			Path:    virtualPath,
			Line:    lineNumber,
			Content: lines[lineNumber-1],
		})
	}
	return matches
}

// selectLineRange adds a matched range and its requested context to the selected line set.
func selectLineRange(selected map[int]struct{}, firstLine, lastLine, lineCount, beforeLines, afterLines int) {
	start := firstLine - max(beforeLines, 0)
	if start < 1 {
		start = 1
	}
	end := lastLine + max(afterLines, 0)
	if end > lineCount {
		end = lineCount
	}
	for lineNumber := start; lineNumber <= end; lineNumber++ {
		selected[lineNumber] = struct{}{}
	}
}

// toFileInfo converts physical metadata to Eino metadata while retaining only a virtual path.
func toFileInfo(virtualPath string, info os.FileInfo) einofs.FileInfo {
	return einofs.FileInfo{
		Path:       virtualPath,
		IsDir:      info.IsDir(),
		Size:       info.Size(),
		ModifiedAt: info.ModTime().Format(time.RFC3339Nano),
	}
}

// sortFileInfos sorts Eino file metadata deterministically by virtual path.
func sortFileInfos(infos []einofs.FileInfo) {
	sort.Slice(infos,
		// orderFileInfos orders file metadata by virtual path.
		func(i, j int) bool {
			return infos[i].Path < infos[j].Path
		},
	)
}
