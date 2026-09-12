// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GSI Helmholtzzentrum für Schwerionenforschung GmbH

package sdnotify

import (
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestWatchdogInterval(t *testing.T) {
	self := strconv.Itoa(os.Getpid())
	tests := []struct {
		desc string
		usec string
		pid  string
		want time.Duration
	}{
		{
			desc: "half of WATCHDOG_USEC, as sd_notify(3) documents",
			usec: "60000000",
			pid:  self,
			want: 30 * time.Second,
		},
		{
			desc: "WATCHDOG_PID may be omitted",
			usec: "20000000",
			want: 10 * time.Second,
		},
		{
			desc: "variables inherited from another process are not ours",
			usec: "60000000",
			pid:  "1",
			want: 0,
		},
		{desc: "unset means no watchdog", usec: "", pid: "", want: 0},
		{desc: "a non-numeric value means no watchdog", usec: "soon", pid: self, want: 0},
		{desc: "zero means no watchdog", usec: "0", pid: self, want: 0},
		{desc: "a negative value means no watchdog", usec: "-1", pid: self, want: 0},
	}

	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			t.Setenv("WATCHDOG_USEC", tc.usec)
			t.Setenv("WATCHDOG_PID", tc.pid)
			if got := WatchdogInterval(); got != tc.want {
				t.Errorf("WatchdogInterval() = %s, want %s", got, tc.want)
			}
		})
	}
}

// TestNotifierIsSilentWithoutASocket is the property that lets the program be
// run by hand, and under --check, exactly as it runs under systemd.
func TestNotifierIsSilentWithoutASocket(t *testing.T) {
	t.Setenv("NOTIFY_SOCKET", "")
	n := New()
	if n != nil {
		t.Fatalf("New() = %v with no socket, want nil", n)
	}
	// Every method has to be safe on the nil receiver.
	n.Ready("armed")
	n.Status("still here")
	n.Alive()
	n.Send("WATCHDOG=1")
}

func TestNotifierSends(t *testing.T) {
	// A socket in the filesystem rather than an abstract one: the path length
	// limit for a unix socket is about 100 bytes, which t.TempDir() fits.
	path := filepath.Join(t.TempDir(), "notify")
	conn, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: path, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	t.Setenv("NOTIFY_SOCKET", path)
	n := New()
	if n == nil {
		t.Fatal("New() = nil, want a notifier")
	}

	read := func() string {
		t.Helper()
		if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, 512)
		count, _, err := conn.ReadFromUnix(buf)
		if err != nil {
			t.Fatal(err)
		}
		return string(buf[:count])
	}

	n.Ready("armed")
	if got, want := read(), "READY=1\nSTATUS=armed\n"; got != want {
		t.Errorf("Ready sent %q, want %q", got, want)
	}
	n.Alive()
	if got, want := read(), "WATCHDOG=1\n"; got != want {
		t.Errorf("Alive sent %q, want %q", got, want)
	}
	n.Status("tripped")
	if got, want := read(), "STATUS=tripped\n"; got != want {
		t.Errorf("Status sent %q, want %q", got, want)
	}
}

// TestNotifierSurvivesAClosedSocket: losing the service manager's socket must
// never take the guard down with it.
func TestNotifierSurvivesAClosedSocket(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notify")
	conn, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: path, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("NOTIFY_SOCKET", path)
	n := New()
	if n == nil {
		t.Fatal("New() = nil, want a notifier")
	}
	conn.Close()
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	n.Alive()
	n.Ready("armed")
}

// TestNewWithUnreachableSocket covers a NOTIFY_SOCKET that names nothing.
func TestNewWithUnreachableSocket(t *testing.T) {
	t.Setenv("NOTIFY_SOCKET", filepath.Join(t.TempDir(), "absent"))
	if n := New(); n != nil {
		t.Errorf("New() = %v for an unreachable socket, want nil", n)
	}
}
