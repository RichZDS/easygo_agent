//go:build linux

package workshop

import (
	"context"
	"errors"
	"io"
	"math"
	"os"

	"golang.org/x/sys/unix"
)

type diskInode struct {
	device uint64
	inode  uint64
}

// Descriptor-relative lstat traversal never follows links, even if an active
// task swaps an entry between stat and open. Directory reads are bounded batches.
// Entries count separately; hard links share one allocated-block charge.
func scanDiskUsage(ctx context.Context, workspace string, limitBytes, limitFiles int64) (usage DiskUsage, err error) {
	if limitBytes < 1 || limitFiles < 1 {
		return usage, ErrDiskQuotaScanFailed
	}
	if err = noSymlinks(workspace); err != nil {
		return usage, ErrDiskQuotaScanFailed
	}
	fd, e := unix.Open(workspace, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if e != nil {
		return usage, ErrDiskQuotaScanFailed
	}
	root := os.NewFile(uintptr(fd), "quota-root")
	defer root.Close()
	seen := map[diskInode]bool{}
	exceeded := func() error { return &DiskQuotaError{DiskUsage: usage, LimitBytes: limitBytes, LimitFiles: limitFiles} }
	charge := func(st *unix.Stat_t) error {
		key := diskInode{uint64(st.Dev), uint64(st.Ino)}
		if !seen[key] {
			seen[key] = true
			if st.Blocks < 0 || st.Blocks > math.MaxInt64/512 || usage.UsedBytes > math.MaxInt64-st.Blocks*512 {
				return ErrDiskQuotaScanFailed
			}
			usage.UsedBytes += st.Blocks * 512
		}
		if usage.UsedBytes > limitBytes {
			return exceeded()
		}
		return nil
	}
	var st unix.Stat_t
	if e = unix.Fstat(fd, &st); e != nil {
		return usage, ErrDiskQuotaScanFailed
	}
	usage.UsedFiles = 1 // Include the workspace directory itself.
	if err = charge(&st); err != nil {
		return usage, err
	}
	var walk func(*os.File) error
	walk = func(dir *os.File) error {
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			names, readErr := dir.Readdirnames(128)
			for _, name := range names {
				if err := ctx.Err(); err != nil {
					return err
				}
				usage.UsedFiles++
				if usage.UsedFiles > limitFiles {
					return exceeded()
				}
				var entry unix.Stat_t
				if unix.Fstatat(int(dir.Fd()), name, &entry, unix.AT_SYMLINK_NOFOLLOW) != nil {
					return ErrDiskQuotaScanFailed
				}
				if err := charge(&entry); err != nil {
					return err
				}
				if entry.Mode&unix.S_IFMT == unix.S_IFDIR {
					childFD, e := unix.Openat(int(dir.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
					if e != nil {
						return ErrDiskQuotaScanFailed
					}
					var actual unix.Stat_t
					if e = unix.Fstat(childFD, &actual); e != nil || actual.Dev != entry.Dev || actual.Ino != entry.Ino {
						unix.Close(childFD)
						return ErrDiskQuotaScanFailed
					}
					child := os.NewFile(uintptr(childFD), "quota-directory")
					e = walk(child)
					closeErr := child.Close()
					if e != nil {
						return e
					}
					if closeErr != nil {
						return ErrDiskQuotaScanFailed
					}
				}
			}
			if errors.Is(readErr, io.EOF) {
				return nil
			}
			if readErr != nil {
				return ErrDiskQuotaScanFailed
			}
		}
	}
	err = walk(root)
	return usage, err
}
