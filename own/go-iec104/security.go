// SPDX-License-Identifier: MIT

package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/otfabric/go-iec104/asdu"
	"github.com/otfabric/go-iec104/sec"
)

// security is the A-Profile of IEC 62351-5 (IEC TS 60870-5-7) as the security
// flags of the contract configure it; nil without --secure.
type security struct {
	identity *sec.Identity
	trust    sec.Trust
	id       uint16
	mac, dpa uint8            // controlling station
	roles    []sec.AccessRole // controlled station
}

// security reads the flags common to server and client. Every security flag
// without --secure is a usage error, so that a run never silently goes without
// the security it asked for.
func (f *flags) security() *security {
	on := f.has("secure")
	certFile, hasCert := f.take("security-cert")
	keyFile, hasKey := f.take("security-key")
	trustFile, hasTrust := f.take("security-trust")
	_, hasID := f.values["association-id"]
	id := f.integer("association-id", 1, 1, 65535)
	if !on {
		if hasCert || hasKey || hasTrust || hasID {
			f.fail("the security flags need --secure")
		}
		return nil
	}
	if !hasCert || !hasKey || !hasTrust {
		f.fail("--secure needs --security-cert, --security-key and --security-trust")
		return nil
	}
	s := &security{id: uint16(id)}
	var err error
	if s.identity, err = sec.LoadIdentity(certFile, keyFile); err != nil {
		f.fail("--security-cert, --security-key: %v", err)
		return nil
	}
	raw, err := os.ReadFile(trustFile)
	if err != nil {
		f.fail("--security-trust: %v", err)
		return nil
	}
	certs, err := sec.ParseCertificates(raw)
	if err != nil || len(certs) == 0 {
		f.fail("--security-trust: no certificate in %s: %v", trustFile, err)
		return nil
	}
	// An authority vouches for the station certificates it signed; any other
	// certificate is pinned.
	for _, c := range certs {
		if c.IsCA {
			s.trust.Authorities = append(s.trust.Authorities, c)
		} else {
			s.trust.Fingerprints = append(s.trust.Fingerprints, sec.Fingerprint(c))
		}
	}
	return s
}

// algorithms reads the choice of the controlling station.
func (f *flags) algorithms(s *security) {
	if s == nil {
		if f.has("mal") || f.has("dpa") {
			f.fail("--mal and --dpa need --secure")
		}
		return
	}
	mac := f.integer("mal", int(sec.HMACSHA256.Code), 1, 255)
	s.mac, s.dpa = uint8(mac), uint8(f.integer("dpa", mac, 1, 255))
}

// accessRoles reads the roles of the controlling stations at a controlled
// station: none applies no access control.
func (f *flags) accessRoles(s *security) {
	list, ok := f.take("roles")
	switch {
	case !ok:
		return
	case s == nil:
		f.fail("--roles needs --secure")
		return
	}
	for _, name := range strings.Split(list, ",") {
		found := false
		for _, r := range sec.Roles {
			if strings.EqualFold(r.Name, strings.TrimSpace(name)) {
				s.roles, found = append(s.roles, r), true
			}
		}
		if !found {
			f.fail("--roles: unknown role %q", name)
		}
	}
}

// endpoint makes one end of an association. An algorithm the library does not
// have fails here, which the contract calls a usage error.
func (s *security) endpoint(role sec.Role, ca asdu.CommonAddr, originator uint8) (*sec.Endpoint, error) {
	ep, err := sec.New(sec.Config{Role: role, AssociationID: s.id, Identity: s.identity, Trust: s.trust,
		MAC: s.mac, DataProtection: s.dpa, Roles: s.roles, CommonAddr: ca, Originator: originator})
	if err != nil {
		return nil, fmt.Errorf("security: %w", err)
	}
	return ep, nil
}
