package workshop

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

type treeEntry struct {
	Path   string
	Type   string
	Size   int64
	SHA256 string
	Mode   os.FileMode `json:",omitempty"`
}

// Files are opened descriptor-relatively without following any path component.
// Worker processes have already stopped before workspace hashing begins.
func treeEntries(ctx context.Context, root string, pack bool) ([]treeEntry, error) {
	if err := noSymlinks(root); err != nil {
		return nil, err
	}
	entries := []treeEntry{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if !pack && rel == ".workshop-home" {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		entry := treeEntry{Path: filepath.ToSlash(rel), Size: info.Size()}
		switch {
		case info.IsDir():
			if err := noSymlinks(path); err != nil {
				return err
			}
			entry.Type = "directory"
			// Pack identity depends on contents and executable bits, not filesystem
			// allocation details, so staging a copy yields the same identity.
			if pack {
				entry.Size = 0
			}
		case info.Mode().IsRegular():
			entry.Type = "file"
			f, err := openArtifactDownload(root, rel)
			if err != nil {
				return err
			}
			actual, err := f.Stat()
			if err != nil || !actual.Mode().IsRegular() {
				f.Close()
				return errors.New("tree entry changed")
			}
			hash := sha256.New()
			size, readErr := copyWithContext(ctx, hash, f)
			closeErr := f.Close()
			if readErr != nil {
				return readErr
			}
			if closeErr != nil {
				return closeErr
			}
			if size != info.Size() {
				return errors.New("tree file changed")
			}
			entry.Size = size
			entry.SHA256 = hex.EncodeToString(hash.Sum(nil))
			if pack {
				entry.Mode = actual.Mode().Perm() & 0111
			}
		case info.Mode()&os.ModeSymlink != 0:
			if pack {
				return errors.New("pack checks cannot contain symlinks")
			}
			entry.Type = "symlink"
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			digest := sha256.Sum256([]byte(target))
			entry.SHA256 = hex.EncodeToString(digest[:])
		default:
			return errors.New("unsupported tree entry type")
		}
		entries = append(entries, entry)
		return nil
	})
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return entries, err
}
func copyWithContext(ctx context.Context, dst io.Writer, src io.Reader) (int64, error) {
	buffer := make([]byte, 32<<10)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		n, err := src.Read(buffer)
		if n > 0 {
			written, writeErr := dst.Write(buffer[:n])
			total += int64(written)
			if writeErr != nil {
				return total, writeErr
			}
			if written != n {
				return total, io.ErrShortWrite
			}
		}
		if err == io.EOF {
			return total, nil
		}
		if err != nil {
			return total, err
		}
	}
}
func entriesHash(entries []treeEntry) string {
	hash := sha256.New()
	encoder := json.NewEncoder(hash)
	for _, entry := range entries {
		_ = encoder.Encode(entry)
	}
	return hex.EncodeToString(hash.Sum(nil))
}
func workspaceTreeHash(ctx context.Context, root string) (string, error) {
	entries, err := treeEntries(ctx, root, false)
	if err != nil {
		return "", err
	}
	return entriesHash(entries), nil
}
func writableTree(root string) {
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			_ = os.Chmod(path, 0700)
		}
		return nil
	})
}

// snapshotChecks only copies trusted pack inputs; the result is outside every
// task workspace and later mounted read-only into fresh check containers.
func snapshotChecks(root, packDir string, required bool) (string, error) {
	if packDir == "" {
		if required {
			return "", fmt.Errorf("%w: acceptance requires pack_dir", ErrInvalid)
		}
		return "", nil
	}
	absolute, err := filepath.Abs(packDir)
	if err != nil {
		return "", err
	}
	source := filepath.Join(absolute, "checks")
	if _, err := os.Lstat(source); os.IsNotExist(err) && !required {
		return "", nil
	}
	entries, err := treeEntries(context.Background(), source, true)
	if err != nil {
		return "", fmt.Errorf("%w: invalid pack checks", ErrInvalid)
	}
	hash := entriesHash(entries)
	parent := filepath.Join(root, "packs")
	if err := mkdirNoSymlinks(parent, 0700); err != nil {
		return "", err
	}
	target := filepath.Join(parent, hash)
	checks := filepath.Join(target, "checks")
	if _, err := os.Lstat(target); err == nil {
		existing, err := treeEntries(context.Background(), checks, true)
		if err != nil || entriesHash(existing) != hash {
			return "", errors.New("pack snapshot verification failed")
		}
		if err := verifySealedChecks(target); err != nil {
			return "", err
		}
		return checks, nil
	} else if !os.IsNotExist(err) {
		return "", err
	}
	staging, err := os.MkdirTemp(parent, "pack-")
	if err != nil {
		return "", err
	}
	defer func() { writableTree(staging); _ = os.RemoveAll(staging) }()
	stageChecks := filepath.Join(staging, "checks")
	if err := os.Mkdir(stageChecks, 0700); err != nil {
		return "", err
	}
	for _, entry := range entries {
		dest := filepath.Join(stageChecks, filepath.FromSlash(entry.Path))
		if entry.Type == "directory" {
			if err := os.Mkdir(dest, 0700); err != nil {
				return "", err
			}
			continue
		}
		src, err := openArtifactDownload(source, filepath.FromSlash(entry.Path))
		if err != nil {
			return "", err
		}
		out, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			src.Close()
			return "", err
		}
		_, err = io.Copy(out, src)
		src.Close()
		closeErr := out.Close()
		if err != nil {
			return "", err
		}
		if closeErr != nil {
			return "", closeErr
		}
		if err := os.Chmod(dest, 0444|entry.Mode); err != nil {
			return "", err
		}
	}
	copied, err := treeEntries(context.Background(), stageChecks, true)
	if err != nil || entriesHash(copied) != hash {
		return "", errors.New("pack changed during snapshot")
	}
	// Seal deepest directories before publishing the content-addressed copy.
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].Type == "directory" {
			if err := os.Chmod(filepath.Join(stageChecks, filepath.FromSlash(entries[i].Path)), 0555); err != nil {
				return "", err
			}
		}
	}
	if err := os.Chmod(stageChecks, 0555); err != nil {
		return "", err
	}
	if err := os.Chmod(staging, 0555); err != nil {
		return "", err
	}
	if err := os.Rename(staging, target); err != nil {
		return "", err
	}
	return checks, nil
}

func verifySealedChecks(root string) error {
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0222 != 0 || (info.IsDir() && info.Mode().Perm() != 0555) {
			return errors.New("pack snapshot is not sealed")
		}
		return nil
	})
}
