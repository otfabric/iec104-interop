// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"encoding/binary"
	"math"
	"time"

	"github.com/pascaldekloe/part5"
	"github.com/pascaldekloe/part5/info"
)

// Conversions between the fixture model, part5 data units and the JSON of the
// contract.
//
// Monitor direction is decoded by part5 (part5.MonitorDataUnit); its element
// types (info.SinglePtQual, info.Norm, info.Counter, info.CP56Time2a ...)
// encode and decode every information element. part5 has no decoder for the
// control direction, so the adapter splits those ASDUs into their elements
// itself and reads each with the part5 element type.

// typeNames are the types this adapter models, by the names of the contract.
// The file transfer types are missing on purpose: part5 has no procedure for
// them and this adapter declares no file feature.
var typeNames = map[info.TypeID]string{
	info.M_SP_NA_1: "M_SP_NA_1", info.M_DP_NA_1: "M_DP_NA_1", info.M_ST_NA_1: "M_ST_NA_1", info.M_BO_NA_1: "M_BO_NA_1",
	info.M_ME_NA_1: "M_ME_NA_1", info.M_ME_NB_1: "M_ME_NB_1", info.M_ME_NC_1: "M_ME_NC_1", info.M_IT_NA_1: "M_IT_NA_1",
	info.M_SP_TB_1: "M_SP_TB_1", info.M_DP_TB_1: "M_DP_TB_1", info.M_ST_TB_1: "M_ST_TB_1", info.M_BO_TB_1: "M_BO_TB_1",
	info.M_ME_TD_1: "M_ME_TD_1", info.M_ME_TE_1: "M_ME_TE_1", info.M_ME_TF_1: "M_ME_TF_1", info.M_IT_TB_1: "M_IT_TB_1",
	info.C_SC_NA_1: "C_SC_NA_1", info.C_DC_NA_1: "C_DC_NA_1", info.C_RC_NA_1: "C_RC_NA_1",
	info.C_SE_NA_1: "C_SE_NA_1", info.C_SE_NB_1: "C_SE_NB_1", info.C_SE_NC_1: "C_SE_NC_1",
	info.C_SC_TA_1: "C_SC_TA_1", info.C_DC_TA_1: "C_DC_TA_1", info.C_RC_TA_1: "C_RC_TA_1",
	info.C_SE_TA_1: "C_SE_TA_1", info.C_SE_TB_1: "C_SE_TB_1", info.C_SE_TC_1: "C_SE_TC_1",
	info.M_EI_NA_1: "M_EI_NA_1", info.C_IC_NA_1: "C_IC_NA_1", info.C_CI_NA_1: "C_CI_NA_1",
	info.C_RD_NA_1: "C_RD_NA_1", info.C_CS_NA_1: "C_CS_NA_1", info.C_TS_TA_1: "C_TS_TA_1",
}

// timeTagged maps a point type to its CP56Time2a variant.
var timeTagged = map[info.TypeID]info.TypeID{
	info.M_SP_NA_1: info.M_SP_TB_1, info.M_DP_NA_1: info.M_DP_TB_1, info.M_ST_NA_1: info.M_ST_TB_1,
	info.M_BO_NA_1: info.M_BO_TB_1, info.M_ME_NA_1: info.M_ME_TD_1, info.M_ME_NB_1: info.M_ME_TE_1,
	info.M_ME_NC_1: info.M_ME_TF_1, info.M_IT_NA_1: info.M_IT_TB_1,
}

// timeTaggedCommand maps a command type to its CP56Time2a variant.
var timeTaggedCommand = map[info.TypeID]info.TypeID{
	info.C_SC_NA_1: info.C_SC_TA_1, info.C_DC_NA_1: info.C_DC_TA_1, info.C_RC_NA_1: info.C_RC_TA_1,
	info.C_SE_NA_1: info.C_SE_TA_1, info.C_SE_NB_1: info.C_SE_TB_1, info.C_SE_NC_1: info.C_SE_TC_1,
}

// plainCommand is the inverse of timeTaggedCommand, plus the plain types.
var plainCommand = map[info.TypeID]info.TypeID{}

func init() {
	for plain, timed := range timeTaggedCommand {
		plainCommand[plain] = plain
		plainCommand[timed] = plain
	}
}

const timeLayout = "2006-01-02T15:04:05.000Z"

func formatTime(t time.Time) string { return t.UTC().Format(timeLayout) }

func hasFlag(quality []string, flag string) bool {
	for _, q := range quality {
		if q == flag {
			return true
		}
	}
	return false
}

func qualOf(quality []string) info.Qual {
	var q info.Qual
	for _, f := range quality {
		switch f {
		case "OV":
			q |= info.OV
		case "BL":
			q |= info.BL
		case "SB":
			q |= info.SB
		case "NT":
			q |= info.NT
		case "IV":
			q |= info.IV
		}
	}
	return q
}

// cp56 is the time tag part5 encodes for t, in UTC.
func cp56(t time.Time) info.CP56Time2a {
	var tag info.CP56Time2a
	tag.Set(t.UTC())
	return tag
}

// appendPoint appends the information object of a point to u.
func appendPoint(u *unit, p *point, timed bool, now time.Time) {
	appendIOA(u, p.IOA)
	q := qualOf(p.Quality)
	switch p.typ {
	case info.M_SP_NA_1:
		u.Info = append(u.Info, byte(info.NewSinglePtQual(info.SinglePt(int(p.value)), q&^info.OV)))
	case info.M_DP_NA_1:
		u.Info = append(u.Info, byte(info.NewDoublePtQual(info.DoublePt(int(p.value)), q&^info.OV)))
	case info.M_ST_NA_1:
		var s info.StepQual
		if p.Transient != nil && *p.Transient {
			s = info.NewTransientStepQual(int(p.value), q)
		} else {
			s = info.NewStepQual(int(p.value), q)
		}
		u.Info = append(u.Info, s[:]...)
	case info.M_BO_NA_1:
		// The bitstring is four octets, BSI bit 1 in the low bit of the first
		// one. part5 has an integer helper only for the big-endian reading
		// of them (BitsQual.SetBigEndian); the contract's value reads them
		// little-endian, as every other stack does.
		var b info.BitsQual
		binary.LittleEndian.PutUint32(b.Slice(), uint32(p.value))
		b.FlagQual(q)
		u.Info = append(u.Info, b[:]...)
	case info.M_ME_NA_1:
		var n info.NormQual
		n.Ref().SetInt16(int16(p.value))
		n.FlagQual(q)
		u.Info = append(u.Info, n[:]...)
	case info.M_ME_NB_1:
		u.Info = binary.LittleEndian.AppendUint16(u.Info, uint16(int16(p.value)))
		u.Info = append(u.Info, byte(q))
	case info.M_ME_NC_1:
		u.Info = binary.LittleEndian.AppendUint32(u.Info, math.Float32bits(float32(p.value)))
		u.Info = append(u.Info, byte(q))
	case info.M_IT_NA_1:
		var c info.Counter
		c.SetCount(int32(p.value))
		if p.Sequence != nil {
			c.SetSeqNo(uint(*p.Sequence))
		}
		if hasFlag(p.Quality, "CY") {
			c.SetCarry()
		}
		if hasFlag(p.Quality, "CA") {
			c.FlagAdjusted()
		}
		if hasFlag(p.Quality, "IV") {
			c.FlagInvalid()
		}
		u.Info = append(u.Info, c[:]...)
	}
	if timed {
		tag := cp56(now)
		u.Info = append(u.Info, tag[:]...)
	}
}

func qualityJSON(q info.Qual, overflow bool) []string {
	out := []string{}
	if overflow && q&info.OV != 0 {
		out = append(out, "OV")
	}
	for _, f := range []struct {
		flag info.Qual
		name string
	}{{info.BL, "BL"}, {info.SB, "SB"}, {info.NT, "NT"}, {info.IV, "IV"}} {
		if q&f.flag != 0 {
			out = append(out, f.name)
		}
	}
	return out
}

type object = map[string]any

func timeJSON(o object, tag info.CP56Time2a) {
	if tag.Invalid() {
		o["timeInvalid"] = true
	}
	// Within20thCentury gives the zero time for a tag with the IV bit; the
	// contract wants the time that is on the wire either way.
	tag[2] &^= byte(info.IV)
	o["time"] = formatTime(tag.Within20thCentury(time.UTC))
}

func normalized(o object, v int16) {
	o["value"] = int(v)
	o["normalized"] = float64(v) / 32768
}

// collector is the part5.Monitor of the adapter: every information object
// part5 hands it becomes a JSON object.
type collector struct {
	objects []object
}

func (c *collector) add(addr ioa3) object {
	o := object{"ioa": int(addr.N())}
	c.objects = append(c.objects, o)
	return o
}

func (c *collector) addAt(addr ioa3, tag info.CP56Time2a) object {
	o := c.add(addr)
	timeJSON(o, tag)
	return o
}

func singleJSON(o object, p info.SinglePtQual) {
	o["value"] = p.Pt() == info.On
	o["quality"] = qualityJSON(p.Qual(), false)
}

func doubleJSON(o object, p info.DoublePtQual) {
	o["value"] = int(p.Pt())
	o["quality"] = qualityJSON(p.Qual(), false)
}

func stepJSON(o object, p info.StepQual) {
	v, transient := p.Step().Pos()
	o["value"], o["transient"] = v, transient
	o["quality"] = qualityJSON(p.Qual(), true)
}

func bitsJSON(o object, b info.BitsQual) {
	o["value"] = binary.LittleEndian.Uint32(b.Slice())
	o["quality"] = qualityJSON(b.Qual(), true)
}

func normJSON(o object, n info.NormQual) {
	normalized(o, n.Ref().Int16())
	o["quality"] = qualityJSON(n.Qual(), true)
}

func totalsJSON(o object, c info.Counter) {
	o["value"] = int(c.Count())
	o["sequence"] = int(c.SeqNo())
	q := []string{}
	if c.Carry() {
		q = append(q, "CY")
	}
	if c.Adjusted() {
		q = append(q, "CA")
	}
	if c.Invalid() {
		q = append(q, "IV")
	}
	o["quality"] = q
}

func (c *collector) SinglePt(_ unit, a ioa3, p info.SinglePtQual) { singleJSON(c.add(a), p) }
func (c *collector) SinglePtAtMinute(unit, ioa3, info.SinglePtQual, info.CP24Time2a) {
}
func (c *collector) SinglePtAtMoment(_ unit, a ioa3, p info.SinglePtQual, t info.CP56Time2a) {
	singleJSON(c.addAt(a, t), p)
}
func (c *collector) SinglePtChangePack(unit, ioa3, info.SinglePtChangePack, info.Qual) {}
func (c *collector) DoublePt(_ unit, a ioa3, p info.DoublePtQual)                      { doubleJSON(c.add(a), p) }
func (c *collector) DoublePtAtMinute(unit, ioa3, info.DoublePtQual, info.CP24Time2a) {
}
func (c *collector) DoublePtAtMoment(_ unit, a ioa3, p info.DoublePtQual, t info.CP56Time2a) {
	doubleJSON(c.addAt(a, t), p)
}
func (c *collector) Step(_ unit, a ioa3, p info.StepQual)                    { stepJSON(c.add(a), p) }
func (c *collector) StepAtMinute(unit, ioa3, info.StepQual, info.CP24Time2a) {}
func (c *collector) StepAtMoment(_ unit, a ioa3, p info.StepQual, t info.CP56Time2a) {
	stepJSON(c.addAt(a, t), p)
}
func (c *collector) Bits(_ unit, a ioa3, b info.BitsQual)                    { bitsJSON(c.add(a), b) }
func (c *collector) BitsAtMinute(unit, ioa3, info.BitsQual, info.CP24Time2a) {}
func (c *collector) BitsAtMoment(_ unit, a ioa3, b info.BitsQual, t info.CP56Time2a) {
	bitsJSON(c.addAt(a, t), b)
}
func (c *collector) NormUnqual(unit, ioa3, info.Norm)                        {}
func (c *collector) Norm(_ unit, a ioa3, n info.NormQual)                    { normJSON(c.add(a), n) }
func (c *collector) NormAtMinute(unit, ioa3, info.NormQual, info.CP24Time2a) {}
func (c *collector) NormAtMoment(_ unit, a ioa3, n info.NormQual, t info.CP56Time2a) {
	normJSON(c.addAt(a, t), n)
}
func (c *collector) Scaled(_ unit, a ioa3, v int16, q info.Qual) {
	o := c.add(a)
	o["value"], o["quality"] = int(v), qualityJSON(q, true)
}
func (c *collector) ScaledAtMinute(unit, ioa3, int16, info.Qual, info.CP24Time2a) {}
func (c *collector) ScaledAtMoment(_ unit, a ioa3, v int16, q info.Qual, t info.CP56Time2a) {
	o := c.addAt(a, t)
	o["value"], o["quality"] = int(v), qualityJSON(q, true)
}
func (c *collector) Float(_ unit, a ioa3, f float32, q info.Qual) {
	o := c.add(a)
	o["value"], o["quality"] = float64(f), qualityJSON(q, true)
}
func (c *collector) FloatAtMinute(unit, ioa3, float32, info.Qual, info.CP24Time2a) {}
func (c *collector) FloatAtMoment(_ unit, a ioa3, f float32, q info.Qual, t info.CP56Time2a) {
	o := c.addAt(a, t)
	o["value"], o["quality"] = float64(f), qualityJSON(q, true)
}
func (c *collector) Totals(_ unit, a ioa3, v info.Counter)                    { totalsJSON(c.add(a), v) }
func (c *collector) TotalsAtMinute(unit, ioa3, info.Counter, info.CP24Time2a) {}
func (c *collector) TotalsAtMoment(_ unit, a ioa3, v info.Counter, t info.CP56Time2a) {
	totalsJSON(c.addAt(a, t), v)
}
func (c *collector) ProtectAtMinute(unit, ioa3, info.ProtectEvent, info.CP24Time2a)           {}
func (c *collector) ProtectAtMoment(unit, ioa3, info.ProtectEvent, info.CP56Time2a)           {}
func (c *collector) ProtectStartAtMinute(unit, ioa3, info.ProtectStartEvent, info.CP24Time2a) {}
func (c *collector) ProtectStartAtMoment(unit, ioa3, info.ProtectStartEvent, info.CP56Time2a) {}
func (c *collector) ProtectOutAtMinute(unit, ioa3, info.ProtectOutEvent, info.CP24Time2a)     {}
func (c *collector) ProtectOutAtMoment(unit, ioa3, info.ProtectOutEvent, info.CP56Time2a)     {}
func (c *collector) InitEnd(u unit, cause info.InitCause) {
	// part5 hands no address for M_EI_NA_1: it is the first three octets.
	o := object{"ioa": int(ioa3(u.Info[:3]).N()), "coi": int(cause)}
	c.objects = append(c.objects, o)
}

var _ part5.Monitor[oa8, ca16, ioa3] = (*collector)(nil)

// element is one information object of a control direction ASDU: its address
// and the octets after it.
type element struct {
	ioa  int
	body []byte
}

// elements splits a control direction ASDU (SQ = 0) into objects of size
// octets after the address. ok is false when the length does not add up.
func elements(u unit, size int) ([]element, bool) {
	n := u.Enc.Count()
	if u.Enc.AddrSeq() || len(u.Info) != n*(3+size) {
		return nil, false
	}
	out := make([]element, n)
	for i := range out {
		at := i * (3 + size)
		out[i] = element{int(ioa3(u.Info[at : at+3]).N()), u.Info[at+3 : at+3+size]}
	}
	return out, true
}

// controlSize is the size of the information element of a control direction
// type, without the address.
var controlSize = map[info.TypeID]int{
	info.C_SC_NA_1: 1, info.C_DC_NA_1: 1, info.C_RC_NA_1: 1, info.C_SE_NA_1: 3, info.C_SE_NB_1: 3, info.C_SE_NC_1: 5,
	info.C_SC_TA_1: 8, info.C_DC_TA_1: 8, info.C_RC_TA_1: 8, info.C_SE_TA_1: 10, info.C_SE_TB_1: 10, info.C_SE_TC_1: 12,
	info.C_IC_NA_1: 1, info.C_CI_NA_1: 1, info.C_RD_NA_1: 0, info.C_CS_NA_1: 7, info.C_TS_TA_1: 9,
}

// commandValue reads the value and the S/E bit of a process command element.
// value is what the fixture model takes; eventValue what the documents show.
func commandValue(t info.TypeID, body []byte) (value float64, eventValue any, sel bool, qualifier int) {
	switch plainCommand[t] {
	case info.C_SC_NA_1:
		q := info.CmdQual(body[0] &^ 1)
		on := info.SinglePt(body[0]&1) == info.On
		if on {
			value = 1
		}
		return value, on, q.Select(), int(q.Additional())
	case info.C_DC_NA_1:
		q := info.CmdQual(body[0] &^ 3)
		v := int(info.DoublePt(body[0] & 3))
		return float64(v), v, q.Select(), int(q.Additional())
	case info.C_RC_NA_1:
		q := info.CmdQual(body[0] &^ 3)
		v := int(info.Regul(body[0] & 3))
		return float64(v), v, q.Select(), int(q.Additional())
	case info.C_SE_NA_1:
		v := info.Norm(body[:2]).Int16()
		q := info.SetPtQual(body[2])
		return float64(v), int(v), q.Select(), int(q.N())
	case info.C_SE_NB_1:
		v := int16(binary.LittleEndian.Uint16(body[:2]))
		q := info.SetPtQual(body[2])
		return float64(v), int(v), q.Select(), int(q.N())
	default: // C_SE_NC_1
		v := math.Float32frombits(binary.LittleEndian.Uint32(body[:4]))
		q := info.SetPtQual(body[4])
		return float64(v), float64(v), q.Select(), int(q.N())
	}
}

// controlJSON decodes the objects of a control direction ASDU.
func controlJSON(u unit) []object {
	size, ok := controlSize[u.Type]
	if !ok {
		return []object{}
	}
	elems, ok := elements(u, size)
	if !ok {
		logf("cannot decode %s: %d octets for %d objects", u.Type, len(u.Info), u.Enc.Count())
		return []object{}
	}
	objects := []object{}
	for _, e := range elems {
		o := object{"ioa": e.ioa}
		switch u.Type {
		case info.C_IC_NA_1:
			o["qoi"] = int(e.body[0])
		case info.C_CI_NA_1:
			o["qcc"] = int(e.body[0])
		case info.C_RD_NA_1:
		case info.C_CS_NA_1:
			timeJSON(o, info.CP56Time2a(e.body[:7]))
		case info.C_TS_TA_1:
			o["counter"] = int(binary.LittleEndian.Uint16(e.body[:2]))
			timeJSON(o, info.CP56Time2a(e.body[2:9]))
		default: // process commands
			_, v, sel, q := commandValue(u.Type, e.body)
			if plainCommand[u.Type] == info.C_SE_NA_1 {
				normalized(o, int16(v.(int)))
			} else {
				o["value"] = v
			}
			o["select"], o["qualifier"] = sel, q
			if u.Type != plainCommand[u.Type] {
				timeJSON(o, info.CP56Time2a(e.body[size-7:size]))
			}
		}
		objects = append(objects, o)
	}
	return objects
}

// asduJSON is the ASDU document of the contract for an ASDU as received. ok
// is false when part5 cannot read its data unit identifier.
func asduJSON(raw []byte) (object, unit, bool) {
	u := params.NewDataUnit()
	if err := u.Adopt(raw); err != nil {
		logf("cannot read ASDU % x: %v", raw, err)
		return nil, u, false
	}
	j := object{
		"type": nil, "typeId": int(u.Type),
		"cot": int(u.Cause & 0x3f), "negative": u.Cause&info.NegFlag != 0, "test": u.Cause&info.TestFlag != 0,
		"originator": int(u.Orig.N()), "commonAddress": int(u.Addr.N()),
		"sequence": u.Enc.AddrSeq(), "count": u.Enc.Count(),
		"objects": []object{},
	}
	name, known := typeNames[u.Type]
	if !known {
		return j, u, true
	}
	j["type"] = name
	if u.Type <= 44 || u.Type == info.M_EI_NA_1 {
		c := &collector{objects: []object{}}
		var err error
		if u.Type == info.M_EI_NA_1 {
			// part5 dispatches M_EI_NA_1 only within MonitorDataUnit's range
			// check of 1..44, which it is not in.
			if len(u.Info) == 4 {
				c.InitEnd(u, info.InitCause(u.Info[3]))
			}
		} else {
			err = part5.MonitorDataUnit[oa8, ca16, ioa3](c, u)
		}
		if err != nil {
			logf("part5 cannot decode %s: %v", name, err)
		}
		j["objects"] = c.objects
	} else {
		j["objects"] = controlJSON(u)
	}
	return j, u, true
}
