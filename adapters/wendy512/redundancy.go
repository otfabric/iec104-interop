// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"time"

	"github.com/wendy512/go-iecp5/asdu"
	"github.com/wendy512/go-iecp5/cs104"
)

// The controlling station with two connections to one station: the operation
// redundancy of the contract, IEC 60870-5-104 clause 10. go-iecp5 knows nothing
// of a group; these are two clients.

// strayLog is what a go-iecp5 client logs when it drops an I frame because it
// has data transfer stopped. The handler never sees that frame, and this is the
// only trace of it (cs104/client.go, run).
const strayLog = "station not active"

type redundancyGroup struct {
	switchover    string
	primary       *cs104.Client
	standby       *cs104.Client
	ps, ss        *session
	plog, slog    *libraryLog
	oa            asdu.OriginAddr
	queuedEvent   bool
	stopConfirmed bool
}

// strayCount is every I frame outside the time its connection was started: the
// ones the library dropped and the ones after a takeover, which it does not
// know about.
func (g *redundancyGroup) strayCount() int {
	g.ps.mu.Lock()
	primary := g.ps.strays
	g.ps.mu.Unlock()
	g.ss.mu.Lock()
	standby := g.ss.strays
	g.ss.mu.Unlock()
	return primary + standby + g.plog.count(strayLog) + g.slog.count(strayLog)
}

// interrogate runs a station interrogation on one connection, up to its
// termination.
func (g *redundancyGroup) interrogate(c *cs104.Client, s *session, ca asdu.CommonAddr, timeout time.Duration) error {
	s.update(func() { s.expectType = asdu.C_IC_NA_1 })
	activation := asdu.CauseOfTransmission{Cause: asdu.Activation}
	if err := asdu.InterrogationCmd(withOriginator{c, g.oa}, activation, ca, asdu.QOIStation); err != nil {
		return err
	}
	if err := s.awaitConfirmation(1, timeout); err != nil {
		return err
	}
	return s.awaitTermination(timeout)
}

// run is steps 2 to 7 of the contract's table. The caller has connected both
// and started the primary.
func (g *redundancyGroup) run(ca asdu.CommonAddr, timeout time.Duration) error {
	stopdt := g.switchover == "stopdt"
	if err := g.interrogate(g.primary, g.ps, ca, timeout); err != nil {
		return err
	}
	if stopdt {
		if err := stopTransfer(g.primary, g.ps, timeout, "stopdt-timeout"); err != nil {
			return err
		}
		g.ps.update(func() { g.ps.windowOver = true })
	}

	// A takeover ends the primary's time with the standby's STARTDT con. That
	// arrives on another socket, so nothing says which of the two a frame sent
	// right after it comes behind. Counting from the STARTDT act closes that gap
	// and wrongly accuses nobody: what the primary asked for has been answered,
	// and a station sends nothing unasked.
	g.ps.update(func() { g.ps.windowOver = true })
	g.standby.SendStartDt()
	if !g.ss.waitFor(func() bool { return g.ss.started }, timeout) {
		if g.ss.isClosed() {
			return failf("connection-lost", "the standby closed while waiting for STARTDT con")
		}
		return failf("startdt-timeout", "no STARTDT con on the standby")
	}
	if stopdt && g.ps.isClosed() {
		return failf("connection-lost", "the primary closed before the standby took over")
	}
	// With a takeover the station may close the primary from here on, which
	// sets off nothing: its client is closed and does not dial again.

	if stopdt {
		if !g.ss.waitFor(func() bool { return g.ss.spontaneous > 0 }, timeout) {
			if g.ss.isClosed() {
				return failf("connection-lost", "the standby closed while waiting for the queued event")
			}
			return failf("timeout", "the station sent no queued event on the standby")
		}
		g.queuedEvent = true
	}

	if err := g.interrogate(g.standby, g.ss, ca, timeout); err != nil {
		return err
	}
	if err := stopTransfer(g.standby, g.ss, timeout, "stopdt-timeout"); err != nil {
		return err
	}
	g.stopConfirmed = true
	return nil
}

// report fills in what differs from a single connection: the confirmations and
// termination of the standby's interrogation, and the redundancy object.
func (g *redundancyGroup) report(r object) {
	strays := g.strayCount()
	g.ps.mu.Lock()
	primary := g.ps.received
	connected := g.ps.connected || g.ps.tcpSeen
	g.ps.mu.Unlock()
	g.ss.mu.Lock()
	defer g.ss.mu.Unlock()
	r["connected"] = connected && (g.ss.connected || g.ss.tcpSeen)
	r["confirmations"] = append(r["confirmations"].([]object), g.ss.confirmations...)
	if g.ss.expectType != 0 {
		r["terminated"] = g.ss.terminated
	}
	r["redundancy"] = object{
		"switchover":   g.switchover,
		"primaryAsdus": primary,
		"standbyAsdus": g.ss.received,
		"strayAsdus":   strays,
		"queuedEvent":  g.queuedEvent,
	}
}
