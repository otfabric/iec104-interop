// SPDX-License-Identifier: MIT

package main

import (
	"encoding/hex"
	"time"

	"github.com/otfabric/go-iec104/asdu"
)

// Conversions to the ASDU documents of the contract.

const timeLayout = "2006-01-02T15:04:05.000Z"

func formatTime(t time.Time) string { return t.UTC().Format(timeLayout) }

// modelled are the types of the contract; any other is reported with "type": null.
var modelled = map[asdu.TypeID]bool{}

func init() {
	for _, t := range []asdu.TypeID{
		asdu.M_SP_NA_1, asdu.M_DP_NA_1, asdu.M_ST_NA_1, asdu.M_BO_NA_1, asdu.M_ME_NA_1, asdu.M_ME_NB_1, asdu.M_ME_NC_1, asdu.M_IT_NA_1,
		asdu.M_SP_TB_1, asdu.M_DP_TB_1, asdu.M_ST_TB_1, asdu.M_BO_TB_1, asdu.M_ME_TD_1, asdu.M_ME_TE_1, asdu.M_ME_TF_1, asdu.M_IT_TB_1,
		asdu.C_SC_NA_1, asdu.C_DC_NA_1, asdu.C_RC_NA_1, asdu.C_SE_NA_1, asdu.C_SE_NB_1, asdu.C_SE_NC_1,
		asdu.C_SC_TA_1, asdu.C_DC_TA_1, asdu.C_RC_TA_1, asdu.C_SE_TA_1, asdu.C_SE_TB_1, asdu.C_SE_TC_1,
		asdu.M_EI_NA_1, asdu.C_IC_NA_1, asdu.C_CI_NA_1, asdu.C_RD_NA_1, asdu.C_CS_NA_1, asdu.C_TS_TA_1,
		asdu.F_FR_NA_1, asdu.F_SR_NA_1, asdu.F_SC_NA_1, asdu.F_LS_NA_1, asdu.F_AF_NA_1, asdu.F_SG_NA_1,
	} {
		modelled[t] = true
	}
}

func qualityJSON(q asdu.Quality) []string {
	out := []string{}
	for _, f := range []struct {
		bit  asdu.Quality
		name string
	}{{asdu.QualityOverflow, "OV"}, {asdu.QualityBlocked, "BL"}, {asdu.QualitySubstituted, "SB"},
		{asdu.QualityNotTopical, "NT"}, {asdu.QualityInvalid, "IV"}} {
		if q&f.bit != 0 {
			out = append(out, f.name)
		}
	}
	return out
}

func totalQualityJSON(o asdu.IntegratedTotal) []string {
	out := []string{}
	for _, f := range []struct {
		set  bool
		name string
	}{{o.Carry, "CY"}, {o.Adjusted, "CA"}, {o.Invalid, "IV"}} {
		if f.set {
			out = append(out, f.name)
		}
	}
	return out
}

// objectJSON is the contract's form of one information object of type t.
func objectJSON(t asdu.TypeID, i asdu.InformationObject) object {
	o := object{"ioa": int(i.Address())}
	stamp := func(ts asdu.Timestamp) {
		if t.HasTimeTag() {
			o["time"] = formatTime(ts.Time)
			if ts.Invalid {
				o["timeInvalid"] = true
			}
		}
	}
	command := func(sel bool, qualifier uint8, ts asdu.Timestamp) {
		o["select"], o["qualifier"] = sel, int(qualifier)
		stamp(ts)
	}
	raw := func(v asdu.Normalized) {
		o["value"], o["normalized"] = int(v), float64(v)/32768
	}
	switch v := i.(type) {
	case asdu.SinglePoint:
		o["value"], o["quality"] = v.Value, qualityJSON(v.Quality)
		stamp(v.Time)
	case asdu.DoublePoint:
		o["value"], o["quality"] = int(v.Value), qualityJSON(v.Quality)
		stamp(v.Time)
	case asdu.StepPosition:
		o["value"], o["transient"], o["quality"] = int(v.Value), v.Transient, qualityJSON(v.Quality)
		stamp(v.Time)
	case asdu.Bitstring32:
		o["value"], o["quality"] = v.Value, qualityJSON(v.Quality)
		stamp(v.Time)
	case asdu.MeasuredNormalized:
		raw(v.Value)
		o["quality"] = qualityJSON(v.Quality)
		stamp(v.Time)
	case asdu.MeasuredScaled:
		o["value"], o["quality"] = int(v.Value), qualityJSON(v.Quality)
		stamp(v.Time)
	case asdu.MeasuredFloat:
		o["value"], o["quality"] = float64(v.Value), qualityJSON(v.Quality)
		stamp(v.Time)
	case asdu.IntegratedTotal:
		o["value"], o["sequence"], o["quality"] = int(v.Value), int(v.Sequence), totalQualityJSON(v)
		stamp(v.Time)
	case asdu.SingleCommand:
		o["value"] = v.Value
		command(v.Select, uint8(v.Qualifier), v.Time)
	case asdu.DoubleCommand:
		o["value"] = int(v.Value)
		command(v.Select, uint8(v.Qualifier), v.Time)
	case asdu.StepCommand:
		o["value"] = int(v.Value)
		command(v.Select, uint8(v.Qualifier), v.Time)
	case asdu.SetpointNormalized:
		raw(v.Value)
		command(v.Select, v.Qualifier, v.Time)
	case asdu.SetpointScaled:
		o["value"] = int(v.Value)
		command(v.Select, v.Qualifier, v.Time)
	case asdu.SetpointFloat:
		o["value"] = float64(v.Value)
		command(v.Select, v.Qualifier, v.Time)
	case asdu.EndOfInitialization:
		coi := int(v.Cause)
		if v.LocalChange {
			coi |= 0x80
		}
		o["coi"] = coi
	case asdu.Interrogation:
		o["qoi"] = int(v.Qualifier)
	case asdu.CounterInterrogation:
		o["qcc"] = int(v.Request) | int(v.Freeze)<<6
	case asdu.ClockSync:
		stamp(v.Time)
	case asdu.TestCommand:
		o["counter"] = int(v.Counter)
		stamp(v.Time)
	case asdu.FileReady:
		o["nof"], o["lof"], o["frq"] = int(v.Name), int(v.Length), int(v.Qualifier)
	case asdu.SectionReady:
		o["nof"], o["nos"], o["lof"], o["srq"] = int(v.Name), int(v.Section), int(v.Length), int(v.Qualifier)
	case asdu.FileCall:
		o["nof"], o["nos"], o["scq"] = int(v.Name), int(v.Section), int(v.Qualifier)
	case asdu.FileLastSegment:
		o["nof"], o["nos"], o["lsq"], o["chs"] = int(v.Name), int(v.Section), int(v.Qualifier), int(v.Checksum)
	case asdu.FileAck:
		o["nof"], o["nos"], o["afq"] = int(v.Name), int(v.Section), int(v.Qualifier)
	case asdu.FileSegment:
		o["nof"], o["nos"], o["data"] = int(v.Name), int(v.Section), hex.EncodeToString(v.Data)
	}
	return o
}

// asduJSON is the ASDU document of the contract.
func asduJSON(a *asdu.ASDU) object {
	j := object{"type": nil, "typeId": int(a.Type), "cot": int(a.Cause), "negative": a.Negative, "test": a.Test,
		"originator": int(a.Originator), "commonAddress": int(a.CommonAddr), "sequence": a.Sequence,
		"count": a.Len(), "objects": []object{}}
	if !modelled[a.Type] {
		return j
	}
	j["type"] = a.Type.String()
	objects := make([]object, 0, len(a.Objects))
	for _, o := range a.Objects {
		objects = append(objects, objectJSON(a.Type, o))
	}
	j["objects"] = objects
	return j
}
