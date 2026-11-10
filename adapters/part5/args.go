// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"fmt"
	"strconv"
	"time"

	"github.com/pascaldekloe/part5/session"
)

// usageError is anything the caller got wrong; it maps to exit code 2.
type usageError struct{ msg string }

func (e usageError) Error() string { return e.msg }

func usagef(format string, a ...any) error { return usageError{fmt.Sprintf(format, a...)} }

// args are command-line options of the form "--name value" and bare "--flag".
type args struct{ rest []string }

func (a *args) str(name, def string) string {
	for i, v := range a.rest {
		if v == name && i+1 < len(a.rest) {
			val := a.rest[i+1]
			a.rest = append(a.rest[:i], a.rest[i+2:]...)
			return val
		}
	}
	return def
}

func (a *args) num(name string, def int) (int, error) {
	s := a.str(name, "")
	if s == "" {
		return def, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, usagef("%s: '%s' is not an integer", name, s)
	}
	return n, nil
}

// finish fails when an argument was not consumed.
func (a *args) finish(context string) error {
	if len(a.rest) > 0 {
		return usagef("%s: unknown argument %s", context, a.rest[0])
	}
	return nil
}

// apci reads the APCI flags, which are in the seconds of the contract.
//
// part5 panics on a timer outside 1..255 seconds and takes 0 for "use the
// default", so it cannot switch the idle test off: both are usage errors here.
// t0 is the connection establishment timeout; part5 does not dial, so the
// adapter applies it (and --connect-timeout-ms) itself.
func (a *args) apci() (session.TCPConfig, error) {
	var cfg session.TCPConfig
	var vals [6]int
	for i, f := range []struct {
		name string
		def  int
	}{{"--k", 12}, {"--w", 8}, {"--t0", 30}, {"--t1", 15}, {"--t2", 10}, {"--t3", 20}} {
		v, err := a.num(f.name, f.def)
		if err != nil {
			return cfg, err
		}
		vals[i] = v
	}
	k, w, t0, t1, t2, t3 := vals[0], vals[1], vals[2], vals[3], vals[4], vals[5]
	if k < 1 || k > 32767 || w < 1 || w > k || t0 < 1 || t1 < 1 || t2 < 1 || t2 >= t1 || t3 < 0 {
		return cfg, usagef("invalid APCI parameters: need 1 <= w <= k <= 32767, t0 > 0, 0 < t2 < t1, t3 >= 0 (seconds)")
	}
	if t3 < 1 {
		return cfg, usagef("--t3: part5 cannot switch the idle test off; t3 must be 1 or more")
	}
	if t0 > 255 || t1 > 255 || t2 > 255 {
		return cfg, usagef("part5 takes t0, t1 and t2 between 1 and 255 seconds")
	}
	cfg.ConnectTimeout = time.Duration(t0) * time.Second
	cfg.SendUnackMax = uint(k)
	cfg.RecvUnackMax = uint(w)
	cfg.SendUnackTimeout = time.Duration(t1) * time.Second
	cfg.RecvUnackTimeout = time.Duration(t2) * time.Second
	cfg.IdleTimeout = time.Duration(t3) * time.Second
	return cfg, nil
}

func sprintf(format string, a ...any) string { return fmt.Sprintf(format, a...) }
