// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"encoding/hex"
	"math"
	"time"

	iec "github.com/cB-Sigma/iec60870"
)

// Conversions between the fixture model, cB-Sigma's ASDUs and the JSON of
// the contract.

// params are the field sizes of IEC 60870-5-104.
var params = iec.Params{}.WithDefaults()

// typeNames are the types the contract models; anything else is reported
// with "type": null.
var typeNames = map[iec.TypeID]string{}

func init() {
	for _, t := range []iec.TypeID{
		iec.MSpNa1, iec.MDpNa1, iec.MStNa1, iec.MBoNa1, iec.MMeNa1, iec.MMeNb1, iec.MMeNc1, iec.MItNa1,
		iec.MSpTb1, iec.MDpTb1, iec.MStTb1, iec.MBoTb1, iec.MMeTd1, iec.MMeTe1, iec.MMeTf1, iec.MItTb1,
		iec.CScNa1, iec.CDcNa1, iec.CRcNa1, iec.CSeNa1, iec.CSeNb1, iec.CSeNc1,
		iec.CScTa1, iec.CDcTa1, iec.CRcTa1, iec.CSeTa1, iec.CSeTb1, iec.CSeTc1,
		iec.MEiNa1, iec.CIcNa1, iec.CCiNa1, iec.CRdNa1, iec.CCsNa1, iec.CTsTa1,
		iec.FFrNa1, iec.FSrNa1, iec.FScNa1, iec.FLsNa1, iec.FAfNa1, iec.FSgNa1,
	} {
		typeNames[t] = t.String()
	}
}

// timeTagged maps a point type to its CP56Time2a variant.
var timeTagged = map[iec.TypeID]iec.TypeID{
	iec.MSpNa1: iec.MSpTb1, iec.MDpNa1: iec.MDpTb1, iec.MStNa1: iec.MStTb1, iec.MBoNa1: iec.MBoTb1,
	iec.MMeNa1: iec.MMeTd1, iec.MMeNb1: iec.MMeTe1, iec.MMeNc1: iec.MMeTf1, iec.MItNa1: iec.MItTb1,
}

// timeTaggedCommand maps a command type to its CP56Time2a variant.
var timeTaggedCommand = map[iec.TypeID]iec.TypeID{
	iec.CScNa1: iec.CScTa1, iec.CDcNa1: iec.CDcTa1, iec.CRcNa1: iec.CRcTa1,
	iec.CSeNa1: iec.CSeTa1, iec.CSeNb1: iec.CSeTb1, iec.CSeNc1: iec.CSeTc1,
}

// untimedCommand is the inverse of timeTaggedCommand, the identity for a
// command without time tag.
func untimedCommand(t iec.TypeID) iec.TypeID {
	for plain, timed := range timeTaggedCommand {
		if t == timed {
			return plain
		}
	}
	return t
}

const timeLayout = "2006-01-02T15:04:05.000Z"

func formatTime(t time.Time) string { return t.UTC().Format(timeLayout) }

func qualityBits(quality []string) uint8 {
	var q uint8
	for _, f := range quality {
		switch f {
		case "OV":
			q |= iec.QOverflow
		case "BL":
			q |= iec.QBlocked
		case "SB":
			q |= iec.QSubstituted
		case "NT":
			q |= iec.QNotTopical
		case "IV":
			q |= iec.QInvalid
		case "CY":
			q |= iec.QCarry
		case "CA":
			q |= iec.QAdjusted
		}
	}
	return q
}

// pointObject is the information object of a point.
func pointObject(p *point) iec.Object {
	o := iec.Object{IOA: uint32(p.IOA), Value: p.value, Quality: qualityBits(p.Quality)}
	switch p.typ {
	case iec.MMeNa1:
		o.Value = p.value / 32768 // cB-Sigma takes normalised values as -1 .. <1
	case iec.MStNa1:
		o.Transient = p.Transient != nil && *p.Transient
	case iec.MItNa1:
		if p.Sequence != nil {
			o.Seq = uint8(*p.Sequence)
		}
	}
	return o
}

// mirror returns the request, as received, with another cause, P/N bit and
// common address: the mirror of the request the contract asks for. It works
// on the octets so that the answer is the request even where a decoder
// would lose something.
func mirror(raw []byte, cause iec.Cause, negative bool, ca uint16) []byte {
	b := append([]byte(nil), raw...)
	b[2] = b[2]&0x80 | byte(cause)&0x3f
	if negative {
		b[2] |= 0x40
	}
	b[4], b[5] = byte(ca), byte(ca>>8)
	return b
}

type object = map[string]any

func qualityJSON(q uint8, overflow bool) []string {
	out := []string{}
	if overflow && q&iec.QOverflow != 0 {
		out = append(out, "OV")
	}
	for _, f := range []struct {
		bit  uint8
		name string
	}{{iec.QBlocked, "BL"}, {iec.QSubstituted, "SB"}, {iec.QNotTopical, "NT"}, {iec.QInvalid, "IV"}} {
		if q&f.bit != 0 {
			out = append(out, f.name)
		}
	}
	return out
}

func counterQualityJSON(q uint8) []string {
	out := []string{}
	for _, f := range []struct {
		bit  uint8
		name string
	}{{iec.QCarry, "CY"}, {iec.QAdjusted, "CA"}, {iec.QInvalid, "IV"}} {
		if q&f.bit != 0 {
			out = append(out, f.name)
		}
	}
	return out
}

// rawNormalised is the 16-bit form of a normalised value.
func rawNormalised(v float64) int {
	return int(max(math.MinInt16, min(math.MaxInt16, math.Round(v*32768))))
}

func normalized(o object, v float64) {
	raw := rawNormalised(v)
	o["value"] = raw
	o["normalized"] = float64(raw) / 32768
}

func hasTime(t iec.TypeID) bool {
	switch t {
	case iec.MSpTb1, iec.MDpTb1, iec.MStTb1, iec.MBoTb1, iec.MMeTd1, iec.MMeTe1, iec.MMeTf1, iec.MItTb1,
		iec.CScTa1, iec.CDcTa1, iec.CRcTa1, iec.CSeTa1, iec.CSeTb1, iec.CSeTc1, iec.CCsNa1, iec.CTsTa1:
		return true
	}
	return false
}

// objectJSON is the contract's form of one information object of type t.
func objectJSON(t iec.TypeID, i iec.Object) object {
	o := object{"ioa": int(i.IOA)}
	if hasTime(t) {
		o["time"] = formatTime(i.Time)
		if i.TimeInvalid {
			o["timeInvalid"] = true
		}
	}
	switch t {
	case iec.MSpNa1, iec.MSpTb1:
		o["value"], o["quality"] = i.Value != 0, qualityJSON(i.Quality, false)
	case iec.MDpNa1, iec.MDpTb1:
		o["value"], o["quality"] = int(i.Value), qualityJSON(i.Quality, false)
	case iec.MStNa1, iec.MStTb1:
		o["value"], o["transient"], o["quality"] = int(i.Value), i.Transient, qualityJSON(i.Quality, true)
	case iec.MBoNa1, iec.MBoTb1:
		o["value"], o["quality"] = uint32(i.Value), qualityJSON(i.Quality, true)
	case iec.MMeNa1, iec.MMeTd1:
		normalized(o, i.Value)
		o["quality"] = qualityJSON(i.Quality, true)
	case iec.MMeNb1, iec.MMeTe1:
		o["value"], o["quality"] = int(i.Value), qualityJSON(i.Quality, true)
	case iec.MMeNc1, iec.MMeTf1:
		o["value"], o["quality"] = i.Value, qualityJSON(i.Quality, true)
	case iec.MItNa1, iec.MItTb1:
		o["value"], o["sequence"], o["quality"] = int(i.Value), int(i.Seq), counterQualityJSON(i.Quality)
	case iec.CScNa1, iec.CScTa1:
		o["value"], o["select"], o["qualifier"] = i.Value != 0, i.Select, int(i.Qualifier)
	case iec.CDcNa1, iec.CDcTa1, iec.CRcNa1, iec.CRcTa1:
		o["value"], o["select"], o["qualifier"] = int(i.Value), i.Select, int(i.Qualifier)
	case iec.CSeNa1, iec.CSeTa1:
		normalized(o, i.Value)
		o["select"], o["qualifier"] = i.Select, int(i.Qualifier)
	case iec.CSeNb1, iec.CSeTb1:
		o["value"], o["select"], o["qualifier"] = int(i.Value), i.Select, int(i.Qualifier)
	case iec.CSeNc1, iec.CSeTc1:
		o["value"], o["select"], o["qualifier"] = i.Value, i.Select, int(i.Qualifier)
	case iec.MEiNa1:
		o["coi"] = int(i.Value)
	case iec.CIcNa1:
		o["qoi"] = int(i.Value)
	case iec.CCiNa1:
		o["qcc"] = int(i.Value)
	case iec.CTsTa1:
		o["counter"] = int(i.Value)
	case iec.FFrNa1:
		o["nof"], o["lof"], o["frq"] = int(i.File), int(i.Length), int(i.Qualifier)
	case iec.FSrNa1:
		o["nof"], o["nos"], o["lof"], o["srq"] = int(i.File), int(i.Section), int(i.Length), int(i.Qualifier)
	case iec.FScNa1:
		o["nof"], o["nos"], o["scq"] = int(i.File), int(i.Section), int(i.Qualifier)
	case iec.FLsNa1:
		o["nof"], o["nos"], o["lsq"], o["chs"] = int(i.File), int(i.Section), int(i.Qualifier), int(i.Checksum)
	case iec.FAfNa1:
		o["nof"], o["nos"], o["afq"] = int(i.File), int(i.Section), int(i.Qualifier)
	case iec.FSgNa1:
		o["nof"], o["nos"], o["data"] = int(i.File), int(i.Section), hex.EncodeToString(i.Segment)
	}
	return o
}

// asduJSON is the ASDU document of the contract for a received ASDU; a is
// what the decoder made of raw, nil or partial when it failed.
func asduJSON(raw []byte, a *iec.ASDU, decodeErr error) object {
	j := object{"type": nil, "typeId": int(raw[0]), "objects": []object{}}
	if len(raw) >= 6 {
		j["cot"], j["negative"], j["test"] = int(raw[2]&0x3f), raw[2]&0x40 != 0, raw[2]&0x80 != 0
		j["originator"], j["commonAddress"] = int(raw[3]), int(raw[4])|int(raw[5])<<8
		j["sequence"], j["count"] = raw[1]&0x80 != 0, int(raw[1]&0x7f)
	}
	name, modelled := typeNames[iec.TypeID(raw[0])]
	if !modelled || decodeErr != nil || a == nil {
		return j
	}
	j["type"] = name
	objs := make([]object, 0, len(a.Objects))
	for _, o := range a.Objects {
		objs = append(objs, objectJSON(a.Type, o))
	}
	j["objects"] = objs
	return j
}
