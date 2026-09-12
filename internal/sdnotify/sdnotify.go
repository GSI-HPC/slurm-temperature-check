// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GSI Helmholtzzentrum für Schwerionenforschung GmbH

// Package sdnotify speaks the parts of sd_notify(3) this program needs:
// readiness, status and the watchdog keep-alive.
//
// It is the replacement for the stale-file check a two-process design needs.
// Where a separate reader process had to be watched by its consumer through
// the mtime of the file between them, a single process is watched by systemd
// itself: WatchdogSec= in the unit, a keep-alive from the check loop, and a
// guard that wedges is killed by systemd and lands in the failed state that
// arms the emergency stop.
//
// Everything here is a no-op when the environment does not offer a socket, so
// the program runs identically outside systemd.
package sdnotify

import (
	"net"
	"os"
	"strconv"
	"time"
)

// Notifier sends messages to the service manager.
type Notifier struct {
	conn *net.UnixConn
}

// New connects to $NOTIFY_SOCKET. A nil Notifier is valid and silent, which
// is what New returns when the variable is unset.
func New() *Notifier {
	addr := os.Getenv("NOTIFY_SOCKET")
	if addr == "" {
		return nil
	}
	// A leading "@" marks an abstract socket, which Go spells with a leading
	// NUL byte.
	if addr[0] == '@' {
		addr = "\x00" + addr[1:]
	}
	conn, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: addr, Net: "unixgram"})
	if err != nil {
		return nil
	}
	return &Notifier{conn: conn}
}

// Send writes one newline-terminated datagram. Failures are ignored: losing
// the notification socket must never take the guard down with it.
func (n *Notifier) Send(state string) {
	if n == nil || n.conn == nil {
		return
	}
	_, _ = n.conn.Write([]byte(state + "\n"))
}

// Ready reports that the guard is armed, with a one-line status.
func (n *Notifier) Ready(status string) { n.Send("READY=1\nSTATUS=" + status) }

// Status updates the line `systemctl status` shows.
func (n *Notifier) Status(status string) { n.Send("STATUS=" + status) }

// Alive sends the watchdog keep-alive.
func (n *Notifier) Alive() { n.Send("WATCHDOG=1") }

// WatchdogInterval returns how often Alive must be called, or zero when the
// unit sets no WatchdogSec=.
//
// systemd kills the service after WATCHDOG_USEC without a keep-alive, so the
// documented practice is to ping at half that. WATCHDOG_PID, when set, names
// the process the timeout applies to; a value that is not this process means
// the variables were inherited rather than addressed to us.
func WatchdogInterval() time.Duration {
	if pid := os.Getenv("WATCHDOG_PID"); pid != "" && pid != strconv.Itoa(os.Getpid()) {
		return 0
	}
	usec, err := strconv.ParseInt(os.Getenv("WATCHDOG_USEC"), 10, 64)
	if err != nil || usec <= 0 {
		return 0
	}
	return time.Duration(usec) * time.Microsecond / 2
}
