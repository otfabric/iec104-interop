// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"github.com/wendy512/go-iecp5/asdu"
)

// The file transfer procedure of IEC 60870-5-101 7.4.11. go-iecp5 has none:
// its asdu/filet.go is a TODO. It has the type identifications and lengths for
// everything but the segment, hands what it can decode to the handler for
// other types and sends anything, so the adapter runs the procedure with the
// Append and Decode primitives.

// The qualifiers, in their low nibble; the high nibble is the error cause.
const (
	scqDirectory      = 0
	scqSelectFile     = 1
	scqRequestFile    = 2
	scqDeactivateFile = 3
	scqRequestSection = 6
	scqDeactivateSect = 7

	afqFileAck     = 1
	afqFileNak     = 2
	afqSectionAck  = 3
	afqSectionNak  = 4
	lsqLastSection = 1
	lsqLastSegment = 3

	// Fits an APDU with three-octet addresses: see docs/FIXTURES.md.
	maxSegment = 236
)

// fileOctets is the content of a file: octet i, from 0, is (i + ioa) mod 251.
func fileOctets(ioa, size int) []byte {
	data := make([]byte, size)
	for i := range data {
		data[i] = byte((i + ioa) % 251)
	}
	return data
}

// checksum is the sum of the octets modulo 256.
func checksum(data []byte) byte {
	var sum byte
	for _, b := range data {
		sum += b
	}
	return sum
}

// fileASDU is one information object of a file transfer type. go-iecp5 has no
// helper for any of them.
func fileASDU(p *asdu.Params, typ asdu.TypeID, cause asdu.Cause, oa asdu.OriginAddr, ca asdu.CommonAddr, ioa int) *asdu.ASDU {
	a := asdu.NewASDU(p, asdu.Identifier{
		Type:     typ,
		Variable: asdu.VariableStruct{Number: 1},
		Coa:      asdu.CauseOfTransmission{Cause: cause},
		OrigAddr: oa, CommonAddr: ca,
	})
	_ = a.AppendInfoObjAddr(asdu.InfoObjAddr(ioa))
	return a
}

// appendLength appends the three-octet length of a file or section.
func appendLength(a *asdu.ASDU, n int) *asdu.ASDU {
	return a.AppendBytes(byte(n), byte(n>>8), byte(n>>16))
}

func fileReady(a *asdu.ASDU, nof, length int) *asdu.ASDU {
	return appendLength(a.AppendUint16(uint16(nof)), length).AppendBytes(0)
}

func sectionReady(a *asdu.ASDU, nof, nos, length int) *asdu.ASDU {
	return appendLength(a.AppendUint16(uint16(nof)).AppendBytes(byte(nos)), length).AppendBytes(0)
}

func segment(a *asdu.ASDU, nof, nos int, data []byte) *asdu.ASDU {
	return a.AppendUint16(uint16(nof)).AppendBytes(byte(nos), byte(len(data))).AppendBytes(data...)
}

func lastSegmentOrSection(a *asdu.ASDU, nof, nos, lsq int, chs byte) *asdu.ASDU {
	return a.AppendUint16(uint16(nof)).AppendBytes(byte(nos), byte(lsq), chs)
}

// transferKey is one file on one connection.
type transferKey struct {
	conn asdu.Connect
	ioa  int
}

// transfer is a download in progress. It starts when the file is requested;
// before that a selection is all there is, and nothing can end.
type transfer struct {
	file      *fileSpec
	requested bool
}

func (f *fileSpec) sections() int { return (f.Size + f.SectionSize - 1) / f.SectionSize }

// section is the content of section nos, from 1.
func (f *fileSpec) section(data []byte, nos int) []byte {
	begin := (nos - 1) * f.SectionSize
	return data[begin:min(begin+f.SectionSize, f.Size)]
}

func fileEvent(f *fileSpec, success bool) {
	e := event("file-transfer")
	e["ioa"], e["name"], e["success"] = f.IOA, f.Name, success
	emit(e)
}

// abandonTransfers ends what a closed connection left behind.
func (s *station) abandonTransfers(c asdu.Connect) {
	var ended []*fileSpec
	s.mu.Lock()
	for k, t := range s.transfers {
		if k.conn == c {
			delete(s.transfers, k)
			if t.requested {
				ended = append(ended, t.file)
			}
		}
	}
	s.mu.Unlock()
	for _, f := range ended {
		fileEvent(f, false)
	}
}

// endTransfer forgets a transfer, reporting it if it had started.
func (s *station) endTransfer(key transferKey, success bool) {
	s.mu.Lock()
	t := s.transfers[key]
	delete(s.transfers, key)
	s.mu.Unlock()
	if t != nil && t.requested {
		fileEvent(t.file, success)
	}
}

// transferOf returns the transfer of key, starting one if the address and name
// are those of a file: a controlling station may request without selecting.
func (s *station) transferOf(key transferKey, nof int) *transfer {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t := s.transfers[key]; t != nil && t.file.Name == nof {
		return t
	}
	f := s.fx.file(key.ioa, nof)
	if f == nil {
		return nil
	}
	t := &transfer{file: f}
	s.transfers[key] = t
	return t
}

func (s *station) fileAnswer(typ asdu.TypeID, request *asdu.ASDU, ioa int) *asdu.ASDU {
	return fileASDU(s.params, typ, asdu.FileTransfer, request.OrigAddr, s.ca, ioa)
}

// fileCall handles F_SC_NA_1. pack is consumed; request is the mirror.
func (s *station) fileCall(c asdu.Connect, request, pack *asdu.ASDU) {
	if request.Coa.Cause != asdu.FileTransfer {
		s.reject(c, request, asdu.UnknownCOT)
		return
	}
	ioa, nof, nos := int(pack.DecodeInfoObjAddr()), int(pack.DecodeUint16()), int(pack.DecodeByte())
	scq := pack.DecodeByte() & 0x0f
	key := transferKey{c, ioa}

	switch scq {
	case scqDeactivateFile, scqDeactivateSect:
		// Of any address and name, and without an answer.
		s.endTransfer(key, false)
		return
	case scqSelectFile, scqRequestFile, scqRequestSection:
	default:
		s.reject(c, request, asdu.UnknownIOA)
		return
	}
	t := s.transferOf(key, nof)
	if t == nil {
		s.reject(c, request, asdu.UnknownIOA)
		return
	}
	f := t.file
	switch scq {
	case scqSelectFile:
		send(c, fileReady(s.fileAnswer(asdu.F_FR_NA_1, request, ioa), f.Name, f.Size))
	case scqRequestFile:
		s.mu.Lock()
		t.requested = true
		s.mu.Unlock()
		s.sectionReady(c, request, f, 1)
	default: // scqRequestSection
		if nos < 1 || nos > f.sections() {
			s.reject(c, request, asdu.UnknownIOA)
			return
		}
		s.sendSection(c, request, f, nos)
	}
}

func (s *station) sectionReady(c asdu.Connect, request *asdu.ASDU, f *fileSpec, nos int) {
	length := len(f.section(fileOctets(f.IOA, f.Size), nos))
	send(c, sectionReady(s.fileAnswer(asdu.F_SR_NA_1, request, f.IOA), f.Name, nos, length))
}

// sendSection sends a section in segments, then its checksum.
func (s *station) sendSection(c asdu.Connect, request *asdu.ASDU, f *fileSpec, nos int) {
	data := f.section(fileOctets(f.IOA, f.Size), nos)
	chs := checksum(data)
	for len(data) > 0 {
		n := min(len(data), maxSegment)
		send(c, segment(s.fileAnswer(asdu.F_SG_NA_1, request, f.IOA), f.Name, nos, data[:n]))
		data = data[n:]
	}
	send(c, lastSegmentOrSection(s.fileAnswer(asdu.F_LS_NA_1, request, f.IOA), f.Name, nos, lsqLastSegment, chs))
}

// fileAck handles F_AF_NA_1, which only means something during a transfer.
func (s *station) fileAck(c asdu.Connect, request, pack *asdu.ASDU) {
	if request.Coa.Cause != asdu.FileTransfer {
		s.reject(c, request, asdu.UnknownCOT)
		return
	}
	ioa, nof := int(pack.DecodeInfoObjAddr()), int(pack.DecodeUint16())
	nos := int(pack.DecodeByte())
	afq := pack.DecodeByte() & 0x0f
	key := transferKey{c, ioa}

	s.mu.Lock()
	t := s.transfers[key]
	s.mu.Unlock()
	if t == nil || t.file.Name != nof {
		return
	}
	switch afq {
	case afqFileAck:
		s.endTransfer(key, true)
	case afqFileNak, afqSectionNak:
		s.endTransfer(key, false)
	case afqSectionAck:
		f := t.file
		if nos < f.sections() {
			s.sectionReady(c, request, f, nos+1)
			return
		}
		// The NOS of this one is the library's choice (docs/FIXTURES.md); the
		// number of the last section, as cB-Sigma/iec60870.
		chs := checksum(fileOctets(f.IOA, f.Size))
		send(c, lastSegmentOrSection(s.fileAnswer(asdu.F_LS_NA_1, request, f.IOA), f.Name, f.sections(), lsqLastSection, chs))
	}
}
