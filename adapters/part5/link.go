// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"errors"
	"net"
	"sync"
	"time"

	"github.com/pascaldekloe/part5/info"
	"github.com/pascaldekloe/part5/session"
)

// The parameters of IEC 60870-5-104: a one-octet originator address next to
// the cause, a two-octet common address, a three-octet object address.
type (
	sys  = info.System[info.OrigAddr8, info.ComAddr16, info.ObjAddr24]
	unit = info.DataUnit[info.OrigAddr8, info.ComAddr16, info.ObjAddr24]
	oa8  = info.OrigAddr8
	ca16 = info.ComAddr16
	ioa3 = info.ObjAddr24
)

var params sys

func newUnit(t info.TypeID, count int, cause info.Cause, oa uint, ca uint) unit {
	u := params.NewDataUnit()
	u.Type = t
	u.Enc = info.Enc(count)
	u.Cause = cause
	u.Orig, _ = params.OrigAddrN(oa)
	u.Addr, _ = params.ComAddrN(ca)
	return u
}

func appendIOA(u *unit, n int) {
	a, _ := params.ObjAddrN(uint(n))
	u.Info = append(u.Info, a[:]...)
}

// link wraps a session.Station of part5.
//
// The part5 engine hands over everything on unbuffered channels from its own
// state machine: an application that is slow to read In, Level or Err stalls
// the engine, including its timers and its reading of Class1/Class2. So every
// channel is drained right away into queues, and the application works from
// those.
type link struct {
	st   *session.Station
	peer string

	mu      sync.Mutex
	changed chan struct{} // closed and replaced on every change
	level   session.Level
	ups     int // Level Up reports
	downs   int // Level Down reports after an Up
	closed  bool
	inbox   [][]byte
	done    chan struct{} // closed when part5 closed Level
	ended   chan struct{} // closed when part5 closed every channel, before done

	onLevel func(session.Level) // optional, called in order from one goroutine
	onASDU  func([]byte)        // optional, called in order from one goroutine
}

func newLink(cfg session.TCPConfig, conn net.Conn, onLevel func(session.Level), onASDU func([]byte)) *link {
	l := &link{peer: conn.RemoteAddr().String(), changed: make(chan struct{}), done: make(chan struct{}), ended: make(chan struct{}),
		onLevel: onLevel, onASDU: onASDU}
	l.st = session.TCP(cfg, conn)

	type item struct {
		lvl   *session.Level
		asdu  []byte
		final bool
	}
	queue := make(chan struct{}, 1)
	var pending []item
	push := func(it item) {
		l.mu.Lock()
		pending = append(pending, it)
		l.mu.Unlock()
		select {
		case queue <- struct{}{}:
		default:
		}
	}
	// One goroutine per part5 channel, each only reading.
	var readers sync.WaitGroup
	readers.Add(3)
	go func() {
		defer readers.Done()
		for lv := range l.st.Level {
			lv := lv
			push(item{lvl: &lv})
		}
	}()
	go func() {
		defer readers.Done()
		for p := range l.st.In {
			push(item{asdu: append([]byte(nil), p...)})
		}
	}()
	go func() {
		defer readers.Done()
		for err := range l.st.Err {
			logf("%s: %v", l.peer, err)
		}
	}()
	go func() {
		readers.Wait()
		close(l.ended)
		push(item{final: true})
	}()
	// The application side, in the order part5 reported.
	go func() {
		for range queue {
			for {
				l.mu.Lock()
				if len(pending) == 0 {
					l.mu.Unlock()
					break
				}
				it := pending[0]
				pending = pending[1:]
				l.mu.Unlock()
				switch {
				case it.final:
					l.update(func() { l.closed = true })
					close(l.done)
					return
				case it.lvl != nil:
					lv := *it.lvl
					l.update(func() {
						if lv == session.Up {
							l.ups++
						} else if lv == session.Down && l.level == session.Up {
							l.downs++
						}
						l.level = lv
					})
					if l.onLevel != nil {
						l.onLevel(lv)
					}
				default:
					if l.onASDU != nil {
						l.onASDU(it.asdu)
					} else {
						l.update(func() { l.inbox = append(l.inbox, it.asdu) })
					}
				}
			}
		}
	}()
	return l
}

func (l *link) update(f func()) {
	l.mu.Lock()
	f()
	close(l.changed)
	l.changed = make(chan struct{})
	l.mu.Unlock()
}

// waitFor waits until cond holds, the session ends or the timeout passes.
func (l *link) waitFor(cond func() bool, timeout time.Duration) bool {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		l.mu.Lock()
		ok, closed, changed := cond(), l.closed, l.changed
		l.mu.Unlock()
		if ok || closed {
			return ok
		}
		select {
		case <-changed:
		case <-deadline.C:
			l.mu.Lock()
			defer l.mu.Unlock()
			return cond()
		}
	}
}

func (l *link) isClosed() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.closed
}

var errLinkClosed = errors.New("the connection is closed")

// target asks part5 for a level: Up sends STARTDT act, Down STOPDT act, Exit
// closes. part5 reads Target only while its session runs.
func (l *link) target(lv session.Level) error {
	select {
	case l.st.Target <- lv:
		return nil
	case <-l.done:
		return errLinkClosed
	}
}

// send hands an ASDU to part5, which takes it once data transfer is started
// and fewer than k I frames are unacknowledged.
func (l *link) send(u unit, timeout time.Duration) error {
	o := session.NewOutbound(u.Append(nil))
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case l.st.Class2 <- o:
		return nil
	case <-l.done:
		return errLinkClosed
	case <-t.C:
		return errors.New("part5 did not take the ASDU in time")
	}
}

// endsSoon reports whether the session ends within a short while. part5
// reports Down both for STOPDT and when a started session closes; only the
// first is followed by more of the session.
func (l *link) endsSoon() bool {
	select {
	case <-l.ended:
		return true
	case <-time.After(100 * time.Millisecond):
		return false
	}
}

// close ends the session and waits a while for part5 to wind it down.
func (l *link) close() {
	_ = l.target(session.Exit)
	select {
	case <-l.done:
	case <-time.After(2 * time.Second):
	}
}
