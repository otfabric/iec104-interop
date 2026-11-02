// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"errors"
	"sync"
	"time"

	iec "github.com/cB-Sigma/iec60870"
	"github.com/cB-Sigma/iec60870/cs104"
)

// redundancyGroup is --redundancy-group: one started connection, and the event a
// STOPDT queues for the next one to start.
//
// This drives cs104.Accept itself, as cs104.Group's documentation allows for
// other rules, because the group of the contract is not that Group: Group.started
// closes the connection it replaces even when that one is in STOPDT, so a
// controlling station sees its primary go before the standby's STARTDT con (the
// contract's connection-lost), and Group.reclaim sends every I frame the old
// connection had not had acknowledged again on the new one, answers to its
// requests included, where the contract has the answer go to the connection that
// asked. The one thing taken over is that a started connection is closed when
// another starts.
type redundancyGroup struct {
	mu      sync.Mutex
	active  *cs104.Link
	noted   bool   // the STOPDT of active is queued
	pending []byte // the event waiting for a start
}

// stoppedLocked queues the event of a STOPDT on the started connection, once. It
// is called with g.mu held.
func (s *station) stoppedLocked(g *redundancyGroup) {
	if g.noted {
		return
	}
	g.noted = true
	s.mu.Lock()
	var event *iec.ASDU
	for _, p := range s.fx.Points { // in ascending address order
		if p.typ == iec.MSpNa1 {
			event = s.pointASDU([]*point{p}, false, iec.CauseSpontaneous, 0)
			break
		}
	}
	s.mu.Unlock()
	if event == nil {
		return
	}
	raw, err := event.Encode(params, time.UTC)
	if err != nil {
		logf("cannot encode the queued event: %v", err)
		return
	}
	g.pending = raw
}

// linkStopped reports the STOPDT the state watch of a connection saw.
func (s *station) linkStopped(l *cs104.Link) {
	g := s.group
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.active == l {
		s.stoppedLocked(g)
	}
}

// linkStarted runs in the link's loop on STARTDT act, before the confirmation
// goes out. The watch of the connection before it looks every 2 ms, so a STOPDT
// it has not seen yet is taken into account here. A link that ended stays
// "started", so Close is what it gets, and that does nothing to it.
func (s *station) linkStarted(l *cs104.Link) {
	g := s.group
	if g == nil {
		return
	}
	g.mu.Lock()
	if old := g.active; old != nil && old != l {
		if old.Started() {
			old.Close()
		} else {
			s.stoppedLocked(g)
		}
	}
	g.active, g.noted = l, false
	event := g.pending
	g.pending = nil
	g.mu.Unlock()
	if event != nil {
		// The loop takes this after it has written the confirmation.
		go func() {
			if err := l.Send(context.Background(), event); err != nil {
				logf("cannot send the queued event: %v", err)
			}
		}()
	}
}

// redundancyRun is the operation "redundancy": two connections to one station,
// their ASDUs in the one session of the operation.
type redundancyRun struct {
	takeover bool
	s        *session
	p        *peer
	timeout  time.Duration

	// Guarded by s.mu.
	links   [2]*cs104.Link
	inTime  [2]bool // the connection is started, as far as the operation goes
	counts  [2]int
	closed  [2]bool
	current int
	stray   int
	queued  bool
}

const primary, standby = 0, 1

// watch receives on one connection. The library closes a connection on an I frame
// in the stopped state before it gets here, so that is a stray too.
func (r *redundancyRun) watch(i int, l *cs104.Link) {
	go func() {
		for raw := range l.ASDUs() {
			if len(raw) < params.Header() {
				continue
			}
			a, err := iec.DecodeASDU(raw, params, time.UTC)
			r.s.update(func() {
				if r.inTime[i] {
					r.counts[i]++
				} else {
					r.stray++
				}
				if i == standby && r.inTime[i] && raw[2]&0x3f == byte(iec.CauseSpontaneous) {
					r.queued = true
				}
			})
			r.s.onASDU(raw, a, err)
		}
		err := l.Err()
		r.s.update(func() {
			r.closed[i] = true
			if i == r.current {
				r.s.closed = true
			}
			if errors.Is(err, cs104.ErrFrame) && !l.Started() {
				r.stray++
			}
		})
	}()
}

func (r *redundancyRun) straySeen() error {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	if r.stray > 0 {
		return failf("failed", "%d I frames outside the time their connection was started", r.stray)
	}
	return nil
}

// interrogate is a general interrogation on the connection, to its termination.
func (r *redundancyRun) interrogate(ctx context.Context, l *cs104.Link, n int) error {
	r.s.update(func() { r.s.expectType, r.s.terminated = iec.CIcNa1, false })
	r.p.link = l
	err := r.p.request(ctx, iec.CIcNa1, iec.CauseActivation, iec.Object{Value: 20})
	if err == nil {
		if err = r.s.awaitConfirmation(n, r.timeout); err == nil {
			err = r.s.awaitTermination(r.timeout)
		}
	}
	var f *failure
	if err != nil && !errors.As(err, &f) {
		return failf("connection-lost", "%v", err)
	}
	return err
}

// stop is STOPDT on the connection, until STOPDT con. It gives up with the error
// of the operation; a stray takes precedence over what it did to the connection.
func (r *redundancyRun) stop(ctx context.Context, l *cs104.Link) error {
	sctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	if err := l.StopDT(sctx); err != nil {
		if err := r.straySeen(); err != nil {
			return err
		}
		if r.s.waitFor(func() bool { return false }, 20*time.Millisecond); r.s.isClosed() {
			return failf("connection-lost", "connection closed while waiting for STOPDT con")
		}
		return failf("timeout", "no STOPDT con from the station")
	}
	return nil
}

func (r *redundancyRun) close() {
	for _, l := range r.links {
		if l != nil {
			l.Close()
		}
	}
}

func (r *redundancyRun) result(switchover string) object {
	return object{"switchover": switchover, "primaryAsdus": r.counts[primary], "standbyAsdus": r.counts[standby],
		"strayAsdus": r.stray, "queuedEvent": r.queued}
}
