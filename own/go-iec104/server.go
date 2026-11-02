// SPDX-License-Identifier: MIT

package main

import (
	"cmp"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"net"
	"os"
	"os/signal"
	"slices"
	"strconv"
	"sync"
	"syscall"
	"time"

	iec104 "github.com/otfabric/go-iec104"
	"github.com/otfabric/go-iec104/asdu"
	"github.com/otfabric/go-iec104/sec"
	"github.com/otfabric/go-iec104/server"
)

// maxConnections is how many simultaneous connections the contract asks for.
const maxConnections = 16

// objectsPerASDU is small enough for every type of an interrogation answer.
const objectsPerASDU = 20

func event(name string) object { return object{"event": name} }

// station is the controlled station of the contract, driven by a fixture
// (docs/FIXTURES.md). Everything it refuses before a handler runs, the library
// refuses: an unknown common address, a type without a handler, a cause that is
// not valid for the type, a request the roles do not permit. Those produce no
// event, as the contract says.
type station struct {
	ca asdu.CommonAddr

	mu       sync.Mutex // guards the point values and the selections
	fx       *fixture
	commands map[int]*command
	sessions map[uint64]bool // data transfer started, by session
	stored   map[asdu.IOA]*storedFile
	arriving map[arrivalKey]*arrival
}

// storedFile is an upload that arrived complete.
type storedFile struct {
	name uint16
	data []byte
}

// storedSectionSize is the size of the sections of a stored file when it is
// fetched, which FIXTURES.md leaves to the station. No stored file can have more
// sections than the file transfer allows.
const storedSectionSize = 2048

// arrivalKey is a file on its way to the station: a session transfers one per
// address.
type arrivalKey struct {
	session uint64
	ioa     asdu.IOA
}

// arrival is what arrived of such a file, for the event. The library keeps that
// to itself until the file is complete.
type arrival struct {
	name uint16
	data []byte
}

func newStation(fx *fixture) *station {
	st := &station{ca: asdu.CommonAddr(fx.Station.CommonAddress), fx: fx,
		commands: map[int]*command{}, sessions: map[uint64]bool{},
		stored: map[asdu.IOA]*storedFile{}, arriving: map[arrivalKey]*arrival{}}
	for _, c := range fx.Commands {
		st.commands[c.IOA] = c
	}
	return st
}

// handler routes the requests the fixture format knows.
func (st *station) handler() server.Handler {
	mux := server.NewMux()
	mux.HandleFunc(asdu.C_IC_NA_1, one(st.interrogation))
	mux.HandleFunc(asdu.C_CI_NA_1, one(st.counterInterrogation))
	mux.HandleFunc(asdu.C_RD_NA_1, one(st.read))
	mux.HandleFunc(asdu.C_CS_NA_1, one(st.clockSync))
	mux.HandleFunc(asdu.C_TS_TA_1, one(st.testCommand))
	for _, t := range []asdu.TypeID{asdu.C_SC_NA_1, asdu.C_DC_NA_1, asdu.C_RC_NA_1, asdu.C_SE_NA_1, asdu.C_SE_NB_1, asdu.C_SE_NC_1,
		asdu.C_SC_TA_1, asdu.C_DC_TA_1, asdu.C_RC_TA_1, asdu.C_SE_TA_1, asdu.C_SE_TB_1, asdu.C_SE_TC_1} {
		mux.HandleFunc(t, one(st.command))
	}
	// Two file servers, because OnDone does not say which way a file went: this
	// one for what the station sends, and the directory.
	files := server.NewFileServer(server.FileSourceFunc(st.openFile))
	files.Directory = st.directory
	files.OnDone = func(_ *server.Session, _ asdu.CommonAddr, ioa asdu.IOA, name uint16, err error) {
		e := event("file-transfer")
		e["ioa"], e["name"], e["success"] = int(ioa), int(name), err == nil
		emit(e)
	}
	fileRequest := one(func(s *server.Session, req *asdu.ASDU) { files.HandleASDU(s, st.own(req)) })
	mux.HandleFunc(asdu.F_SC_NA_1, fileRequest)
	mux.HandleFunc(asdu.F_AF_NA_1, fileRequest)
	// And this one for what it takes.
	uploads := server.NewFileServer(nil)
	uploads.Sink = st
	uploads.OnDone = func(s *server.Session, _ asdu.CommonAddr, ioa asdu.IOA, _ uint16, err error) {
		st.fileReceived(arrivalKey{s.ID(), ioa}, err == nil)
	}
	fileUpload := one(func(s *server.Session, req *asdu.ASDU) {
		if seg, ok := req.First().(asdu.FileSegment); ok {
			st.segment(arrivalKey{s.ID(), seg.IOA}, seg)
		}
		uploads.HandleASDU(s, st.own(req))
	})
	for _, t := range []asdu.TypeID{asdu.F_FR_NA_1, asdu.F_SR_NA_1, asdu.F_SG_NA_1, asdu.F_LS_NA_1} {
		mux.HandleFunc(t, fileUpload)
	}
	return mux
}

// one refuses a request that does not carry the one object every type of the
// contract has, which the handlers can then rely on.
func one(h func(*server.Session, *asdu.ASDU)) func(*server.Session, *asdu.ASDU) {
	return func(s *server.Session, req *asdu.ASDU) {
		if len(req.Objects) != 1 {
			_ = s.Reject(req, asdu.CauseUnknownCause)
			return
		}
		h(s, req)
	}
}

// own returns req addressed to this station: a request to the broadcast address
// is answered with the station's own address.
func (st *station) own(req *asdu.ASDU) *asdu.ASDU {
	if req.CommonAddr == st.ca {
		return req
	}
	r := *req
	r.CommonAddr = st.ca
	return &r
}

// onState turns the states of a session into the events of the contract. For one
// session it runs on the goroutine of the handlers, in protocol order.
func (st *station) onState(s *server.Session, state iec104.State, _ error) {
	peer := s.RemoteAddr().String()
	st.mu.Lock()
	started, known := st.sessions[s.ID()]
	st.sessions[s.ID()] = state == iec104.StateStarted
	var cut []arrivalKey
	if state == iec104.StateDisconnected {
		delete(st.sessions, s.ID())
		for key := range st.arriving {
			if key.session == s.ID() {
				cut = append(cut, key)
			}
		}
	}
	st.mu.Unlock()
	// The library says nothing of a transfer that ends with its session.
	for _, key := range cut {
		st.fileReceived(key, false)
	}
	name := ""
	switch {
	case state == iec104.StateDisconnected:
		name = "connection-closed"
	case state == iec104.StateStarted:
		name = "data-transfer-started"
	case !known:
		name = "connection-opened"
	case started:
		name = "data-transfer-stopped"
	default:
		return
	}
	e := event(name)
	e["peer"] = peer
	emit(e)
}

// send transmits an ASDU on the session; the end of the session is not worth a
// message.
func send(s *server.Session, a *asdu.ASDU) {
	if err := s.Send(s.Context(), a); err != nil && s.Context().Err() == nil {
		logf("send to %s failed: %v", s.RemoteAddr(), err)
	}
}

// answer sends the points chosen, in ascending address order, consecutive points
// of one type sharing an ASDU.
func (st *station) answer(s *server.Session, cause asdu.Cause, originator uint8, chosen func(*point) bool) {
	var out []*asdu.ASDU
	var cur *asdu.ASDU
	last := ""
	st.mu.Lock()
	for _, p := range st.fx.Points {
		if !chosen(p) {
			continue
		}
		if cur == nil || p.Type != last || len(cur.Objects) >= objectsPerASDU {
			cur = asdu.New(cause, st.ca, p.object(asdu.Timestamp{}))
			cur.Originator = originator
			out = append(out, cur)
			last = p.Type
			continue
		}
		cur.Objects = append(cur.Objects, p.object(asdu.Timestamp{}))
	}
	st.mu.Unlock()
	for _, a := range out {
		send(s, a)
	}
}

// interrogated says which points answer a QOI: every point that is not an
// integrated total for the station, the points of a group for QOI 21..36, and
// nothing for any other QOI.
func interrogated(qoi asdu.QOI) func(*point) bool {
	switch {
	case qoi == asdu.QOIStation:
		return func(p *point) bool { return p.Type != "M_IT_NA_1" }
	case qoi >= asdu.QOIGroup1 && qoi <= asdu.QOIGroup16:
		group := int(qoi - asdu.QOIStation)
		return func(p *point) bool { return slices.Contains(p.Groups, group) }
	}
	return func(*point) bool { return false }
}

func (st *station) interrogation(s *server.Session, req *asdu.ASDU) {
	if req.Cause != asdu.CauseActivation {
		_ = s.Reject(req, asdu.CauseUnknownCause)
		return
	}
	qoi := req.First().(asdu.Interrogation).Qualifier
	chosen := interrogated(qoi)
	// A group without a point has no group to serve.
	accepted := qoi == asdu.QOIStation || slices.ContainsFunc(st.fx.Points, chosen)
	e := event("interrogation")
	e["commonAddress"], e["qoi"], e["accepted"] = int(req.CommonAddr), int(qoi), accepted
	emit(e)
	own := st.own(req)
	if !accepted {
		_ = s.Negative(own)
		return
	}
	_ = s.Confirm(own)
	st.answer(s, qoi.Cause(), req.Originator, chosen)
	_ = s.Terminate(own)
}

func (st *station) counterInterrogation(s *server.Session, req *asdu.ASDU) {
	if req.Cause != asdu.CauseActivation {
		_ = s.Reject(req, asdu.CauseUnknownCause)
		return
	}
	ci := req.First().(asdu.CounterInterrogation)
	accepted := ci.Request == asdu.CounterGeneral && ci.Freeze == asdu.FreezeRead
	e := event("counter-interrogation")
	e["commonAddress"], e["qcc"], e["accepted"] = int(req.CommonAddr), int(ci.Request)|int(ci.Freeze)<<6, accepted
	emit(e)
	own := st.own(req)
	if !accepted {
		_ = s.Negative(own)
		return
	}
	_ = s.Confirm(own)
	st.answer(s, ci.Cause(), req.Originator, func(p *point) bool { return p.Type == "M_IT_NA_1" })
	_ = s.Terminate(own)
}

func (st *station) read(s *server.Session, req *asdu.ASDU) {
	ioa := req.First().Address()
	var answer *asdu.ASDU
	st.mu.Lock()
	for _, p := range st.fx.Points {
		if asdu.IOA(p.IOA) == ioa {
			answer = asdu.New(asdu.CauseRequest, st.ca, p.object(asdu.Timestamp{}))
			answer.Originator = req.Originator
		}
	}
	st.mu.Unlock()
	e := event("read")
	e["ioa"], e["accepted"] = int(ioa), answer != nil
	emit(e)
	if answer == nil {
		_ = s.Reject(st.own(req), asdu.CauseUnknownIOA)
		return
	}
	send(s, answer)
}

func (st *station) clockSync(s *server.Session, req *asdu.ASDU) {
	if req.Cause != asdu.CauseActivation {
		_ = s.Reject(req, asdu.CauseUnknownCause)
		return
	}
	e := event("clock-sync")
	e["time"] = formatTime(req.First().(asdu.ClockSync).Time.Time)
	emit(e)
	_ = s.Confirm(st.own(req))
}

func (st *station) testCommand(s *server.Session, req *asdu.ASDU) {
	if req.Cause != asdu.CauseActivation {
		_ = s.Reject(req, asdu.CauseUnknownCause)
		return
	}
	_ = s.Confirm(st.own(req))
}

// decodeCommand returns the type without time tag, the value of the event, the
// number the point takes, the select bit, and whether the value is one the type
// permits.
func decodeCommand(obj asdu.InformationObject) (typ string, shown any, value float64, sel, valid bool) {
	switch c := obj.(type) {
	case asdu.SingleCommand:
		if c.Value {
			value = 1
		}
		return "C_SC_NA_1", c.Value, value, c.Select, true
	case asdu.DoubleCommand:
		return "C_DC_NA_1", int(c.Value), float64(c.Value), c.Select, c.Value == asdu.DoubleOff || c.Value == asdu.DoubleOn
	case asdu.StepCommand:
		return "C_RC_NA_1", int(c.Value), float64(c.Value), c.Select, c.Value == asdu.StepLower || c.Value == asdu.StepHigher
	case asdu.SetpointNormalized:
		return "C_SE_NA_1", int(c.Value), float64(c.Value), c.Select, true
	case asdu.SetpointScaled:
		return "C_SE_NB_1", int(c.Value), float64(c.Value), c.Select, true
	case asdu.SetpointFloat:
		return "C_SE_NC_1", float64(c.Value), float64(c.Value), c.Select, true
	}
	return "", nil, 0, false, false
}

func (st *station) command(s *server.Session, raw *asdu.ASDU) {
	req := st.own(raw)
	typ, shown, value, sel, valid := decodeCommand(req.First())
	ioa := int(req.First().Address())
	e := event("command")
	e["type"], e["ioa"], e["cot"] = req.Type.String(), ioa, int(req.Cause)

	st.mu.Lock()
	cmd := st.commands[ioa]
	if cmd == nil || cmd.Type != typ {
		st.mu.Unlock()
		e["outcome"] = "unknown-ioa"
		emit(e)
		_ = s.Reject(req, asdu.CauseUnknownIOA)
		return
	}
	e["value"], e["select"] = shown, sel
	var report *asdu.ASDU
	outcome := "rejected"
	switch {
	case req.Cause == asdu.CauseDeactivation:
		cmd.selected = false
		outcome = "deactivated"
	case !valid:
	case sel:
		cmd.selected = true
		outcome = "selected"
	case cmd.SelectRequired && !cmd.selected:
	default:
		cmd.selected = false
		next := value
		if typ == "C_RC_NA_1" {
			next = cmd.target.value - 1
			if value == float64(asdu.StepHigher) {
				next = cmd.target.value + 1
			}
			if next < -64 || next > 63 {
				break // the step position is at its limit
			}
		}
		cmd.target.value = next
		report = asdu.New(asdu.Cause(*cmd.ReportCause), st.ca, cmd.target.object(asdu.Now()))
		outcome = "executed"
	}
	st.mu.Unlock()
	e["outcome"] = outcome
	emit(e)

	switch outcome {
	case "rejected":
		_ = s.Negative(req)
	case "executed":
		// Confirmation, then the new state of the target, then termination.
		_ = s.Confirm(req)
		send(s, report)
		_ = s.Terminate(req)
	default:
		_ = s.Confirm(req)
	}
}

func (st *station) openFile(_ *server.Session, _ asdu.CommonAddr, ioa asdu.IOA, name uint16) (server.File, bool) {
	st.mu.Lock()
	stored := st.stored[ioa]
	st.mu.Unlock()
	if stored != nil && stored.name == name {
		return server.NewFile(stored.data, storedSectionSize), true
	}
	for _, f := range st.fx.Files {
		if asdu.IOA(f.IOA) == ioa && uint16(f.Name) == name {
			return server.NewFile(f.content(), f.SectionSize), true
		}
	}
	return server.File{}, false
}

// AcceptFile says whether an upload has a place: the address and name of an entry
// of the fixture, and a length it takes.
func (st *station) AcceptFile(s *server.Session, _ asdu.CommonAddr, ioa asdu.IOA, name uint16, length int) bool {
	for _, u := range st.fx.Uploads {
		if asdu.IOA(u.IOA) == ioa && uint16(u.Name) == name && length >= 1 && length <= u.MaxSize {
			st.mu.Lock()
			st.arriving[arrivalKey{s.ID(), ioa}] = &arrival{name: name}
			st.mu.Unlock()
			return true
		}
	}
	return false
}

// StoreFile keeps a file that arrived complete, in place of any earlier one.
func (st *station) StoreFile(_ *server.Session, _ asdu.CommonAddr, ioa asdu.IOA, name uint16, file server.File) error {
	data := make([]byte, 0, file.Len())
	for _, section := range file.Sections {
		data = append(data, section...)
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	st.stored[ioa] = &storedFile{name: name, data: data}
	return nil
}

// segment notes what arrived, for the event. The handler of the session runs one
// ASDU at a time, so nothing else touches this arrival.
func (st *station) segment(key arrivalKey, seg asdu.FileSegment) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if a := st.arriving[key]; a != nil && a.name == seg.Name {
		a.data = append(a.data, seg.Data...)
	}
}

// fileReceived ends an arrival with its event.
func (st *station) fileReceived(key arrivalKey, success bool) {
	st.mu.Lock()
	a := st.arriving[key]
	delete(st.arriving, key)
	st.mu.Unlock()
	if a == nil {
		return
	}
	sum := sha256.Sum256(a.data)
	e := event("file-received")
	e["ioa"], e["name"], e["length"], e["sha256"], e["success"] = int(key.ioa), int(a.name), len(a.data), hex.EncodeToString(sum[:]), success
	emit(e)
}

// directory lists the files of the fixture and the stored uploads, in ascending
// address order. The library marks the last one.
func (st *station) directory(*server.Session, asdu.CommonAddr, asdu.IOA) []asdu.FileDirectoryEntry {
	now := asdu.Now()
	var entries []asdu.FileDirectoryEntry
	for _, f := range st.fx.Files {
		entries = append(entries, asdu.FileDirectoryEntry{IOA: asdu.IOA(f.IOA), Name: uint16(f.Name), Length: uint32(f.Size), Time: now})
	}
	st.mu.Lock()
	for ioa, f := range st.stored {
		entries = append(entries, asdu.FileDirectoryEntry{IOA: ioa, Name: f.name, Length: uint32(len(f.data)), Time: now})
	}
	st.mu.Unlock()
	slices.SortFunc(entries, func(a, b asdu.FileDirectoryEntry) int { return cmp.Compare(a.IOA, b.IOA) })
	return entries
}

// listen opens the listener of a station and starts serving on it.
func (st *station) serve(ln net.Listener, opts ...server.Option) (*server.Server, <-chan error, error) {
	opts = append([]server.Option{server.WithCommonAddrs(st.ca), server.WithMaxSessions(maxConnections),
		server.WithStateHandler(st.onState)}, opts...)
	srv, err := server.New(st.handler(), opts...)
	if err != nil {
		return nil, nil, err
	}
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()
	return srv, served, nil
}

func runServer(f *flags) int {
	fixturePath := f.text("fixture", fixtureRoot+"/baseline/fixture.json")
	bind := f.text("bind-address", "0.0.0.0")
	readyFile := f.text("ready-file", "/run/iec104-interop/ready")
	port := f.integer("port", 2404, 1, 65535)
	params := f.apci()
	secure := f.security()
	f.accessRoles(secure)
	transport := f.transport()
	if secure != nil && transport != nil {
		f.fail("--tls with --secure is not in the contract")
	}
	if err := f.finish(); err != nil {
		return usage(err.Error())
	}

	fx, err := loadFixture(fixturePath)
	if err != nil {
		logf("fixture %s: %v", fixturePath, err)
		return exitInvalidFixture
	}
	st := newStation(fx)
	opts := []server.Option{server.WithParams(params)}
	if secure != nil {
		if _, err := secure.endpoint(sec.Controlled, st.ca, 0); err != nil {
			return usage(err.Error())
		}
		// The association of each controlling station, by the AIM it names.
		opts = append(opts, server.WithSecurity(func(string, uint16) (*sec.Endpoint, error) {
			return secure.endpoint(sec.Controlled, st.ca, 0)
		}))
	}
	_ = os.Remove(readyFile)

	address := net.JoinHostPort(bind, strconv.Itoa(port))
	ln, err := net.Listen("tcp", address)
	if err != nil {
		logf("cannot listen on %s: %v", address, err)
		return exitFailed
	}
	if transport != nil {
		// Serve takes the listener as it is; the library shakes hands with each
		// connection, within t0, before it becomes a session.
		ln = tls.NewListener(ln, transport.server())
	}
	srv, served, err := st.serve(ln, opts...)
	if err != nil {
		_ = ln.Close()
		return usage(err.Error())
	}
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM, syscall.SIGINT)

	// The event first: whoever sees the file finds the event at the top of the log.
	ready := event("ready")
	ready["adapter"], ready["address"], ready["fixture"], ready["commonAddress"] = adapterName, address, fx.Name, fx.Station.CommonAddress
	if secure != nil {
		ready["secure"] = true
	}
	if transport != nil {
		ready["tls"] = true
	}
	emit(ready)
	if err := os.WriteFile(readyFile, []byte("ready\n"), 0o644); err != nil {
		logf("cannot write the ready file %s: %v", readyFile, err)
	}

	select {
	case <-stop:
	case err := <-served:
		if !errors.Is(err, server.ErrServerClosed) {
			logf("the listener died: %v", err)
		}
		_ = os.Remove(readyFile)
		return exitFailed
	}
	_ = os.Remove(readyFile)
	closed := make(chan struct{})
	go func() { _ = srv.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		logf("stopping without waiting for the connections")
	}
	emit(event("stopped"))
	return exitOK
}
