// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 GSI Helmholtzzentrum für Schwerionenforschung GmbH

// Command slurm-temperature-check keeps a SLURM worker node from running jobs
// while one of its temperature sensors is over the limit configured for its
// mainboard.
//
// It does nothing privileged. It reads sysfs, and it exits non-zero when the
// node must stop; the unit's OnFailure= turns that into the emergency stop.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/GSI-HPC/slurm-temperature-check/internal/config"
	"github.com/GSI-HPC/slurm-temperature-check/internal/dmi"
	"github.com/GSI-HPC/slurm-temperature-check/internal/guard"
	"github.com/GSI-HPC/slurm-temperature-check/internal/hwmon"
	"github.com/GSI-HPC/slurm-temperature-check/internal/sdnotify"
)

// version is set at build time with -X main.version=...
var version = "devel"

// Exit codes. Every non-zero code arms the emergency stop, because every one
// of them means the node's temperature is no longer being watched. The codes
// are distinguished so the journal says which kind of failure it was.
const (
	exitOK      = 0 // asked to stop, or a one-shot mode succeeded
	exitTripped = 1 // the guard tripped: over the limit, or no reading
	exitConfig  = 2 // could not arm at all: bad configuration or unknown board
)

type options struct {
	configPath   string
	hwmonPath    string
	boardPath    string
	disablePath  string
	overridePath string
	interval     time.Duration
	readRetries  int
	logLevel     string
	logFormat    string
	list         bool
	check        bool
	showVersion  bool
}

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	var o options
	fs := flag.NewFlagSet("slurm-temperature-check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&o.configPath, "config", config.DefaultPath, "board table")
	fs.StringVar(&o.hwmonPath, "hwmon.path", hwmon.DefaultClassDir, "sysfs hwmon class directory")
	fs.StringVar(&o.boardPath, "board-name-path", dmi.DefaultPath, "sysfs file holding the DMI board name")
	fs.StringVar(&o.disablePath, "disable-file", "/etc/slurm-temperature-check/disable",
		"while this file exists, checking is suspended")
	fs.StringVar(&o.overridePath, "override-file", "/run/slurm-temperature-check/override",
		"while this file exists, it is read in place of the sensors")
	fs.DurationVar(&o.interval, "interval", 10*time.Second, "delay between checks")
	fs.IntVar(&o.readRetries, "read-retries", 2,
		"consecutive failed readings tolerated before tripping")
	fs.StringVar(&o.logLevel, "log.level", "info", "debug, info, warn or error")
	fs.StringVar(&o.logFormat, "log.format", "text", "text or json")
	fs.BoolVar(&o.list, "list", false, "print the node's temperature sensors and exit")
	fs.BoolVar(&o.check, "check", false,
		"resolve the configuration against this node, take one reading and exit")
	fs.BoolVar(&o.showVersion, "version", false, "print the version and exit")
	fs.Usage = func() {
		fmt.Fprintf(stderr, "Usage: slurm-temperature-check [flags]\n\n"+
			"Exits 0 when asked to stop, %d when the node must stop running jobs,\n"+
			"%d when it cannot arm at all.\n\nFlags:\n", exitTripped, exitConfig)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return exitConfig
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "unexpected argument %q\n", fs.Arg(0))
		return exitConfig
	}

	if o.showVersion {
		fmt.Fprintf(stdout, "slurm-temperature-check %s\n", version)
		return exitOK
	}

	log, err := newLogger(stderr, o.logLevel, o.logFormat)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitConfig
	}

	if o.interval <= 0 {
		log.Error("interval must be positive", "interval", o.interval.String())
		return exitConfig
	}
	if o.readRetries < 0 {
		log.Error("read-retries cannot be negative", "read_retries", o.readRetries)
		return exitConfig
	}

	if o.list {
		sensors, err := hwmon.Discover(o.hwmonPath)
		if err != nil {
			log.Error("cannot read the hwmon class", "error", err)
			return exitConfig
		}
		printSensors(stdout, sensors)
		return exitOK
	}

	// The disable file outranks everything, including whether the guard can
	// be armed at all. Specification row 1 says a node with the file present
	// keeps running whatever its sensors say, and a node that cannot arm is
	// where that matters most: exiting here arms the emergency stop exactly
	// as a trip does, so treating an arming failure as fatal while checking
	// is suspended kills the jobs on every start and every boot, and leaves
	// the operator no lever that stops it.
	g, armErr := arm(&o, log)
	if armErr != nil && !guard.Present(o.disablePath) {
		log.Error("cannot arm the guard", "error", armErr)
		return exitConfig
	}

	if o.check {
		if armErr != nil {
			fmt.Fprintf(stdout, "disable  %s\nverdict  %s\nreason   checking is suspended; "+
				"the guard cannot arm: %s\n", o.disablePath, guard.Disabled, armErr)
			return exitOK
		}
		res := g.Check()
		reading := "-"
		if res.HasReading {
			reading = res.Reading.String()
		}
		// The source is printed separately from the sensors, because the two
		// are not always the same thing: while the override file is present
		// it is read in place of the hardware, and reporting only the sensors
		// beside a reading that did not come from them is how a node with a
		// forgotten override looks guarded.
		fmt.Fprintf(stdout, "board    %s\nsensors  %s\nlimit    %s\nsource   %s\nreading  %s\nverdict  %s\n",
			g.Board, g.Sensors.Name(), g.Max, res.Source, reading, res.Verdict)
		if res.Err != nil {
			fmt.Fprintf(stdout, "reason   %s\n", res.Err)
		}
		// A failed reading is reported as a failure here even though the loop
		// would tolerate it: the retry budget is a property of running
		// repeatedly, and a single shot has none to spend. --check exists to
		// answer "would this node be guarded", and a reading it cannot take
		// is a no.
		if res.Verdict == guard.Trip || res.Verdict == guard.Tolerated {
			return exitTripped
		}
		return exitOK
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	notify := sdnotify.New()

	if armErr != nil {
		g, armErr = rearmWhenResumed(ctx, &o, notify, log, armErr)
		if armErr != nil {
			if ctx.Err() != nil {
				log.Info("asked to stop while checking was suspended, exiting cleanly")
				return exitOK
			}
			log.Error("cannot arm the guard", "error", armErr)
			return exitConfig
		}
	}

	pass := watchdog(ctx, notify, g.Interval, log)
	status := statusReporter(notify, g)
	g.Observe = func(res guard.Result) {
		pass()
		status(res)
	}
	notify.Ready(fmt.Sprintf("Watching %s, limit %s", g.Sensors.Name(), g.Max))

	if err := g.Run(ctx); err != nil {
		// The reason was already logged at error level by the guard; this
		// line is what a reader of `systemctl status` sees.
		notify.Status("Tripped: " + err.Error())
		return exitTripped
	}
	return exitOK
}

// rearmWhenResumed holds a node whose guard cannot arm but whose disable file
// is present, and tries again once that file is removed.
//
// Returning the reason instead of the guard means checking is no longer
// suspended and the node still cannot be guarded, at which point refusing to
// run is correct again.
func rearmWhenResumed(ctx context.Context, o *options, notify *sdnotify.Notifier,
	log *slog.Logger, why error) (*guard.Guard, error) {
	log.Warn("checking suspended: the disable file is present and the guard cannot arm",
		"path", o.disablePath, "error", why)
	notify.Ready("Suspended: " + o.disablePath + " is present; the guard cannot arm")

	// This loop is what keeps the process alive, so it is also what the
	// watchdog is watching; the keep-alive goes out at whichever of the two
	// periods is the shorter.
	every := o.interval
	if w := sdnotify.WatchdogInterval(); w > 0 && w < every {
		every = w
	}
	t := time.NewTicker(every)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil, why
		case <-t.C:
		}
		notify.Alive()
		if guard.Present(o.disablePath) {
			continue
		}
		g, err := arm(o, log)
		if err != nil {
			return nil, err
		}
		log.Info("the disable file is gone and the guard armed", "board", g.Board)
		return g, nil
	}
}

// arm resolves the configuration against this node and returns a guard.
func arm(o *options, log *slog.Logger) (*guard.Guard, error) {
	sensors, err := hwmon.Discover(o.hwmonPath)
	if err != nil {
		return nil, fmt.Errorf("read the hwmon class at %s: %w", o.hwmonPath, err)
	}
	if len(sensors) == 0 {
		return nil, fmt.Errorf("the node exports no temperature sensors at all (%s)", o.hwmonPath)
	}
	cfg, err := config.Load(o.configPath)
	if err != nil {
		return nil, err
	}
	board, err := dmi.BoardName(o.boardPath)
	if err != nil {
		return nil, err
	}
	entry, err := cfg.Lookup(board)
	if err != nil {
		return nil, err
	}
	selected, err := hwmon.Select(sensors, entry.Chip, entry.Sensor)
	if err != nil {
		return nil, fmt.Errorf("board %s (%s line %d): %w", board, o.configPath, entry.Line, err)
	}

	// The sensors are deliberately not read here. Select() has already proven
	// that the configured chip and attribute exist, which is what rows 11 and
	// 12 refuse to start for. An attribute that exists but cannot be read is
	// rows 7 and 8 instead -- "tolerate, then stop" -- and reading once at
	// startup turned those into a refusal to start: a single transient bus
	// error while the node was still coming up became fatal, where the very
	// same error one interval later is absorbed twice over by the retry
	// budget. Both paths end with the node out of service if the sensor
	// really is gone, so nothing is lost by letting the loop decide it.
	return &guard.Guard{
		Board:        board,
		Max:          entry.Max,
		Sensors:      guard.NewSensors(selected),
		DisablePath:  o.disablePath,
		OverridePath: o.overridePath,
		Interval:     o.interval,
		ReadRetries:  o.readRetries,
		Log:          log,
	}, nil
}

// statusReporter returns an observer that keeps the line `systemctl status`
// shows in step with the loop.
//
// The line sent with READY= describes the guard as it was armed and nothing
// updated it afterwards, so a node reading the override file went on
// advertising its hwmon sensors for as long as it ran. It is deliberately
// free of the reading itself, so that a datagram is sent when the state
// changes rather than on every pass.
func statusReporter(notify *sdnotify.Notifier, g *guard.Guard) func(guard.Result) {
	last := ""
	return func(res guard.Result) {
		var line string
		switch res.Verdict {
		case guard.Disabled:
			line = "Suspended: " + res.Source + " is present"
		case guard.Tolerated:
			line = "Reading " + res.Source + " failed, within the retry budget"
		default:
			line = fmt.Sprintf("Watching %s, limit %s", res.Source, g.Max)
		}
		if line == last {
			return
		}
		last = line
		notify.Status(line)
	}
}

// watchdog wires the unit's WatchdogSec= to the progress of the check loop.
//
// The keep-alive is sent by a ticker of its own, but only while the loop has
// completed a pass recently. A ping sent unconditionally would keep the
// service alive through a wedged loop, which is the one thing the watchdog
// exists to catch. The returned function is what the loop calls to record a
// pass; it is a no-op when the unit sets no WatchdogSec=.
func watchdog(ctx context.Context, notify *sdnotify.Notifier, interval time.Duration, log *slog.Logger) func() {
	every := sdnotify.WatchdogInterval()
	if every <= 0 {
		return func() {}
	}

	var lastPass atomic.Int64
	lastPass.Store(time.Now().UnixNano())

	// A pass is overdue once two intervals have gone by without one.
	stale := 2 * interval
	if stale < 2*every {
		stale = 2 * every
	}
	log.Debug("watchdog enabled", "ping_every", every.String(), "stale_after", stale.String())

	go func() {
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				since := time.Since(time.Unix(0, lastPass.Load()))
				if since > stale {
					log.Error("the check loop has not completed a pass, withholding the watchdog keep-alive",
						"since_last_pass", since.String())
					continue
				}
				notify.Alive()
			}
		}
	}()

	return func() { lastPass.Store(time.Now().UnixNano()) }
}

func printSensors(w io.Writer, sensors []hwmon.Sensor) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "CHIP\tSENSOR\tLABEL\tREADING")
	for _, s := range sensors {
		reading := "-"
		if v, err := s.Read(); err == nil {
			reading = v.String()
		}
		label := s.Label
		if label == "" {
			label = "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", s.Chip, s.Attr, label, reading)
	}
	_ = tw.Flush()
}

func newLogger(w io.Writer, level, format string) (*slog.Logger, error) {
	var l slog.Level
	switch strings.ToLower(level) {
	case "debug":
		l = slog.LevelDebug
	case "info":
		l = slog.LevelInfo
	case "warn", "warning":
		l = slog.LevelWarn
	case "error":
		l = slog.LevelError
	default:
		return nil, fmt.Errorf("unknown log level %q", level)
	}

	// Under systemd the journal records the timestamp and the unit, so the
	// handler omits both and emits only the level, the message and the
	// attributes.
	opts := &slog.HandlerOptions{
		Level: l,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if len(groups) == 0 && a.Key == slog.TimeKey {
				return slog.Attr{}
			}
			return a
		},
	}
	switch strings.ToLower(format) {
	case "text":
		return slog.New(slog.NewTextHandler(w, opts)), nil
	case "json":
		return slog.New(slog.NewJSONHandler(w, opts)), nil
	}
	return nil, fmt.Errorf("unknown log format %q", format)
}
