// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"time"

	"github.com/wendy512/go-iecp5/asdu"
	"github.com/wendy512/go-iecp5/cs104"
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

	// The LFD bit in the status of file: the last entry of the directory.
	sofLastFile = 0x20

	// Fits an APDU with three-octet addresses: see docs/FIXTURES.md.
	maxSegment = 236
	// As many entries of three-octet address, 13 octets each, as an ASDU holds.
	directoryEntries = 15
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

// decodeLength reads the three-octet length of a file or section.
func decodeLength(a *asdu.ASDU) int {
	return int(a.DecodeByte()) | int(a.DecodeByte())<<8 | int(a.DecodeByte())<<16
}

// fileFields decodes the elements of a file transfer type, but for the time
// tag of a directory entry.
func fileFields(a *asdu.ASDU) object {
	o := object{"nof": int(a.DecodeUint16())}
	switch a.Type {
	case asdu.F_FR_NA_1:
		o["lof"] = decodeLength(a)
		o["frq"] = int(a.DecodeByte())
	case asdu.F_SR_NA_1:
		o["nos"] = int(a.DecodeByte())
		o["lof"] = decodeLength(a)
		o["srq"] = int(a.DecodeByte())
	case asdu.F_SC_NA_1:
		o["nos"] = int(a.DecodeByte())
		o["scq"] = int(a.DecodeByte())
	case asdu.F_LS_NA_1:
		o["nos"] = int(a.DecodeByte())
		o["lsq"] = int(a.DecodeByte())
		o["chs"] = int(a.DecodeByte())
	case asdu.F_AF_NA_1:
		o["nos"] = int(a.DecodeByte())
		o["afq"] = int(a.DecodeByte())
	case asdu.F_DR_TA_1:
		o["lof"] = decodeLength(a)
		o["sof"] = int(a.DecodeByte())
	}
	return o
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
	lastSent  bool // the checksum of the file went out
	acked     bool // the file acknowledgement arrived
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
	var ended []*transfer
	s.mu.Lock()
	for k, t := range s.transfers {
		if k.conn == c {
			delete(s.transfers, k)
			if t.requested {
				ended = append(ended, t)
			}
		}
	}
	s.mu.Unlock()
	for _, t := range ended {
		fileEvent(t.file, t.acked)
	}
}

// noteFrame sees every APDU as go-iecp5 receives it, by way of its log. A
// session stops its handler loop as soon as the peer closes, whatever is still
// queued (cs104/server_session.go, handlerLoop). A controlling station's last
// word is the file acknowledgement, after which it closes, so the handler often
// does not get to see it. The frame did arrive: this is where that shows. It
// does not say on which connection, which matters only to two downloads of one
// file that are both at their last step.
func (s *station) noteFrame(apdu []byte) {
	// APCI 6, then type, count, cause, originator, common address 2, address 3,
	// NOF 2, NOS, AFQ, at the only sizes this adapter uses (asdu.ParamsWide).
	if len(apdu) < 19 || apdu[2]&1 != 0 || asdu.TypeID(apdu[6]) != asdu.F_AF_NA_1 ||
		apdu[8]&0x3f != byte(asdu.FileTransfer) || apdu[18]&0x0f != afqFileAck {
		return
	}
	ioa := int(apdu[12]) | int(apdu[13])<<8 | int(apdu[14])<<16
	nof := int(apdu[15]) | int(apdu[16])<<8
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, t := range s.transfers {
		if k.ioa == ioa && t.file.Name == nof && t.lastSent {
			t.acked = true
		}
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
	if request.Coa.Cause != asdu.FileTransfer && request.Coa.Cause != asdu.Request {
		s.reject(c, request, asdu.UnknownCOT)
		return
	}
	ioa, nof, nos := int(pack.DecodeInfoObjAddr()), int(pack.DecodeUint16()), int(pack.DecodeByte())
	scq := pack.DecodeByte() & 0x0f
	key := transferKey{c, ioa}

	if request.Coa.Cause == asdu.Request {
		if scq != scqDirectory {
			s.reject(c, request, asdu.UnknownCOT)
			return
		}
		s.directory(c, request)
		return
	}

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

// directory answers the call of the directory: an entry for every file, in
// ascending address order, the last one with the LFD bit. A station without
// files has nothing to say that fits the procedure, so it refuses the call.
func (s *station) directory(c asdu.Connect, request *asdu.ASDU) {
	files := append([]fileSpec{}, s.fx.Files...)
	if len(files) == 0 {
		s.reject(c, request, asdu.UnknownIOA)
		return
	}
	sort.Slice(files, func(i, j int) bool { return files[i].IOA < files[j].IOA })
	now := time.Now()
	for len(files) > 0 {
		n := min(len(files), directoryEntries)
		a := asdu.NewASDU(s.params, asdu.Identifier{
			Type:     asdu.F_DR_TA_1,
			Variable: asdu.VariableStruct{Number: byte(n)},
			Coa:      asdu.CauseOfTransmission{Cause: asdu.Request},
			OrigAddr: request.OrigAddr, CommonAddr: s.ca,
		})
		for i, f := range files[:n] {
			sof := byte(0)
			if i == n-1 && n == len(files) {
				sof = sofLastFile
			}
			_ = a.AppendInfoObjAddr(asdu.InfoObjAddr(f.IOA))
			appendLength(a.AppendUint16(uint16(f.Name)), f.Size).AppendBytes(sof).AppendCP56Time2a(now, time.UTC)
		}
		send(c, a)
		files = files[n:]
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
		s.mu.Lock()
		t.lastSent = true
		s.mu.Unlock()
		chs := checksum(fileOctets(f.IOA, f.Size))
		send(c, lastSegmentOrSection(s.fileAnswer(asdu.F_LS_NA_1, request, f.IOA), f.Name, f.sections(), lsqLastSection, chs))
	}
}

// The controlling station's side: it sends a file, and it calls the directory.

// parseFilePut reads the flags of file-put.
func parseFilePut(a *args) (ioa, name, size, sectionSize int, err error) {
	if ioa, err = a.num("--ioa", 0); err != nil {
		return
	}
	if name, err = a.num("--name", 1); err != nil {
		return
	}
	if size, err = a.num("--size", 0); err != nil {
		return
	}
	if sectionSize, err = a.num("--section-size", size); err != nil {
		return
	}
	switch {
	case ioa < 1 || ioa > 0xFFFFFF:
		err = usagef("file-put: --ioa is required, 1..16777215")
	case name < 1 || name > 255:
		err = usagef("file-put: --name must be 1..255")
	case size < 1 || size > 65536:
		err = usagef("file-put: --size is required, 1..65536")
	case sectionSize < 1 || sectionSize > size:
		err = usagef("file-put: --section-size must be 1..--size")
	case (size+sectionSize-1)/sectionSize > 255:
		err = usagef("file-put: NOS is one octet, so a file has at most 255 sections")
	}
	return
}

// filePut is the state of a file-put, for the result document.
type filePut struct {
	ioa, name, size, sectionSize int
	data                         []byte
	sections, sent               int
	hash                         []byte // what the station called so far
}

func (p *filePut) result() object {
	sum := sha256.Sum256(p.hash)
	return object{
		"ioa": p.ioa, "name": p.name, "length": p.size, "received": p.sent, "sections": p.sections,
		"sha256": hex.EncodeToString(sum[:]),
	}
}

// sendRetry queues an ASDU, waiting for room: a section is more than the send
// queue of go-iecp5 takes with a small k.
func sendRetry(conn asdu.Connect, a *asdu.ASDU, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		err := conn.Send(a)
		if !errors.Is(err, cs104.ErrBufferFulled) {
			return err
		}
		if time.Now().After(deadline) {
			return failf("timeout", "the station does not take what is sent")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// nextASDU returns the document of the next ASDU after *cursor.
func (s *session) nextASDU(cursor *int, timeout time.Duration, waitingFor string) (object, error) {
	if !s.waitFor(func() bool { return len(s.asdus) > *cursor }, timeout) {
		if s.isClosed() {
			return nil, failf("connection-lost", "connection closed while waiting for %s", waitingFor)
		}
		return nil, failf("timeout", "no %s from the station", waitingFor)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	doc := s.asdus[*cursor]
	*cursor++
	return doc, nil
}

// refusal says whether a document is a refusal, and what kind of error that is.
func refusal(doc object) error {
	cot := doc["cot"].(int)
	if doc["negative"].(bool) || (cot >= 44 && cot <= 47) {
		return failf("negative-confirmation", "the station refused with %v, cause %d", doc["type"], cot)
	}
	return nil
}

// fileElement returns the object of a file transfer document of type typ.
func fileElement(doc object, typ string) (object, bool) {
	objects := doc["objects"].([]object)
	if doc["type"] != typ || len(objects) != 1 {
		return nil, false
	}
	return objects[0], true
}

// runFilePut is the procedure of the contract's file-put.
func runFilePut(s *session, conn asdu.Connect, ca asdu.CommonAddr, oa asdu.OriginAddr, timeout time.Duration, p *filePut) error {
	params := conn.Params()
	out := func(typ asdu.TypeID) *asdu.ASDU { return fileASDU(params, typ, asdu.FileTransfer, oa, ca, p.ioa) }
	cursor := 0
	// await returns the object of the next answer, which must be of type typ
	// and be for this file.
	await := func(typ, waitingFor string) (object, error) {
		doc, err := s.nextASDU(&cursor, timeout, waitingFor)
		if err != nil {
			return nil, err
		}
		if err := refusal(doc); err != nil {
			return nil, err
		}
		o, ok := fileElement(doc, typ)
		if !ok || o["ioa"] != p.ioa || o["nof"] != p.name {
			return nil, failf("failed", "%s: unexpected answer: %v", waitingFor, doc)
		}
		return o, nil
	}
	call := func(scq, nos int, waitingFor string) error {
		o, err := await("F_SC_NA_1", waitingFor)
		if err != nil {
			return err
		}
		if o["scq"].(int)&0x0f != scq || (nos != 0 && o["nos"] != nos) {
			return failf("failed", "%s: got qualifier %v of section %v", waitingFor, o["scq"], o["nos"])
		}
		return nil
	}
	ack := func(want int, waitingFor string) error {
		o, err := await("F_AF_NA_1", waitingFor)
		if err != nil {
			return err
		}
		switch o["afq"].(int) & 0x0f {
		case want:
			return nil
		case afqFileNak, afqSectionNak:
			return failf("negative-confirmation", "%s: the station said not acknowledged (AFQ %v)", waitingFor, o["afq"])
		}
		return failf("failed", "%s: got qualifier %v", waitingFor, o["afq"])
	}

	if err := sendRetry(conn, fileReady(out(asdu.F_FR_NA_1), p.name, p.size), timeout); err != nil {
		return err
	}
	if err := call(scqRequestFile, 0, "the request of the file"); err != nil {
		return err
	}
	last := (p.size + p.sectionSize - 1) / p.sectionSize
	for nos := 1; nos <= last; nos++ {
		begin := (nos - 1) * p.sectionSize
		data := p.data[begin:min(begin+p.sectionSize, p.size)]
		if err := sendRetry(conn, sectionReady(out(asdu.F_SR_NA_1), p.name, nos, len(data)), timeout); err != nil {
			return err
		}
		if err := call(scqRequestSection, nos, "the request of a section"); err != nil {
			return err
		}
		p.sections++
		p.sent += len(data)
		p.hash = append(p.hash, data...)
		for rest := data; len(rest) > 0; {
			n := min(len(rest), maxSegment)
			if err := sendRetry(conn, segment(out(asdu.F_SG_NA_1), p.name, nos, rest[:n]), timeout); err != nil {
				return err
			}
			rest = rest[n:]
		}
		a := lastSegmentOrSection(out(asdu.F_LS_NA_1), p.name, nos, lsqLastSegment, checksum(data))
		if err := sendRetry(conn, a, timeout); err != nil {
			return err
		}
		if err := ack(afqSectionAck, "the acknowledgement of a section"); err != nil {
			return err
		}
	}
	// The NOS here is the client's, and the number of the last section is as
	// good as any: docs/FIXTURES.md.
	a := lastSegmentOrSection(out(asdu.F_LS_NA_1), p.name, last, lsqLastSection, checksum(p.data))
	if err := sendRetry(conn, a, timeout); err != nil {
		return err
	}
	return ack(afqFileAck, "the acknowledgement of the file")
}

// runFileDir is the procedure of the contract's file-dir.
func runFileDir(s *session, conn asdu.Connect, ca asdu.CommonAddr, oa asdu.OriginAddr, timeout time.Duration, directory *[]object) error {
	a := fileASDU(conn.Params(), asdu.F_SC_NA_1, asdu.Request, oa, ca, 0)
	a.AppendUint16(0).AppendBytes(0, scqDirectory)
	if err := sendRetry(conn, a, timeout); err != nil {
		return err
	}
	cursor := 0
	for {
		doc, err := s.nextASDU(&cursor, timeout, "the directory")
		if err != nil {
			return err
		}
		if doc["type"] == "F_SC_NA_1" {
			if err := refusal(doc); err != nil {
				return err
			}
			continue
		}
		if doc["type"] != "F_DR_TA_1" || doc["cot"] != int(asdu.Request) {
			continue
		}
		for _, o := range doc["objects"].([]object) {
			*directory = append(*directory, object{"ioa": o["ioa"], "nof": o["nof"], "lof": o["lof"], "sof": o["sof"]})
			if o["sof"].(int)&sofLastFile != 0 {
				return nil
			}
		}
	}
}
