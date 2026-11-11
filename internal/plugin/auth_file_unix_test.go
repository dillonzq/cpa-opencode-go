//go:build unix

package plugin

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestCleanupOpenDoesNotWaitForFIFO(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pipe.json")
	if err := syscall.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	done := make(chan error, 1)
	go func() {
		file, err := openCleanupRecord(root, "pipe.json", os.O_RDONLY)
		if err == nil {
			err = file.Close()
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		// Unblock a broken blocking-open implementation before failing.
		if writer, err := os.OpenFile(path, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
			_ = writer.Close()
		}
		<-done
		t.Fatal("cleanup open blocked on a FIFO")
	}
}
