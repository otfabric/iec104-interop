// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"errors"
	"net"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	iec "github.com/cB-Sigma/iec60870"
	"github.com/cB-Sigma/iec60870/cs104"
	"github.com/cB-Sigma/iec60870/sec"
)

const (
	broadcast = 0xffff
	// Objects per ASDU when answering an interrogation; small enough for every type.
	maxObjects = 20
	// Simultaneous connections, as the contract asks.
	maxConnections = 16
)

// station is the controlled station of the contract, driven by a fixture.
type station struct {
	mu    sync.Mutex // guards the point values and selections of fx
	fx    *fixture
	ca    uint16
	files map[int]*iec.FileServer // by information object address
}

// conn is one connection: its link and, with --secure, its association.
type conn struct {
	link *cs104.Link
	sl   *sec.SecureLink
	peer string
	// watch is the goroutine that reports STOPDT; connection-closed waits
	// for it.
	watch sync.WaitGroup
}

func event(name string) object { return object{"event": name} }

func runServer(a *args) (int, error) {
	fixturePath := a.str("--fixture", defaultFixture)
	bind := a.str("--bind-address", "0.0.0.0")
	readyFile := a.str("--ready-file", defaultReadyFile)
	port, err := a.num("--port", 2404)
	if err != nil {
		return 0, err
	}
	cfg, err := a.apci()
	if err != nil {
		return 0, err
	}
	security, err := a.securityFlags()
	if err != nil {
		return 0, err
	}
	if err := a.roleFlags(security); err != nil {
		return 0, err
	}
	if err := a.finish("server"); err != nil {
		return 0, err
	}
	if port < 1 || port > 65535 {
		return 0, usagef("--port: %d is not a TCP port", port)
	}

	fx, err := loadFixture(fixturePath)
	if err != nil {
		logf("fixture %s: %v", fixturePath, err)
		return exitFixtureInvalid, nil
	}
	_ = os.Remove(readyFile)

	st := &station{fx: fx, ca: uint16(fx.Station.CommonAddr), files: map[int]*iec.FileServer{}}
	for _, f := range fx.Files {
		st.files[f.IOA] = st.fileServer(f)
	}

	ln, err := net.Listen("tcp", net.JoinHostPort(bind, strconv.Itoa(port)))
	if err != nil {
		logf("cannot listen on %s:%d: %v", bind, port, err)
		return exitOperationFailed, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var (
		mu    sync.Mutex
		conns = map[*conn]struct{}{}
		wg    sync.WaitGroup
	)
	acceptErr := make(chan error, 1)
	go func() {
		for {
			nc, err := ln.Accept()
			if err != nil {
				acceptErr <- err
				return
			}
			mu.Lock()
			full := len(conns) >= maxConnections
			mu.Unlock()
			if full {
				logf("refusing %s: %d connections are open", nc.RemoteAddr(), maxConnections)
				_ = nc.Close()
				continue
			}
			c := &conn{peer: nc.RemoteAddr().String()}
			lcfg := cfg
			if security != nil {
				if c.sl, err = security.link(false, st.ca); err != nil {
					logf("security: %v", err)
					_ = nc.Close()
					continue
				}
				lcfg.Security = c.sl
			}
			cctx, ccancel := context.WithCancel(ctx)
			if c.sl != nil {
				go c.sl.Run(cctx)
			}
			e := event("connection-opened")
			e["peer"] = c.peer
			emit(e)
			c.link = cs104.Accept(cctx, nc, lcfg, func(l *cs104.Link) { st.started(c, l) }, nil)
			mu.Lock()
			conns[c] = struct{}{}
			mu.Unlock()
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer ccancel()
				for raw := range c.link.ASDUs() {
					st.handle(cctx, c, raw)
				}
				<-c.link.Done()
				c.watch.Wait()
				mu.Lock()
				delete(conns, c)
				mu.Unlock()
				e := event("connection-closed")
				e["peer"] = c.peer
				emit(e)
			}()
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM, syscall.SIGINT)

	if err := os.WriteFile(readyFile, []byte("ready\n"), 0o644); err != nil {
		logf("warning: cannot write ready file %s", readyFile)
	}
	ready := event("ready")
	ready["adapter"] = adapter
	ready["address"] = net.JoinHostPort(bind, strconv.Itoa(port))
	ready["fixture"] = fx.Name
	ready["commonAddress"] = fx.Station.CommonAddr
	if security != nil {
		ready["secure"] = true
	}
	emit(ready)

	select {
	case <-stop:
	case err := <-acceptErr:
		logf("the listener died: %v", err)
		return exitOperationFailed, nil
	}
	_ = os.Remove(readyFile)
	_ = ln.Close()
	mu.Lock()
	for c := range conns {
		c.link.Close()
	}
	mu.Unlock()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
	}
	emit(event("stopped"))
	return exitOK, nil
}

// started reports STARTDT. cB-Sigma calls back on STARTDT only; STOPDT is
// seen as the link leaving the started state, which it does before it
// confirms.
func (s *station) started(c *conn, l *cs104.Link) {
	e := event("data-transfer-started")
	e["peer"] = c.peer
	emit(e)
	c.watch.Add(1)
	go func() {
		defer c.watch.Done()
		t := time.NewTicker(2 * time.Millisecond)
		defer t.Stop()
		for {
			// A link that ends stays "started"; one stopped just before
			// it ended is still reported.
			if !l.Started() {
				e := event("data-transfer-stopped")
				e["peer"] = c.peer
				emit(e)
				return
			}
			select {
			case <-l.Done():
				if !l.Started() {
					continue
				}
				return
			case <-t.C:
			}
		}
	}()
}

// send transmits an ASDU on the connection; with --secure the link protects
// it, or holds it until the session keys are in place.
func (c *conn) send(ctx context.Context, raw []byte) {
	if err := c.link.Send(ctx, raw); err != nil && !errors.Is(err, context.Canceled) {
		logf("send to %s failed: %v", c.peer, err)
	}
}

func (c *conn) sendASDU(ctx context.Context, a *iec.ASDU) {
	raw, err := a.Encode(params, time.UTC)
	if err != nil {
		logf("cannot encode %s: %v", a.Type, err)
		return
	}
	c.send(ctx, raw)
}

// pointASDU is an ASDU with the given points, all of one type.
func (s *station) pointASDU(pts []*point, timed bool, cause iec.Cause, oa uint8) *iec.ASDU {
	typ := pts[0].typ
	if timed {
		typ = timeTagged[typ]
	}
	a := &iec.ASDU{Type: typ, Cause: cause, Orig: oa, CA: s.ca}
	now := time.Now()
	for _, p := range pts {
		o := pointObject(p)
		if timed {
			o.Time = now
		}
		a.Objects = append(a.Objects, o)
	}
	return a
}

// sendPoints sends the integrated totals (counters) or everything else, in
// ascending address order, consecutive points of one type sharing an ASDU.
func (s *station) sendPoints(ctx context.Context, c *conn, cause iec.Cause, oa uint8, counters bool) {
	var out []*iec.ASDU
	var batch []*point
	s.mu.Lock()
	for _, p := range s.fx.Points {
		if (p.typ == iec.MItNa1) != counters {
			continue
		}
		if len(batch) > 0 && (p.typ != batch[0].typ || len(batch) >= maxObjects) {
			out = append(out, s.pointASDU(batch, false, cause, oa))
			batch = nil
		}
		batch = append(batch, p)
	}
	if len(batch) > 0 {
		out = append(out, s.pointASDU(batch, false, cause, oa))
	}
	s.mu.Unlock()
	for _, a := range out {
		c.sendASDU(ctx, a)
	}
}

// handle serves one ASDU of the controlling station, already verified by
// the security when there is one.
func (s *station) handle(ctx context.Context, c *conn, raw []byte) {
	if len(raw) < params.Header() {
		return
	}
	reject := func(cause iec.Cause) { c.send(ctx, mirror(raw, cause, true, uint16(raw[4])|uint16(raw[5])<<8)) }
	reply := func(cause iec.Cause, negative bool) { c.send(ctx, mirror(raw, cause, negative, s.ca)) }
	a, err := iec.DecodeASDU(raw, params, time.UTC)
	if errors.Is(err, iec.ErrUnsupportedType) {
		reject(iec.CauseUnknownType)
		return
	}
	if err != nil {
		logf("dropping an ASDU that cannot be decoded: %v", err)
		return
	}
	// The roles of the controlling station come first: a request they do
	// not permit is refused with cause 17, whatever it asks for.
	if c.sl != nil && !c.sl.Endpoint().Permits(uint8(a.Type)) {
		c.sl.Endpoint().NotAuthorized()
		reject(iec.CauseNotAuthorized)
		return
	}
	if a.CA != s.ca && a.CA != broadcast {
		reject(iec.CauseUnknownCA)
		return
	}
	switch a.Type {
	case iec.CIcNa1:
		if a.Cause != iec.CauseActivation || len(a.Objects) != 1 {
			reject(iec.CauseUnknownCause)
			return
		}
		qoi := int(a.Objects[0].Value)
		accepted := qoi == 20
		e := event("interrogation")
		e["commonAddress"], e["qoi"], e["accepted"] = int(a.CA), qoi, accepted
		emit(e)
		reply(iec.CauseActCon, !accepted)
		if accepted {
			s.sendPoints(ctx, c, iec.CauseInterrogated, a.Orig, false)
			reply(iec.CauseActTerm, false)
		}
	case iec.CCiNa1:
		if a.Cause != iec.CauseActivation || len(a.Objects) != 1 {
			reject(iec.CauseUnknownCause)
			return
		}
		// Only "general request counter" with "read" (no freeze, no reset).
		qcc := int(a.Objects[0].Value)
		accepted := qcc == 5
		e := event("counter-interrogation")
		e["commonAddress"], e["qcc"], e["accepted"] = int(a.CA), qcc, accepted
		emit(e)
		reply(iec.CauseActCon, !accepted)
		if accepted {
			s.sendPoints(ctx, c, iec.CauseCounterGen, a.Orig, true)
			reply(iec.CauseActTerm, false)
		}
	case iec.CRdNa1:
		if a.Cause != iec.CauseRequest || len(a.Objects) != 1 {
			reject(iec.CauseUnknownCause)
			return
		}
		ioa := int(a.Objects[0].IOA)
		var answer *iec.ASDU
		s.mu.Lock()
		if p := s.fx.point(ioa); p != nil {
			answer = s.pointASDU([]*point{p}, false, iec.CauseRequest, a.Orig)
		}
		s.mu.Unlock()
		e := event("read")
		e["ioa"], e["accepted"] = ioa, answer != nil
		emit(e)
		if answer == nil {
			reject(iec.CauseUnknownObject)
			return
		}
		c.sendASDU(ctx, answer)
	case iec.CCsNa1:
		if a.Cause != iec.CauseActivation || len(a.Objects) != 1 {
			reject(iec.CauseUnknownCause)
			return
		}
		e := event("clock-sync")
		e["time"] = formatTime(a.Objects[0].Time)
		emit(e)
		reply(iec.CauseActCon, false)
	case iec.CTsTa1:
		if a.Cause != iec.CauseActivation {
			reject(iec.CauseUnknownCause)
			return
		}
		reply(iec.CauseActCon, false)
	case iec.CScNa1, iec.CDcNa1, iec.CRcNa1, iec.CSeNa1, iec.CSeNb1, iec.CSeNc1,
		iec.CScTa1, iec.CDcTa1, iec.CRcTa1, iec.CSeTa1, iec.CSeTb1, iec.CSeTc1:
		if a.Cause != iec.CauseActivation && a.Cause != iec.CauseDeactivation || len(a.Objects) != 1 {
			reject(iec.CauseUnknownCause)
			return
		}
		s.command(ctx, c, raw, a)
	case iec.FScNa1, iec.FAfNa1:
		s.file(ctx, c, raw, a)
	default:
		reject(iec.CauseUnknownType)
	}
}

func (s *station) command(ctx context.Context, c *conn, raw []byte, a *iec.ASDU) {
	o := a.Objects[0]
	base := untimedCommand(a.Type)
	value := o.Value
	var eventValue any
	valid := true
	switch base {
	case iec.CScNa1:
		eventValue = o.Value != 0
	case iec.CDcNa1, iec.CRcNa1:
		eventValue = int(o.Value)
		valid = o.Value == 1 || o.Value == 2
	case iec.CSeNa1:
		value = float64(rawNormalised(o.Value))
		eventValue = int(value)
	case iec.CSeNb1:
		eventValue = int(o.Value)
	default: // C_SE_NC_1
		eventValue = o.Value
	}

	e := event("command")
	e["type"], e["ioa"], e["cot"] = a.Type.String(), int(o.IOA), int(a.Cause)
	reply := func(cause iec.Cause, negative bool) { c.send(ctx, mirror(raw, cause, negative, s.ca)) }

	var outcome string
	var report *iec.ASDU
	s.mu.Lock()
	cmd := s.fx.command(int(o.IOA))
	if cmd == nil || cmd.typ != base {
		s.mu.Unlock()
		e["outcome"] = "unknown-ioa"
		emit(e)
		c.send(ctx, mirror(raw, iec.CauseUnknownObject, true, a.CA))
		return
	}
	e["value"], e["select"] = eventValue, o.Select
	switch {
	case a.Cause == iec.CauseDeactivation:
		cmd.selected = false
		outcome = "deactivated"
	case !valid:
		outcome = "rejected"
	case o.Select:
		cmd.selected = true
		outcome = "selected"
	case cmd.SelectRequired && !cmd.selected:
		outcome = "rejected"
	default:
		cmd.selected = false
		t := cmd.target
		next := value
		if base == iec.CRcNa1 {
			next = t.value - 1
			if value == 2 {
				next = t.value + 1
			}
		}
		if base == iec.CRcNa1 && (next < -64 || next > 63) {
			outcome = "rejected" // the step position is at its limit
		} else {
			t.value = next
			report = s.pointASDU([]*point{t}, true, iec.Cause(*cmd.ReportCause), 0)
			outcome = "executed"
		}
	}
	s.mu.Unlock()
	e["outcome"] = outcome
	emit(e)

	switch outcome {
	case "deactivated":
		reply(iec.CauseDeactCon, false)
	case "rejected":
		reply(iec.CauseActCon, true)
	default:
		reply(iec.CauseActCon, false)
		if report != nil {
			// Confirmation, then the new state of the target, then termination.
			c.sendASDU(ctx, report)
			reply(iec.CauseActTerm, false)
		}
	}
}

// fileServer is cB-Sigma's file service for one file of the fixture, with
// the file's section size and segments of 236 octets, as the fixture format
// says (cB-Sigma's default is 200).
func (s *station) fileServer(f *fileSpec) *iec.FileServer {
	return &iec.FileServer{
		Store:       fileStore{f},
		SectionSize: f.SectionSize,
		SegmentSize: 236,
	}
}

func fileEvent(f *fileSpec, success bool) {
	e := event("file-transfer")
	e["ioa"], e["name"], e["success"] = f.IOA, f.Name, success
	emit(e)
}

// file serves the file transfer requests of the monitor direction: the
// selection is checked here, against the files of the fixture, and the
// procedure is the library's.
func (s *station) file(ctx context.Context, c *conn, raw []byte, a *iec.ASDU) {
	reject := func(cause iec.Cause) { c.send(ctx, mirror(raw, cause, true, a.CA)) }
	if a.Cause != iec.CauseFile {
		reject(iec.CauseUnknownCause)
		return
	}
	if len(a.Objects) != 1 {
		reject(iec.CauseUnknownObject)
		return
	}
	o := a.Objects[0]
	f := s.fx.file(int(o.IOA))
	if f == nil || int(o.File) != f.Name {
		// A deactivation ends a transfer, and there is none of an unknown
		// file: it is not answered, like the library's own file server does
		// not answer one. (cB-Sigma's client deactivates after a refusal.)
		if a.Type == iec.FScNa1 && (o.Qualifier&0x0f == iec.SCQDeactivateFile || o.Qualifier&0x0f == iec.SCQDeactivateSection) {
			return
		}
		reject(iec.CauseUnknownObject)
		return
	}
	// The end of a transfer is reported here rather than from the library's
	// OnFileDone, which runs on a goroutine of its own and could come after
	// the connection-closed event.
	switch {
	case a.Type == iec.FAfNa1 && o.Qualifier&0x0f == iec.AFQFileOK:
		fileEvent(f, true)
	case a.Type == iec.FAfNa1 && o.Qualifier&0x0f == iec.AFQFileNegative,
		a.Type == iec.FScNa1 && o.Qualifier&0x0f == iec.SCQDeactivateFile:
		fileEvent(f, false)
	}
	s.files[f.IOA].Handle(ctx, a, func(answer *iec.ASDU) {
		answer.CA = s.ca
		c.sendASDU(ctx, answer)
	})
}

// fileStore serves the content of one file of the fixture.
type fileStore struct{ f *fileSpec }

func (fs fileStore) Directory(context.Context) ([]iec.DirEntry, error) {
	return []iec.DirEntry{{IOA: uint32(fs.f.IOA), Name: uint16(fs.f.Name), Length: uint32(fs.f.Size)}}, nil
}

func (fs fileStore) Read(_ context.Context, ioa uint32, name uint16) ([]byte, error) {
	if int(ioa) != fs.f.IOA || int(name) != fs.f.Name {
		return nil, iec.ErrNoFile
	}
	return fs.f.content(), nil
}

func (fs fileStore) Write(context.Context, uint32, uint16, []byte) error { return iec.ErrNoFile }
