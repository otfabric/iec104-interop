// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"sync"
	"time"

	iec "github.com/cB-Sigma/iec60870"
)

// storedSectionSize is the section size of a stored upload when it is fetched:
// the contract leaves it to the station.
const storedSectionSize = 2048

// upload is one entry of the fixture's uploads: the library's FileServer runs
// the procedure of the control direction, and this keeps what it does not
// (FileServer accepts any file ready, does not compare the length of a section
// with its section ready, and says nothing of a file it refused).
type upload struct {
	spec *uploadSpec
	srv  *iec.FileServer

	mu       sync.Mutex
	active   bool
	name     int
	nos      uint8
	expected int    // length of the section, from its section ready
	section  []byte // the section as it arrives
	arrived  []byte // the sections acknowledged
	length   int    // length of the file, from its file ready
}

// stationFiles answers the call of the directory; an upload is stored by Write.
type stationFiles struct{ s *station }

func (sf stationFiles) Directory(context.Context) ([]iec.DirEntry, error) {
	sf.s.mu.Lock()
	defer sf.s.mu.Unlock()
	out := make([]iec.DirEntry, 0, len(sf.s.fx.Files))
	now := time.Now()
	for _, f := range sf.s.fx.Files {
		out = append(out, iec.DirEntry{IOA: uint32(f.IOA), Name: uint16(f.Name), Length: uint32(f.Size), Time: now})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].IOA < out[j].IOA })
	return out, nil
}

func (stationFiles) Read(context.Context, uint32, uint16) ([]byte, error) { return nil, iec.ErrNoFile }

// Write stores a file that arrived complete: from here on it can be fetched and
// is in the directory. A second upload to the same address replaces it.
func (sf stationFiles) Write(_ context.Context, ioa uint32, name uint16, data []byte) error {
	f := &fileSpec{IOA: int(ioa), Name: int(name), Size: len(data), SectionSize: min(len(data), storedSectionSize),
		data: data}
	s := sf.s
	s.mu.Lock()
	defer s.mu.Unlock()
	replaced := false
	for i, old := range s.fx.Files {
		if old.IOA == f.IOA {
			s.fx.Files[i], replaced = f, true
		}
	}
	if !replaced {
		s.fx.Files = append(s.fx.Files, f)
	}
	s.files[f.IOA] = s.fileServer(f)
	return nil
}

func (u *upload) received(success bool) {
	sum := sha256.Sum256(u.arrived)
	e := event("file-received")
	e["ioa"], e["name"], e["length"] = u.spec.IOA, u.name, len(u.arrived)
	e["sha256"], e["success"] = hex.EncodeToString(sum[:]), success
	emit(e)
}

// receive serves the ASDUs of the control direction. The refusal of a file ready
// is decided here, before the library takes it.
func (s *station) receive(ctx context.Context, c *conn, raw []byte, a *iec.ASDU) {
	reject := func() { c.send(ctx, mirror(raw, iec.CauseUnknownObject, true, a.CA)) }
	o := a.Objects[0]
	u := s.uploads[int(o.IOA)]
	if a.Type == iec.FFrNa1 {
		if u == nil || int(o.File) != u.spec.Name || o.Length < 1 || int(o.Length) > u.spec.MaxSize {
			reject()
			return
		}
	} else if u == nil {
		// Nothing is being received at this address: a section ready is refused,
		// and a client's end of a transfer the station refused needs no answer.
		if a.Type == iec.FSrNa1 {
			reject()
		}
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	reply := func(answer *iec.ASDU) {
		answer.CA = s.ca
		if answer.Type == iec.FAfNa1 && len(answer.Objects) == 1 {
			switch answer.Objects[0].Qualifier & 0x0f {
			case iec.AFQSectionOK:
				u.arrived = append(u.arrived, u.section...)
				u.section = nil
			case iec.AFQSectionNegative:
				u.section = nil
			case iec.AFQFileOK, iec.AFQFileNegative:
				// Before the answer: whoever got the answer finds the event.
				if u.active && a.Type == iec.FLsNa1 {
					u.received(answer.Objects[0].Qualifier&0x0f == iec.AFQFileOK)
					u.active = false
				}
			}
		}
		c.sendASDU(ctx, answer)
	}
	switch a.Type {
	case iec.FFrNa1:
		u.active, u.name, u.length, u.arrived, u.section = true, int(o.File), int(o.Length), nil, nil
	case iec.FSrNa1:
		if u.active && int(o.File) == u.name {
			u.nos, u.expected, u.section = o.Section, int(o.Length), nil
		}
	case iec.FSgNa1:
		// The library's own rule for what it keeps.
		if u.active && int(o.File) == u.name && o.Section == u.nos &&
			len(u.arrived)+len(u.section)+len(o.Segment) <= u.length {
			u.section = append(u.section, o.Segment...)
		}
	case iec.FLsNa1:
		switch {
		case !u.active || int(o.File) != u.name:
		case o.Qualifier == iec.LSQSection && o.Section == u.nos && len(u.section) != u.expected:
			reply(&iec.ASDU{Type: iec.FAfNa1, Cause: iec.CauseFile, Orig: a.Orig, Test: a.Test, Objects: []iec.Object{
				{IOA: o.IOA, File: o.File, Section: o.Section, Qualifier: iec.AFQSectionNegative | iec.FileErrChecksum<<4}}})
			return
		case o.Qualifier != iec.LSQSection && o.Qualifier != iec.LSQFile:
			u.received(false) // the controlling station gave up
			u.active = false
		}
	}
	u.srv.Handle(ctx, a, reply)
}
