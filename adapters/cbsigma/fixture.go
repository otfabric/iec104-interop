// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"

	iec "github.com/cB-Sigma/iec60870"
)

// The simulated station: see docs/FIXTURES.md. The rules mirror
// scripts/validate-fixtures.py.

type point struct {
	IOA       int      `json:"ioa"`
	Type      string   `json:"type"`
	RawValue  any      `json:"value"`
	Transient *bool    `json:"transient"`
	Sequence  *int     `json:"sequence"`
	Quality   []string `json:"quality"`

	typ   iec.TypeID
	value float64 // raw NVA for M_ME_NA_1; 0 or 1 for M_SP_NA_1
}

type command struct {
	IOA            int    `json:"ioa"`
	Type           string `json:"type"`
	Target         int    `json:"target"`
	ReportCause    *int   `json:"reportCause"`
	SelectRequired bool   `json:"selectRequired"`

	typ      iec.TypeID
	target   *point
	selected bool
}

type fileSpec struct {
	IOA         int `json:"ioa"`
	Name        int `json:"name"`
	Size        int `json:"size"`
	SectionSize int `json:"sectionSize"`
}

type fixture struct {
	SchemaVersion string `json:"schemaVersion"`
	Name          string `json:"name"`
	Station       struct {
		CommonAddr int `json:"commonAddress"`
	} `json:"station"`
	Points   []*point    `json:"points"`
	Commands []*command  `json:"commands"`
	Files    []*fileSpec `json:"files"`
}

var pointTypes = map[string]iec.TypeID{
	"M_SP_NA_1": iec.MSpNa1, "M_DP_NA_1": iec.MDpNa1, "M_ST_NA_1": iec.MStNa1, "M_BO_NA_1": iec.MBoNa1,
	"M_ME_NA_1": iec.MMeNa1, "M_ME_NB_1": iec.MMeNb1, "M_ME_NC_1": iec.MMeNc1, "M_IT_NA_1": iec.MItNa1,
}

var commandTypes = map[string]iec.TypeID{
	"C_SC_NA_1": iec.CScNa1, "C_DC_NA_1": iec.CDcNa1, "C_RC_NA_1": iec.CRcNa1,
	"C_SE_NA_1": iec.CSeNa1, "C_SE_NB_1": iec.CSeNb1, "C_SE_NC_1": iec.CSeNc1,
}

var commandTarget = map[iec.TypeID]iec.TypeID{
	iec.CScNa1: iec.MSpNa1, iec.CDcNa1: iec.MDpNa1, iec.CRcNa1: iec.MStNa1,
	iec.CSeNa1: iec.MMeNa1, iec.CSeNb1: iec.MMeNb1, iec.CSeNc1: iec.MMeNc1,
}

func (fx *fixture) point(ioa int) *point {
	for _, p := range fx.Points {
		if p.IOA == ioa {
			return p
		}
	}
	return nil
}

func (fx *fixture) file(ioa int) *fileSpec {
	for _, f := range fx.Files {
		if f.IOA == ioa {
			return f
		}
	}
	return nil
}

// content is the content of a file: octet i is (i + ioa) mod 251.
func (f *fileSpec) content() []byte {
	b := make([]byte, f.Size)
	for i := range b {
		b[i] = byte((i + f.IOA) % 251)
	}
	return b
}

func (fx *fixture) command(ioa int) *command {
	for _, c := range fx.Commands {
		if c.IOA == ioa {
			return c
		}
	}
	return nil
}

func integer(v any, lo, hi float64) (float64, bool) {
	f, ok := v.(float64)
	return f, ok && f == math.Trunc(f) && f >= lo && f <= hi
}

func allowedQuality(t iec.TypeID) string {
	switch t {
	case iec.MSpNa1, iec.MDpNa1:
		return "BL SB NT IV"
	case iec.MBoNa1:
		return "" // kept empty for every adapter: lib60870 cannot set it
	case iec.MItNa1:
		return "CY CA IV"
	default:
		return "OV BL SB NT IV"
	}
}

func loadFixture(path string) (*fixture, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("cannot read fixture %s", path)
	}
	fx := &fixture{}
	if err := json.Unmarshal(raw, fx); err != nil {
		return nil, fmt.Errorf("fixture is not valid JSON: %v", err)
	}
	if fx.SchemaVersion != schemaVersion {
		return nil, fmt.Errorf("unsupported schemaVersion, want %s", schemaVersion)
	}
	if fx.Name == "" || fx.Station.CommonAddr < 1 || fx.Station.CommonAddr > 65534 || len(fx.Points) == 0 {
		return nil, fmt.Errorf("fixture needs name, station.commonAddress (1..65534) and points")
	}
	seen := map[int]bool{}
	for _, p := range fx.Points {
		var ok bool
		if p.typ, ok = pointTypes[p.Type]; !ok || p.IOA < 1 || p.IOA > 0xFFFFFF {
			return nil, fmt.Errorf("point %d: needs ioa (1..16777215) and a supported type, got %q", p.IOA, p.Type)
		}
		if seen[p.IOA] {
			return nil, fmt.Errorf("point %d: duplicate information object address", p.IOA)
		}
		seen[p.IOA] = true
		valid := false
		switch p.typ {
		case iec.MSpNa1:
			var b bool
			if b, valid = p.RawValue.(bool); b {
				p.value = 1
			}
		case iec.MDpNa1:
			p.value, valid = integer(p.RawValue, 0, 3)
		case iec.MStNa1:
			p.value, valid = integer(p.RawValue, -64, 63)
		case iec.MBoNa1:
			p.value, valid = integer(p.RawValue, 0, 4294967295)
		case iec.MMeNa1, iec.MMeNb1:
			p.value, valid = integer(p.RawValue, -32768, 32767)
		case iec.MMeNc1:
			p.value, valid = p.RawValue.(float64)
		case iec.MItNa1:
			p.value, valid = integer(p.RawValue, -2147483648, 2147483647)
		}
		if !valid {
			return nil, fmt.Errorf("point %d: value is not valid for %s", p.IOA, p.Type)
		}
		if p.Transient != nil && p.typ != iec.MStNa1 {
			return nil, fmt.Errorf("point %d: transient is a boolean of M_ST_NA_1 only", p.IOA)
		}
		if p.Sequence != nil && (p.typ != iec.MItNa1 || *p.Sequence < 0 || *p.Sequence > 31) {
			return nil, fmt.Errorf("point %d: sequence is 0..31 of M_IT_NA_1 only", p.IOA)
		}
		allowed := " " + allowedQuality(p.typ) + " "
		for _, q := range p.Quality {
			if len(q) != 2 || !containsWord(allowed, q) {
				return nil, fmt.Errorf("point %d: quality is not valid for %s", p.IOA, p.Type)
			}
		}
	}
	sort.Slice(fx.Points, func(i, j int) bool { return fx.Points[i].IOA < fx.Points[j].IOA })

	for _, c := range fx.Commands {
		var ok bool
		if c.typ, ok = commandTypes[c.Type]; !ok || c.IOA < 1 || c.IOA > 0xFFFFFF {
			return nil, fmt.Errorf("command %d: needs ioa, a supported type and target, got %q", c.IOA, c.Type)
		}
		if seen[c.IOA] {
			return nil, fmt.Errorf("command %d: duplicate information object address", c.IOA)
		}
		seen[c.IOA] = true
		c.target = fx.point(c.Target)
		if c.target == nil || c.target.typ != commandTarget[c.typ] {
			return nil, fmt.Errorf("command %d: target must be a point of the matching type", c.IOA)
		}
		if c.ReportCause == nil {
			cause := 11
			c.ReportCause = &cause
		} else if *c.ReportCause != 3 && *c.ReportCause != 11 {
			return nil, fmt.Errorf("command %d: reportCause must be 3 or 11", c.IOA)
		}
	}
	for i, f := range fx.Files {
		if f.IOA < 1 || f.IOA > 0xFFFFFF || f.Name < 1 || f.Name > 255 || f.Size < 1 || f.Size > 65536 ||
			f.SectionSize < 1 || f.SectionSize > f.Size {
			return nil, fmt.Errorf("file %d: needs ioa, name (1..255), size (1..65536) and sectionSize (1..size)", i)
		}
		if seen[f.IOA] {
			return nil, fmt.Errorf("file %d: duplicate information object address", f.IOA)
		}
		seen[f.IOA] = true
	}
	return fx, nil
}

func containsWord(spaced, w string) bool {
	for i := 0; i+len(w)+2 <= len(spaced); i++ {
		if spaced[i] == ' ' && spaced[i+1:i+1+len(w)] == w && spaced[i+1+len(w)] == ' ' {
			return true
		}
	}
	return false
}
