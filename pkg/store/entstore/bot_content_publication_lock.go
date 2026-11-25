package entstore

import (
	"context"
	"errors"
	"os"

	"github.com/SAP/astonish/pkg/store"
	"golang.org/x/sys/unix"
)

// lockPublication coordinates every staged publisher and maintenance process.
// The lock inode lives outside the namespace and is NEVER unlinked (including
// purge): replacing it would permit two processes to own different locks.
// Always try, never wait. A publisher's barrier may acquire SQL while maintenance
// already holds SQL; blocking here would invert that ordering and deadlock.
func (d *botDiskContent) lockPublication(ctx context.Context) (*os.File, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	name := ".publish-" + d.namespace()
	f, err := d.root.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0600)
	if errors.Is(err, os.ErrExist) {
		// The permanent lock inode may have been created by another publisher.
		// Open it without O_CREATE: concurrent nonexclusive creation with
		// O_NOFOLLOW can report ENOENT on Darwin even though the inode exists.
		f, err = d.root.OpenFile(name, os.O_RDWR|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	}
	if err != nil {
		return nil, err
	}
	var stat unix.Stat_t
	if err = unix.Fstat(int(f.Fd()), &stat); err == nil && (stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1) {
		err = store.ErrBotConflict
	}
	if err == nil {
		err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	}
	if err != nil {
		_ = f.Close()
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, store.ErrBotBusy
		}
		return nil, err
	}
	return f, nil // closing the descriptor releases flock, including on process exit
}
