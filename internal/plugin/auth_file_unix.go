//go:build unix

package plugin

import (
	"os"
	"syscall"
)

// Opening a FIFO must not wait for a writer: a planted pipe in the auth
// directory would otherwise stall registration.
func openCleanupRecord(root *os.Root, name string, flag int) (*os.File, error) {
	return root.OpenFile(name, flag|syscall.O_NONBLOCK, 0)
}
