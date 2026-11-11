//go:build !unix

package plugin

import "os"

func openCleanupRecord(root *os.Root, name string, flag int) (*os.File, error) {
	return root.OpenFile(name, flag, 0)
}
