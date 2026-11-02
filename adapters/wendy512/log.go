// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"os"
	"sync"
)

// libraryLog receives the log of go-iecp5 and sends it to stderr.
//
// go-iecp5 reports a few things only here: that the listener is up, that it
// could not listen, that a connection attempt failed, and that a client dropped
// an I frame outside data transfer. watch lets the adapter wait for those by
// their format string.
type libraryLog struct {
	debug bool
	mu    sync.Mutex
	watch map[string]chan struct{}
	seenN map[string]int
	// rawFrame, when set before go-iecp5 starts, gets every APDU it receives.
	rawFrame func([]byte)
}

func newLibraryLog() *libraryLog {
	return &libraryLog{debug: os.Getenv("IEC104_INTEROP_DEBUG") != "", watch: map[string]chan struct{}{}, seenN: map[string]int{}}
}

// seen returns a channel that is closed the first time format is logged.
func (l *libraryLog) seen(format string) <-chan struct{} {
	l.mu.Lock()
	defer l.mu.Unlock()
	ch, ok := l.watch[format]
	if !ok {
		ch = make(chan struct{})
		l.watch[format] = ch
	}
	return ch
}

// count says how many times format was logged.
func (l *libraryLog) count(format string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.seenN[format]
}

func (l *libraryLog) note(format string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.seenN[format]++
	if ch, ok := l.watch[format]; ok {
		select {
		case <-ch:
		default:
			close(ch)
		}
	}
}

func (l *libraryLog) Critical(format string, v ...interface{}) {
	l.note(format)
	logf("go-iecp5: "+format, v...)
}

func (l *libraryLog) Error(format string, v ...interface{}) {
	l.note(format)
	logf("go-iecp5: "+format, v...)
}

func (l *libraryLog) Warn(format string, v ...interface{}) {
	l.note(format)
	logf("go-iecp5: "+format, v...)
}

func (l *libraryLog) Debug(format string, v ...interface{}) {
	l.note(format)
	if format == "RX Raw[% x]" && l.rawFrame != nil && len(v) == 1 {
		if apdu, ok := v[0].([]byte); ok {
			l.rawFrame(apdu)
		}
	}
	if l.debug {
		logf("go-iecp5: "+format, v...)
	}
}
