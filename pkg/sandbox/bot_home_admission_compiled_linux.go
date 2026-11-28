//go:build linux

package sandbox

import (
	"bufio"
	"errors"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

// RunBotHomeAdmission holds the retained private volume's existing lock inode
// until release or stdin closure. The caller owns the helper's entire lifetime;
// this is not a routine file executor, pool, or persistent node server.
func RunBotHomeAdmission(mode BotHomeAdmissionMode, input *os.File, output io.Writer) error {
	fd, err := unix.Open("/private-files", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	root := os.NewFile(uintptr(fd), "admission-private")
	defer root.Close()
	return runBotHomeAdmission(root, mode, input, output)
}

func runBotHomeAdmission(root *os.File, mode BotHomeAdmissionMode, input *os.File, output io.Writer) error {
	if mode != BotHomeAdmissionWait && mode != BotHomeAdmissionTry {
		return errors.New("invalid admission mode")
	}
	if input == nil {
		return errors.New("missing admission input")
	}
	lock, err := homeOpen(root, ".workspace-lock", unix.O_RDWR|unix.O_CREAT, 0600)
	if err != nil {
		return err
	}
	defer lock.Close() // Never unlink or replace the shared inode.
	s, err := homeStat(lock, true)
	if err != nil {
		return err
	}
	r, err := homeStat(root, false)
	if err != nil {
		return err
	}
	if r.Mode&unix.S_IFMT != unix.S_IFDIR || s.Dev != r.Dev {
		return errHomeForbidden
	}
	for {
		err = unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			break
		}
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if !errors.Is(err, unix.EWOULDBLOCK) {
			return err
		}
		// Archive can already hold SQL's writer fence. Never wait behind a private
		// writer that needs that fence at its commit barrier.
		if mode == BotHomeAdmissionTry {
			_, err = io.WriteString(output, "{\"busy\":true}\n")
			return err
		}
		fds := []unix.PollFd{{Fd: int32(input.Fd()), Events: unix.POLLIN}}
		n, err := unix.Poll(fds, 50)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return err
		}
		if n != 0 {
			return errors.New("admission cancelled")
		}
	}
	if _, err = io.WriteString(output, "{\"ready\":true}\n"); err != nil {
		return err
	}
	// Bound malformed release input exactly as the previous readline(16).
	line, err := bufio.NewReader(io.LimitReader(input, 16)).ReadString('\n')
	if err != nil || line != "release\n" {
		return errors.New("admission cancelled")
	}
	return nil
}
