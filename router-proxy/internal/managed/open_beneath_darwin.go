//go:build darwin

package managed

import (
	"golang.org/x/sys/unix"
)

func openRootDirectory(cwd string) (int, error) {
	return unix.Open(cwd, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
}

func openBeneath(rootFD int, relPath string) (int, error) {
	return unix.Openat(rootFD, relPath, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
}

func renameatNoReplace(olddirfd int, oldpath string, newdirfd int, newpath string) error {
	var st unix.Stat_t
	if err := unix.Fstatat(newdirfd, newpath, &st, unix.AT_SYMLINK_NOFOLLOW); err == nil {
		return unix.EEXIST
	}
	return unix.Renameat(olddirfd, oldpath, newdirfd, newpath)
}
