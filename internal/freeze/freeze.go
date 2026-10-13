// Package freeze sets and clears the Linux immutable attribute (chattr +i).
package freeze

import (
	"os"

	"golang.org/x/sys/unix"
)

// fsImmutableFl is FS_IMMUTABLE_FL from linux/fs.h.
const fsImmutableFl = 0x00000010

// Freezer freezes and melts files. The engine uses an interface so tests can
// run without root.
type Freezer interface {
	Freeze(path string) error
	Melt(path string) error
	IsFrozen(path string) (bool, error)
}

// Linux uses the FS_IOC_SETFLAGS ioctl. It needs CAP_LINUX_IMMUTABLE and a
// filesystem that supports the flag (ext4, XFS, btrfs).
type Linux struct{}

func (Linux) Freeze(path string) error { return setFlag(path, true) }
func (Linux) Melt(path string) error   { return setFlag(path, false) }

func (Linux) IsFrozen(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	flags, err := unix.IoctlGetUint32(int(f.Fd()), unix.FS_IOC_GETFLAGS)
	if err != nil {
		return false, &os.PathError{Op: "getflags", Path: path, Err: err}
	}
	return flags&fsImmutableFl != 0, nil
}

func setFlag(path string, on bool) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	fd := int(f.Fd())
	flags, err := unix.IoctlGetUint32(fd, unix.FS_IOC_GETFLAGS)
	if err != nil {
		return &os.PathError{Op: "getflags", Path: path, Err: err}
	}
	next := flags &^ fsImmutableFl
	if on {
		next = flags | fsImmutableFl
	}
	if next == flags {
		return nil
	}
	if err := unix.IoctlSetPointerInt(fd, unix.FS_IOC_SETFLAGS, int(next)); err != nil {
		return &os.PathError{Op: "setflags", Path: path, Err: err}
	}
	return nil
}
