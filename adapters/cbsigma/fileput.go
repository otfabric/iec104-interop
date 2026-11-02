// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	iec "github.com/cB-Sigma/iec60870"
)

// maxSegment is the most octets of a segment, as the contract says.
const maxSegment = 236

// filePut is the file-put operation. iec60870.PutFile sends sections of the
// library's fixed SectionSize, and the contract has --section-size, so the
// procedure is sent here through the same public Peer and Taps. A refusal ends
// it, without the deactivation PutFile would send.
type filePut struct {
	ioa, name, sectionSize int
	data                   []byte

	mu       sync.Mutex
	sent     int // octets of the sections called
	sections int // sections called
}

func validatePut(ioa, name, size, section int) error {
	if ioa < 1 || ioa > 0xffffff || name < 1 || name > 65535 {
		return usagef("file-put: --ioa is required; --name is 1..65535")
	}
	if size < 1 || size > 65536 || section < 1 || section > size {
		return usagef("file-put: --size is required and 1..65536; --section-size is 1..--size")
	}
	// NOS is one octet.
	if (size+section-1)/section > 255 {
		return usagef("file-put: more than 255 sections cannot be named")
	}
	return nil
}

func newFilePut(ioa, name, size, section int) *filePut {
	f := &fileSpec{IOA: ioa, Size: size}
	return &filePut{ioa: ioa, name: name, sectionSize: section, data: f.content()}
}

func (f *filePut) result() object {
	f.mu.Lock()
	defer f.mu.Unlock()
	sum := sha256.Sum256(f.data[:f.sent])
	return object{"ioa": f.ioa, "name": f.name, "length": len(f.data), "received": f.sent,
		"sections": f.sections, "sha256": hex.EncodeToString(sum[:])}
}

func (f *filePut) run(ctx context.Context, p iec.Peer, timeout time.Duration) error {
	ch, stop := p.Taps().Add(func(a *iec.ASDU) bool {
		return a.Type.File() && a.Type != iec.FDrTa1 && len(a.Objects) > 0 && a.Objects[0].IOA == uint32(f.ioa)
	})
	defer stop()
	send := func(t iec.TypeID, o iec.Object) error {
		o.IOA, o.File = uint32(f.ioa), uint16(f.name)
		return p.Send(ctx, &iec.ASDU{Type: t, Cause: iec.CauseFile, Objects: []iec.Object{o}})
	}
	// await returns the next answer of the type, and fails on a refusal.
	await := func(what string, want ...iec.TypeID) (*iec.ASDU, error) {
		t := time.NewTimer(timeout)
		defer t.Stop()
		for {
			var a *iec.ASDU
			select {
			case a = <-ch:
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-t.C:
				return nil, fmt.Errorf("%w: no %s within %s", iec.ErrNoAnswer, what, timeout)
			}
			q := a.Objects[0].Qualifier
			switch {
			case a.Cause >= iec.CauseUnknownType && a.Cause <= iec.CauseUnknownObject, a.Negative,
				a.Type == iec.FAfNa1 && (q&0x0f == iec.AFQFileNegative || q&0x0f == iec.AFQSectionNegative):
				return nil, failf("negative-confirmation", "the station refused %s (cause %d, qualifier %d)", what, a.Cause, q)
			}
			for _, w := range want {
				if a.Type == w {
					return a, nil
				}
			}
			if a.Type == iec.FLsNa1 && (q == iec.LSQFileDeact || q == iec.LSQSectionDeact) {
				return nil, failf("failed", "the station ended the file transfer (deactivation)")
			}
		}
	}

	if err := send(iec.FFrNa1, iec.Object{Length: uint32(len(f.data))}); err != nil {
		return err
	}
	a, err := await("call of the file", iec.FScNa1)
	if err != nil {
		return err
	}
	if a.Objects[0].Qualifier&0x0f != iec.SCQRequestFile {
		return failf("failed", "the station did not call the file (qualifier %d)", a.Objects[0].Qualifier)
	}
	nos := uint8(0)
	for off := 0; off < len(f.data); off += f.sectionSize {
		nos++
		sec := f.data[off:min(len(f.data), off+f.sectionSize)]
		if err := send(iec.FSrNa1, iec.Object{Section: nos, Length: uint32(len(sec))}); err != nil {
			return err
		}
		a, err := await("call of the section", iec.FScNa1)
		if err != nil {
			return err
		}
		if o := a.Objects[0]; o.Qualifier&0x0f != iec.SCQRequestSection || o.Section != nos {
			return failf("failed", "the station did not call section %d (section %d, qualifier %d)", nos, o.Section, o.Qualifier)
		}
		f.mu.Lock()
		f.sections++
		f.sent += len(sec)
		f.mu.Unlock()
		for i := 0; i < len(sec); i += maxSegment {
			if err := send(iec.FSgNa1, iec.Object{Section: nos, Segment: sec[i:min(len(sec), i+maxSegment)]}); err != nil {
				return err
			}
		}
		if err := send(iec.FLsNa1, iec.Object{Section: nos, Qualifier: iec.LSQSection, Checksum: iec.Checksum(sec)}); err != nil {
			return err
		}
		a, err = await("acknowledgement of the section", iec.FAfNa1)
		if err != nil {
			return err
		}
		if q := a.Objects[0].Qualifier; q&0x0f != iec.AFQSectionOK {
			return failf("failed", "section %d was answered with AFQ %d", nos, q)
		}
	}
	if err := send(iec.FLsNa1, iec.Object{Section: nos, Qualifier: iec.LSQFile, Checksum: iec.Checksum(f.data)}); err != nil {
		return err
	}
	a, err = await("acknowledgement of the file", iec.FAfNa1)
	if err != nil {
		return err
	}
	if q := a.Objects[0].Qualifier; q&0x0f != iec.AFQFileOK {
		return failf("failed", "the file was answered with AFQ %d", q)
	}
	return nil
}
