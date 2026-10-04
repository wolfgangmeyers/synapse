//go:build linux

package managed

import (
	"golang.org/x/sys/unix"
)

func openRootDirectory(cwd string) (int, error) {
	return unix.Open(cwd, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
}

func openBeneath(rootFD int, relPath string) (int, error) {
	return unix.Openat2(rootFD, relPath, &unix.OpenHow{
		Flags:   unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK,
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS,
	})
}

func renameatNoReplace(olddirfd int, oldpath string, newdirfd int, newpath string) error {
	return unix.Renameat2(olddirfd, oldpath, newdirfd, newpath, unix.RENAME_NOREPLACE)
}

