// SPDX-License-Identifier: MIT

package main

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"slices"
	"sort"

	"github.com/otfabric/go-iec104/asdu"
)

// The fixture of docs/FIXTURES.md. The rules mirror scripts/validate-fixtures.py.

type point struct {
	IOA       int      `json:"ioa"`
	Type      string   `json:"type"`
	RawValue  any      `json:"value"`
	Transient bool     `json:"transient"`
	Sequence  *int     `json:"sequence"`
	Quality   []string `json:"quality"`
	Groups    []int    `json:"groups"`

	value float64 // 0 or 1 for M_SP_NA_1, the raw NVA for M_ME_NA_1
}

type command struct {
	IOA            int    `json:"ioa"`
	Type           string `json:"type"`
	Target         int    `json:"target"`
	ReportCause    *int   `json:"reportCause"`
	SelectRequired bool   `json:"selectRequired"`

	target   *point
	selected bool
}

type fileSpec struct {
	IOA         int `json:"ioa"`
	Name        int `json:"name"`
	Size        int `json:"size"`
	SectionSize int `json:"sectionSize"`
}

type uploadSpec struct {
	IOA     int `json:"ioa"`
	Name    int `json:"name"`
	MaxSize int `json:"maxSize"`
}

type fixture struct {
	SchemaVersion string `json:"schemaVersion"`
	Name          string `json:"name"`
	Station       struct {
		CommonAddress int `json:"commonAddress"`
	} `json:"station"`
	Points   []*point      `json:"points"`
	Commands []*command    `json:"commands"`
	Files    []*fileSpec   `json:"files"`
	Uploads  []*uploadSpec `json:"uploads"`
}

// commandTarget is the point type each command type acts on.
var commandTarget = map[string]string{
	"C_SC_NA_1": "M_SP_NA_1", "C_DC_NA_1": "M_DP_NA_1", "C_RC_NA_1": "M_ST_NA_1",
	"C_SE_NA_1": "M_ME_NA_1", "C_SE_NB_1": "M_ME_NB_1", "C_SE_NC_1": "M_ME_NC_1",
}

func (p *point) validate() error {
	number := func(lo, hi float64) bool {
		v, ok := p.RawValue.(float64)
		p.value = v
		return ok && v == math.Trunc(v) && v >= lo && v <= hi
	}
	quality := []string{"OV", "BL", "SB", "NT", "IV"}
	valid := false
	switch p.Type {
	case "M_SP_NA_1":
		var on bool
		on, valid = p.RawValue.(bool)
		if on {
			p.value = 1
		}
		quality = quality[1:]
	case "M_DP_NA_1":
		valid = number(0, 3)
		quality = quality[1:]
	case "M_ST_NA_1":
		valid = number(-64, 63)
	case "M_BO_NA_1":
		valid = number(0, math.MaxUint32)
	case "M_ME_NA_1", "M_ME_NB_1":
		valid = number(math.MinInt16, math.MaxInt16)
	case "M_ME_NC_1":
		p.value, valid = p.RawValue.(float64)
	case "M_IT_NA_1":
		valid = number(math.MinInt32, math.MaxInt32)
		quality = []string{"CY", "CA", "IV"}
	default:
		return fmt.Errorf("point %d: unknown type %q", p.IOA, p.Type)
	}
	if !valid {
		return fmt.Errorf("point %d: the value is not valid for %s", p.IOA, p.Type)
	}
	if p.Transient && p.Type != "M_ST_NA_1" {
		return fmt.Errorf("point %d: transient is of M_ST_NA_1 only", p.IOA)
	}
	if p.Sequence != nil && (p.Type != "M_IT_NA_1" || *p.Sequence < 0 || *p.Sequence > 31) {
		return fmt.Errorf("point %d: sequence is 0..31 of M_IT_NA_1 only", p.IOA)
	}
	for _, q := range p.Quality {
		if !slices.Contains(quality, q) {
			return fmt.Errorf("point %d: quality %s is not valid for %s", p.IOA, q, p.Type)
		}
	}
	if p.Groups != nil && p.Type == "M_IT_NA_1" {
		return fmt.Errorf("point %d: an integrated total has no interrogation group", p.IOA)
	}
	for i, g := range p.Groups {
		if g < 1 || g > 16 || slices.Contains(p.Groups[:i], g) {
			return fmt.Errorf("point %d: the groups must be unique and 1..16", p.IOA)
		}
	}
	return nil
}

func loadFixture(path string) (*fixture, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	fx := &fixture{}
	if err := json.Unmarshal(raw, fx); err != nil {
		return nil, err
	}
	if fx.SchemaVersion != schemaVersion {
		return nil, fmt.Errorf("schemaVersion %q, want %s", fx.SchemaVersion, schemaVersion)
	}
	if fx.Name == "" || fx.Station.CommonAddress < 1 || fx.Station.CommonAddress > 65534 || len(fx.Points) == 0 {
		return nil, fmt.Errorf("a fixture needs a name, station.commonAddress (1..65534) and points")
	}
	seen := map[int]bool{}
	address := func(what string, ioa int) error {
		if ioa < 1 || ioa > 0xFFFFFF {
			return fmt.Errorf("%s %d: the address is not 1..16777215", what, ioa)
		}
		if seen[ioa] {
			return fmt.Errorf("%s %d: duplicate information object address", what, ioa)
		}
		seen[ioa] = true
		return nil
	}
	points := map[int]*point{}
	for _, p := range fx.Points {
		if err := address("point", p.IOA); err != nil {
			return nil, err
		}
		if err := p.validate(); err != nil {
			return nil, err
		}
		points[p.IOA] = p
	}
	sort.Slice(fx.Points, func(i, j int) bool { return fx.Points[i].IOA < fx.Points[j].IOA })
	for _, c := range fx.Commands {
		if err := address("command", c.IOA); err != nil {
			return nil, err
		}
		want, known := commandTarget[c.Type]
		if !known {
			return nil, fmt.Errorf("command %d: unknown type %q", c.IOA, c.Type)
		}
		c.target = points[c.Target]
		if c.target == nil || c.target.Type != want {
			return nil, fmt.Errorf("command %d: %s needs a %s target", c.IOA, c.Type, want)
		}
		if c.ReportCause == nil {
			cause := int(asdu.CauseReturnRemote)
			c.ReportCause = &cause
		} else if *c.ReportCause != int(asdu.CauseSpontaneous) && *c.ReportCause != int(asdu.CauseReturnRemote) {
			return nil, fmt.Errorf("command %d: reportCause must be 3 or 11", c.IOA)
		}
	}
	for _, f := range fx.Files {
		if err := address("file", f.IOA); err != nil {
			return nil, err
		}
		if f.Name < 1 || f.Name > 255 || f.Size < 1 || f.Size > 65536 || f.SectionSize < 1 || f.SectionSize > f.Size {
			return nil, fmt.Errorf("file %d: needs a name (1..255), size (1..65536) and sectionSize (1..size)", f.IOA)
		}
	}
	for _, u := range fx.Uploads {
		if err := address("upload", u.IOA); err != nil {
			return nil, err
		}
		if u.Name < 1 || u.Name > 255 || u.MaxSize < 1 || u.MaxSize > 65536 {
			return nil, fmt.Errorf("upload %d: needs a name (1..255) and maxSize (1..65536)", u.IOA)
		}
	}
	return fx, nil
}

// content is the content of a file: octet i is (i + ioa) mod 251.
func (f *fileSpec) content() []byte {
	return fileContent(f.IOA, f.Size)
}

func fileContent(ioa, size int) []byte {
	b := make([]byte, size)
	for i := range b {
		b[i] = byte((i + ioa) % 251)
	}
	return b
}

// quality is what the fixture says of the flags of a point.
func (p *point) quality() asdu.Quality {
	var q asdu.Quality
	for _, f := range p.Quality {
		switch f {
		case "OV":
			q |= asdu.QualityOverflow
		case "BL":
			q |= asdu.QualityBlocked
		case "SB":
			q |= asdu.QualitySubstituted
		case "NT":
			q |= asdu.QualityNotTopical
		case "IV":
			q |= asdu.QualityInvalid
		}
	}
	return q
}

// object is the information object of the point; a tag makes it the CP56Time2a
// type.
func (p *point) object(tag asdu.Timestamp) asdu.InformationObject {
	ioa, q := asdu.IOA(p.IOA), p.quality()
	switch p.Type {
	case "M_SP_NA_1":
		return asdu.SinglePoint{IOA: ioa, Value: p.value != 0, Quality: q, Time: tag}
	case "M_DP_NA_1":
		return asdu.DoublePoint{IOA: ioa, Value: asdu.DoubleState(p.value), Quality: q, Time: tag}
	case "M_ST_NA_1":
		return asdu.StepPosition{IOA: ioa, Value: int8(p.value), Transient: p.Transient, Quality: q, Time: tag}
	case "M_BO_NA_1":
		return asdu.Bitstring32{IOA: ioa, Value: uint32(p.value), Quality: q, Time: tag}
	case "M_ME_NA_1":
		return asdu.MeasuredNormalized{IOA: ioa, Value: asdu.Normalized(p.value), Quality: q, Time: tag}
	case "M_ME_NB_1":
		return asdu.MeasuredScaled{IOA: ioa, Value: int16(p.value), Quality: q, Time: tag}
	case "M_ME_NC_1":
		return asdu.MeasuredFloat{IOA: ioa, Value: float32(p.value), Quality: q, Time: tag}
	}
	it := asdu.IntegratedTotal{IOA: ioa, Value: int32(p.value), Time: tag,
		Carry: slices.Contains(p.Quality, "CY"), Adjusted: slices.Contains(p.Quality, "CA"), Invalid: slices.Contains(p.Quality, "IV")}
	if p.Sequence != nil {
		it.Sequence = uint8(*p.Sequence)
	}
	return it
}
