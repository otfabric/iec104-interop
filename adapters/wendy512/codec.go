// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"time"

	"github.com/wendy512/go-iecp5/asdu"
)

// Conversions between the fixture model, go-iecp5 ASDUs and the JSON of the
// contract.

// typeNames are the types this adapter models, by the names of the contract.
// The time-tagged commands (C_SC_TA_1 ...) and the file segment are missing on
// purpose: go-iecp5 has no size for them, so it drops them on reception and
// they never get here.
var typeNames = map[asdu.TypeID]string{
	asdu.M_SP_NA_1: "M_SP_NA_1", asdu.M_DP_NA_1: "M_DP_NA_1", asdu.M_ST_NA_1: "M_ST_NA_1", asdu.M_BO_NA_1: "M_BO_NA_1",
	asdu.M_ME_NA_1: "M_ME_NA_1", asdu.M_ME_NB_1: "M_ME_NB_1", asdu.M_ME_NC_1: "M_ME_NC_1", asdu.M_IT_NA_1: "M_IT_NA_1",
	asdu.M_SP_TB_1: "M_SP_TB_1", asdu.M_DP_TB_1: "M_DP_TB_1", asdu.M_ST_TB_1: "M_ST_TB_1", asdu.M_BO_TB_1: "M_BO_TB_1",
	asdu.M_ME_TD_1: "M_ME_TD_1", asdu.M_ME_TE_1: "M_ME_TE_1", asdu.M_ME_TF_1: "M_ME_TF_1", asdu.M_IT_TB_1: "M_IT_TB_1",
	asdu.C_SC_NA_1: "C_SC_NA_1", asdu.C_DC_NA_1: "C_DC_NA_1", asdu.C_RC_NA_1: "C_RC_NA_1",
	asdu.C_SE_NA_1: "C_SE_NA_1", asdu.C_SE_NB_1: "C_SE_NB_1", asdu.C_SE_NC_1: "C_SE_NC_1",
	asdu.M_EI_NA_1: "M_EI_NA_1", asdu.C_IC_NA_1: "C_IC_NA_1", asdu.C_CI_NA_1: "C_CI_NA_1",
	asdu.C_RD_NA_1: "C_RD_NA_1", asdu.C_CS_NA_1: "C_CS_NA_1", asdu.C_TS_TA_1: "C_TS_TA_1",
}

// timeTagged maps a point type to its CP56Time2a variant.
var timeTagged = map[asdu.TypeID]asdu.TypeID{
	asdu.M_SP_NA_1: asdu.M_SP_TB_1, asdu.M_DP_NA_1: asdu.M_DP_TB_1, asdu.M_ST_NA_1: asdu.M_ST_TB_1,
	asdu.M_BO_NA_1: asdu.M_BO_TB_1, asdu.M_ME_NA_1: asdu.M_ME_TD_1, asdu.M_ME_NB_1: asdu.M_ME_TE_1,
	asdu.M_ME_NC_1: asdu.M_ME_TF_1, asdu.M_IT_NA_1: asdu.M_IT_TB_1,
}

// timeTaggedCommand maps a command type to its CP56Time2a variant.
var timeTaggedCommand = map[asdu.TypeID]asdu.TypeID{
	asdu.C_SC_NA_1: asdu.C_SC_TA_1, asdu.C_DC_NA_1: asdu.C_DC_TA_1, asdu.C_RC_NA_1: asdu.C_RC_TA_1,
	asdu.C_SE_NA_1: asdu.C_SE_TA_1, asdu.C_SE_NB_1: asdu.C_SE_TB_1, asdu.C_SE_NC_1: asdu.C_SE_TC_1,
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

func qualityByte(quality []string) byte {
	var q asdu.QualityDescriptor
	for _, f := range quality {
		switch f {
		case "OV":
			q |= asdu.QDSOverflow
		case "BL":
			q |= asdu.QDSBlocked
		case "SB":
			q |= asdu.QDSSubstituted
		case "NT":
			q |= asdu.QDSNotTopical
		case "IV":
			q |= asdu.QDSInvalid
		}
	}
	return byte(q)
}

// appendPoint appends the information object of a point to a. The go-iecp5
// helpers (asdu.Single ...) are not used: they always send originator 0 and
// refuse an integrated total with cause 5, both of which the station needs.
func appendPoint(a *asdu.ASDU, p *point, timed bool, now time.Time) error {
	if err := a.AppendInfoObjAddr(asdu.InfoObjAddr(p.IOA)); err != nil {
		return err
	}
	q := qualityByte(p.Quality)
	switch p.typ {
	case asdu.M_SP_NA_1:
		a.AppendBytes(byte(int(p.value))&0x01 | q&0xf0)
	case asdu.M_DP_NA_1:
		a.AppendBytes(byte(int(p.value))&0x03 | q&0xf0)
	case asdu.M_ST_NA_1:
		a.AppendBytes(asdu.StepPosition{Val: int(p.value), HasTransient: p.Transient != nil && *p.Transient}.Value(), q)
	case asdu.M_BO_NA_1:
		a.AppendBitsString32(uint32(p.value)).AppendBytes(q)
	case asdu.M_ME_NA_1:
		a.AppendNormalize(asdu.Normalize(int16(p.value))).AppendBytes(q)
	case asdu.M_ME_NB_1:
		a.AppendScaled(int16(p.value)).AppendBytes(q)
	case asdu.M_ME_NC_1:
		a.AppendFloat32(float32(p.value)).AppendBytes(q)
	case asdu.M_IT_NA_1:
		seq := 0
		if p.Sequence != nil {
			seq = *p.Sequence
		}
		a.AppendBinaryCounterReading(asdu.BinaryCounterReading{
			CounterReading: int32(p.value), SeqNumber: byte(seq),
			HasCarry: hasFlag(p.Quality, "CY"), IsAdjusted: hasFlag(p.Quality, "CA"), IsInvalid: hasFlag(p.Quality, "IV"),
		})
	}
	if timed {
		a.AppendCP56Time2a(now, time.UTC)
	}
	return nil
}

func qualityJSON(q asdu.QualityDescriptor, overflow bool) []string {
	out := []string{}
	if overflow && q&asdu.QDSOverflow != 0 {
		out = append(out, "OV")
	}
	if q&asdu.QDSBlocked != 0 {
		out = append(out, "BL")
	}
	if q&asdu.QDSSubstituted != 0 {
		out = append(out, "SB")
	}
	if q&asdu.QDSNotTopical != 0 {
		out = append(out, "NT")
	}
	if q&asdu.QDSInvalid != 0 {
		out = append(out, "IV")
	}
	return out
}

type object = map[string]any

// addTime adds the time tag of a CP56Time2a type. go-iecp5 decodes a time tag
// with the IV bit to the zero time and keeps nothing else of it.
func addTime(o object, typ asdu.TypeID, t time.Time) {
	switch typ {
	case asdu.M_SP_TB_1, asdu.M_DP_TB_1, asdu.M_ST_TB_1, asdu.M_BO_TB_1, asdu.M_ME_TD_1, asdu.M_ME_TE_1,
		asdu.M_ME_TF_1, asdu.M_IT_TB_1, asdu.C_CS_NA_1, asdu.C_TS_TA_1:
		o["time"] = formatTime(t)
		if t.IsZero() {
			o["timeInvalid"] = true
		}
	}
}

func normalized(o object, v asdu.Normalize) {
	o["value"] = int(v)
	o["normalized"] = float64(v) / 32768
}

// objectsJSON decodes the information objects of a. The go-iecp5 getters
// consume the ASDU, so the caller passes a clone.
func objectsJSON(a *asdu.ASDU) (objects []object) {
	objects = []object{}
	// The getters index without checking and panic on a short ASDU.
	defer func() {
		if r := recover(); r != nil {
			logf("cannot decode %s: %v", a.Type, r)
		}
	}()
	typ := a.Type
	add := func(ioa asdu.InfoObjAddr, t time.Time) object {
		o := object{"ioa": int(ioa)}
		addTime(o, typ, t)
		objects = append(objects, o)
		return o
	}
	switch typ {
	case asdu.M_SP_NA_1, asdu.M_SP_TB_1:
		for _, i := range a.GetSinglePoint() {
			o := add(i.Ioa, i.Time)
			o["value"] = i.Value
			o["quality"] = qualityJSON(i.Qds, false)
		}
	case asdu.M_DP_NA_1, asdu.M_DP_TB_1:
		for _, i := range a.GetDoublePoint() {
			o := add(i.Ioa, i.Time)
			o["value"] = int(i.Value)
			o["quality"] = qualityJSON(i.Qds, false)
		}
	case asdu.M_ST_NA_1, asdu.M_ST_TB_1:
		for _, i := range a.GetStepPosition() {
			o := add(i.Ioa, i.Time)
			o["value"] = i.Value.Val
			o["transient"] = i.Value.HasTransient
			o["quality"] = qualityJSON(i.Qds, true)
		}
	case asdu.M_BO_NA_1, asdu.M_BO_TB_1:
		for _, i := range a.GetBitString32() {
			o := add(i.Ioa, i.Time)
			o["value"] = i.Value
			o["quality"] = qualityJSON(i.Qds, true)
		}
	case asdu.M_ME_NA_1, asdu.M_ME_TD_1:
		for _, i := range a.GetMeasuredValueNormal() {
			o := add(i.Ioa, i.Time)
			normalized(o, i.Value)
			o["quality"] = qualityJSON(i.Qds, true)
		}
	case asdu.M_ME_NB_1, asdu.M_ME_TE_1:
		for _, i := range a.GetMeasuredValueScaled() {
			o := add(i.Ioa, i.Time)
			o["value"] = int(i.Value)
			o["quality"] = qualityJSON(i.Qds, true)
		}
	case asdu.M_ME_NC_1, asdu.M_ME_TF_1:
		for _, i := range a.GetMeasuredValueFloat() {
			o := add(i.Ioa, i.Time)
			o["value"] = float64(i.Value)
			o["quality"] = qualityJSON(i.Qds, true)
		}
	case asdu.M_IT_NA_1, asdu.M_IT_TB_1:
		for _, i := range a.GetIntegratedTotals() {
			o := add(i.Ioa, i.Time)
			o["value"] = int(i.Value.CounterReading)
			o["sequence"] = int(i.Value.SeqNumber)
			q := []string{}
			if i.Value.HasCarry {
				q = append(q, "CY")
			}
			if i.Value.IsAdjusted {
				q = append(q, "CA")
			}
			if i.Value.IsInvalid {
				q = append(q, "IV")
			}
			o["quality"] = q
		}
	case asdu.C_SC_NA_1:
		i := a.GetSingleCmd()
		o := add(i.Ioa, i.Time)
		o["value"], o["select"], o["qualifier"] = i.Value, i.Qoc.InSelect, int(i.Qoc.Qual)
	case asdu.C_DC_NA_1:
		i := a.GetDoubleCmd()
		o := add(i.Ioa, i.Time)
		o["value"], o["select"], o["qualifier"] = int(i.Value), i.Qoc.InSelect, int(i.Qoc.Qual)
	case asdu.C_RC_NA_1:
		i := a.GetStepCmd()
		o := add(i.Ioa, i.Time)
		o["value"], o["select"], o["qualifier"] = int(i.Value), i.Qoc.InSelect, int(i.Qoc.Qual)
	case asdu.C_SE_NA_1:
		i := a.GetSetpointNormalCmd()
		o := add(i.Ioa, i.Time)
		normalized(o, i.Value)
		o["select"], o["qualifier"] = i.Qos.InSelect, int(i.Qos.Qual)
	case asdu.C_SE_NB_1:
		i := a.GetSetpointCmdScaled()
		o := add(i.Ioa, i.Time)
		o["value"], o["select"], o["qualifier"] = int(i.Value), i.Qos.InSelect, int(i.Qos.Qual)
	case asdu.C_SE_NC_1:
		i := a.GetSetpointFloatCmd()
		o := add(i.Ioa, i.Time)
		o["value"], o["select"], o["qualifier"] = float64(i.Value), i.Qos.InSelect, int(i.Qos.Qual)
	case asdu.M_EI_NA_1:
		ioa, coi := a.GetEndOfInitialization()
		add(ioa, time.Time{})["coi"] = int(coi.Value())
	case asdu.C_IC_NA_1:
		ioa, qoi := a.GetInterrogationCmd()
		add(ioa, time.Time{})["qoi"] = int(qoi)
	case asdu.C_CI_NA_1:
		ioa, qcc := a.GetCounterInterrogationCmd()
		add(ioa, time.Time{})["qcc"] = int(qcc.Value())
	case asdu.C_RD_NA_1:
		add(a.GetReadCmd(), time.Time{})
	case asdu.C_CS_NA_1:
		ioa, t := a.GetClockSynchronizationCmd()
		add(ioa, t)
	case asdu.C_TS_TA_1:
		// GetTestCommandCP56Time2a only says whether the counter is 0x55AA.
		ioa := a.DecodeInfoObjAddr()
		counter := a.DecodeUint16()
		add(ioa, a.DecodeCP56Time2a())["counter"] = int(counter)
	}
	return objects
}

// asduJSON is the ASDU document of the contract. It leaves a untouched.
func asduJSON(a *asdu.ASDU) object {
	j := object{
		"type": nil, "typeId": int(a.Type),
		"cot": int(a.Coa.Cause), "negative": a.Coa.IsNegative, "test": a.Coa.IsTest,
		"originator": int(a.OrigAddr), "commonAddress": int(a.CommonAddr),
		"sequence": a.Variable.IsSequence, "count": int(a.Variable.Number),
		"objects": []object{},
	}
	if name, ok := typeNames[a.Type]; ok {
		j["type"] = name
		j["objects"] = objectsJSON(a.Clone())
	}
	return j
}
