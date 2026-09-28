//go:build linux

package workshop

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// Open from a pinned filesystem-root descriptor and walk every directory using
// openat + O_NOFOLLOW. Neither workspace/root ancestors nor the final file may
// be a symlink; replacing a name during traversal cannot redirect an open parent.
// Nonblocking avoids hanging on a malicious FIFO; caller requires a regular file.
func openArtifactDownload(workspace, path string) (*os.File, error) {
	if !filepath.IsAbs(workspace) || filepath.Clean(workspace) != workspace || !filepath.IsLocal(path) || filepath.Clean(path) != path || path == "." || strings.ContainsAny(path, "\\\x00") {
		return nil, errors.New("invalid artifact path")
	}
	fd, err := syscall.Open("/", syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_DIRECTORY, 0)
	if err != nil {
		return nil, err
	}
	components := strings.Split(strings.TrimPrefix(workspace, "/")+"/"+path, "/")
	for i, component := range components {
		flags := syscall.O_RDONLY | syscall.O_CLOEXEC | syscall.O_NOFOLLOW | syscall.O_NONBLOCK
		if i < len(components)-1 {
			flags |= syscall.O_DIRECTORY
		}
		next, openErr := syscall.Openat(fd, component, flags, 0)
		syscall.Close(fd)
		if openErr != nil {
			return nil, openErr
		}
		fd = next
	}
	return os.NewFile(uintptr(fd), "artifact"), nil
}
