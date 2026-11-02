// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"errors"
	"strings"

	iec104 "github.com/otfabric/go-iec104"
	"github.com/otfabric/go-iec104/asdu"
)

// redundancy is the operation of that name: a controlling station with two
// connections to one station, each step in its own hands. That is why it does not
// use client.Group, which decides the steps itself.
type redundancy struct {
	s          *session
	switchover string
	standby    *peer
	closeBoth  func()

	// Guarded by the mutex of the session.
	primaryAsdus    int
	standbyAsdus    int
	strays          int
	queued          bool
	primaryEnded    bool // the time the primary is started has passed
	primaryGone     bool
	primaryMayClose bool
	stopped         bool
}

func parseRedundancy(f *flags, s *session) *redundancy {
	r := &redundancy{s: s, switchover: f.text("switchover", "stopdt")}
	if r.switchover != "stopdt" && r.switchover != "takeover" {
		f.fail("--switchover %q: want stopdt or takeover", r.switchover)
	}
	return r
}

// strayFrame says that a connection closed because of an I frame outside the time
// it is started. The library treats that as a protocol error of the station and
// drops the connection, so the frame reaches no handler: it counts as one stray,
// however many the station sent, and is not in the asdus of the result.
func strayFrame(err error) bool {
	return errors.Is(err, iec104.ErrProtocol) && strings.Contains(err.Error(), "while data transfer is stopped")
}

// HandleASDU receives the primary.
func (r *redundancy) HandleASDU(a *asdu.ASDU) {
	r.s.update(func() {
		if r.primaryEnded {
			r.strays++
		} else {
			r.primaryAsdus++
		}
	})
	r.s.HandleASDU(a)
}

func (r *redundancy) primaryState(state iec104.State, err error) {
	if state != iec104.StateDisconnected {
		return
	}
	r.s.update(func() {
		r.primaryGone = true
		if strayFrame(err) {
			r.strays++
		}
		if !r.primaryMayClose {
			r.s.closed = true
		}
	})
}

// standbyHandler receives the standby. Its link has already refused any I frame
// from before its STARTDT con.
type standbyHandler struct{ r *redundancy }

func (h standbyHandler) HandleASDU(a *asdu.ASDU) {
	r := h.r
	r.s.update(func() {
		r.standbyAsdus++
		if a.Cause == asdu.CauseSpontaneous && r.switchover == "stopdt" {
			r.queued = true
		}
	})
	r.s.HandleASDU(a)
}

func (r *redundancy) standbyState(state iec104.State, err error) {
	if state != iec104.StateDisconnected {
		return
	}
	r.s.update(func() {
		if strayFrame(err) {
			r.strays++
		}
		r.s.closed = true
	})
}

// run is the sequence of the contract from step 2 on: step 1 connects, as for
// every operation, and starts the primary.
func (r *redundancy) run(p *peer, collect func()) error {
	err := r.steps(p, collect)
	// When this returns, the handlers have seen everything that was received.
	r.closeBoth()
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	if r.strays > 0 {
		return failf("failed", "%d I frames outside the time the connection was started", r.strays)
	}
	return err
}

func (r *redundancy) steps(p *peer, collect func()) error {
	station := asdu.Interrogation{Qualifier: asdu.QOIStation}
	if err := p.activate(station); err != nil {
		return err
	}
	if r.switchover == "stopdt" {
		if err := p.stopDT(); err != nil {
			return err
		}
		r.s.update(func() { r.primaryEnded = true })
	} else {
		r.s.update(func() { r.primaryMayClose = true })
	}

	ctx, cancel := context.WithTimeout(context.Background(), p.timeout)
	defer cancel()
	if err := r.standby.startDT(ctx); err != nil {
		return err
	}
	var gone bool
	r.s.update(func() {
		gone = r.primaryGone
		r.primaryEnded, r.primaryMayClose = true, true
	})
	if gone && r.switchover == "stopdt" {
		return failf("connection-lost", "the primary closed before the standby started")
	}

	if r.switchover == "stopdt" {
		if !r.s.waitFor(func() bool { return r.queued || r.strays > 0 }, p.timeout) {
			if r.s.isClosed() {
				return failf("connection-lost", "the connection closed while waiting for the queued event")
			}
			return failf("timeout", "no queued event on the standby")
		}
	}

	if err := r.standby.activate(station); err != nil {
		return err
	}
	if err := r.standby.stopDT(); err != nil {
		return err
	}
	r.s.update(func() { r.stopped = true })
	collect()
	return nil
}

// result is the redundancy object of the result document. The caller holds the
// mutex of the session.
func (r *redundancy) result() object {
	return object{"switchover": r.switchover, "primaryAsdus": r.primaryAsdus, "standbyAsdus": r.standbyAsdus,
		"strayAsdus": r.strays, "queuedEvent": r.queued}
}
