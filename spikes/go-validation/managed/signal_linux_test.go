//go:build linux

package managed

import (
	"os"
	"os/signal"

	"golang.org/x/sys/unix"
)

func signalNotify(channel chan<- os.Signal) {
	signal.Notify(channel, unix.SIGHUP, unix.SIGTERM, unix.SIGINT)
}
