// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"fmt"
	"strconv"
	"time"

	"github.com/cB-Sigma/iec60870/cs104"
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

func (a *args) flag(name string) bool {
	for i, v := range a.rest {
		if v == name {
			a.rest = append(a.rest[:i], a.rest[i+1:]...)
			return true
		}
	}
	return false
}

// finish fails when an argument was not consumed.
func (a *args) finish(context string) error {
	if len(a.rest) > 0 {
		return usagef("%s: unknown argument %s", context, a.rest[0])
	}
	return nil
}

// apci reads the APCI flags, which are in the seconds of the contract.
func (a *args) apci() (cs104.LinkConfig, error) {
	var cfg cs104.LinkConfig
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
	// cB-Sigma takes a timer of 0 as "use the default" and cannot switch the
	// idle test off: refuse it rather than run with another value.
	if t3 < 1 {
		return cfg, usagef("--t3: cB-Sigma/iec60870 cannot switch the idle test off; t3 must be 1 or more")
	}
	cfg = cs104.LinkConfig{K: k, W: w, T0: time.Duration(t0) * time.Second, T1: time.Duration(t1) * time.Second,
		T2: time.Duration(t2) * time.Second, T3: time.Duration(t3) * time.Second}
	// The library has its own limits (t0 to t2 at most 255 s, for one).
	if err := cfg.Validate(); err != nil {
		return cfg, usagef("APCI parameters not accepted by cB-Sigma/iec60870: %v", err)
	}
	return cfg, nil
}

func sprintf(format string, a ...any) string { return fmt.Sprintf(format, a...) }
