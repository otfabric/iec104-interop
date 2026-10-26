// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/pem"
	"os"
	"strconv"
	"strings"

	"github.com/cB-Sigma/iec60870/cs104"
	"github.com/cB-Sigma/iec60870/sec"
)

// security is the A-Profile of IEC 62351-5 (IEC TS 60870-5-7) as the
// security flags of the contract configure it; nil when --secure is not
// given.
type security struct {
	key   *sec.PrivateKey
	cert  []byte // DER
	trust sec.Trust
	id    uint16 // AIM of a controlling station, AIS of a controlled one
	mal   uint8  // controlling station
	dpa   uint8  // controlling station
	roles []sec.Role
}

// securityFlags reads the flags common to server and client. Every
// security flag without --secure is a usage error, so that a run never
// silently goes without the security it asked for.
func (a *args) securityFlags() (*security, error) {
	on := a.flag("--secure")
	certFile := a.str("--security-cert", "")
	keyFile := a.str("--security-key", "")
	trustFile := a.str("--security-trust", "")
	id, err := a.num("--association-id", 1)
	if err != nil {
		return nil, err
	}
	if !on {
		if certFile != "" || keyFile != "" || trustFile != "" || id != 1 {
			return nil, usagef("the security flags need --secure")
		}
		return nil, nil
	}
	if certFile == "" || keyFile == "" || trustFile == "" {
		return nil, usagef("--secure needs --security-cert, --security-key and --security-trust")
	}
	if id < 1 || id > 65535 {
		return nil, usagef("--association-id: %d is not 1..65535", id)
	}
	s := &security{id: uint16(id)}
	if s.cert, err = readCertificate(certFile); err != nil {
		return nil, err
	}
	if s.key, err = readKey(keyFile); err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(trustFile)
	if err != nil {
		return nil, usagef("--security-trust: cannot read %s", trustFile)
	}
	// A certificate authority vouches for the station certificates it
	// signed; any other certificate is pinned.
	for b, rest := pem.Decode(raw); b != nil; b, rest = pem.Decode(rest) {
		if b.Type != "CERTIFICATE" {
			continue
		}
		c, err := x509.ParseCertificate(b.Bytes)
		if err != nil {
			return nil, usagef("--security-trust: %v", err)
		}
		if c.IsCA {
			s.trust.CAs = append(s.trust.CAs, c.Raw)
		} else {
			cc, err := sec.ParseCertificate(c.Raw)
			if err != nil {
				return nil, usagef("--security-trust: %v", err)
			}
			s.trust.Fingerprints = append(s.trust.Fingerprints, cc.Fingerprint())
		}
	}
	if len(s.trust.CAs) == 0 && len(s.trust.Fingerprints) == 0 {
		return nil, usagef("--security-trust: no certificate in %s", trustFile)
	}
	return s, nil
}

func readCertificate(file string) ([]byte, error) {
	raw, err := os.ReadFile(file)
	if err != nil {
		return nil, usagef("--security-cert: cannot read %s", file)
	}
	for b, rest := pem.Decode(raw); b != nil; b, rest = pem.Decode(rest) {
		if b.Type == "CERTIFICATE" {
			if _, err := sec.ParseCertificate(b.Bytes); err != nil {
				return nil, usagef("--security-cert: %v", err)
			}
			return b.Bytes, nil
		}
	}
	return nil, usagef("--security-cert: no certificate in %s", file)
}

// readKey reads a PKCS #8 (or SEC 1) private key on secp256r1 or X25519.
func readKey(file string) (*sec.PrivateKey, error) {
	raw, err := os.ReadFile(file)
	if err != nil {
		return nil, usagef("--security-key: cannot read %s", file)
	}
	b, _ := pem.Decode(raw)
	if b == nil {
		return nil, usagef("--security-key: no PEM block in %s", file)
	}
	var k any
	if k, err = x509.ParsePKCS8PrivateKey(b.Bytes); err != nil {
		if k, err = x509.ParseECPrivateKey(b.Bytes); err != nil {
			return nil, usagef("--security-key: %v", err)
		}
	}
	if e, ok := k.(*ecdsa.PrivateKey); ok {
		if k, err = e.ECDH(); err != nil {
			return nil, usagef("--security-key: %v", err)
		}
	}
	x, ok := k.(*ecdh.PrivateKey)
	if !ok {
		return nil, usagef("--security-key: not an elliptic curve key")
	}
	switch x.Curve() {
	case ecdh.P256():
		return sec.ParsePrivateKey(sec.Secp256r1, x.Bytes())
	case ecdh.X25519():
		return sec.ParsePrivateKey(sec.Curve25519, x.Bytes())
	}
	return nil, usagef("--security-key: the curve is neither secp256r1 nor X25519")
}

// algorithms reads the controlling station's choice of MAC and data
// protection algorithm.
func (a *args) algorithms(s *security) error {
	malArg, dpaArg := a.str("--mal", ""), a.str("--dpa", "")
	if s == nil {
		if malArg != "" || dpaArg != "" {
			return usagef("--mal and --dpa need --secure")
		}
		return nil
	}
	mal := 4
	var err error
	if malArg != "" {
		if mal, err = strconv.Atoi(malArg); err != nil {
			return usagef("--mal: '%s' is not an integer", malArg)
		}
	}
	dpa := mal
	if dpaArg != "" {
		if dpa, err = strconv.Atoi(dpaArg); err != nil {
			return usagef("--dpa: '%s' is not an integer", dpaArg)
		}
	}
	if mal < 1 || mal > 255 || dpa < 1 || dpa > 255 {
		return usagef("--mal and --dpa are algorithm codes, 1..255")
	}
	s.mal, s.dpa = uint8(mal), uint8(dpa)
	return nil
}

// roleFlags reads the roles a controlled station gives every controlling
// station: none applies no access control.
func (a *args) roleFlags(s *security) error {
	list := a.str("--roles", "")
	if list == "" {
		return nil
	}
	if s == nil {
		return usagef("--roles needs --secure")
	}
	for _, name := range strings.Split(list, ",") {
		found := false
		for _, r := range sec.Roles {
			if strings.EqualFold(r.Name, strings.TrimSpace(name)) {
				s.roles, found = append(s.roles, r), true
			}
		}
		if !found {
			return usagef("--roles: unknown role %q (viewer, operator, engineer, installer, secadm, secaud, rbacmnt)", name)
		}
	}
	return nil
}

// link returns the security of one association: the connection of a
// controlling station, or one connection at the controlled station.
func (s *security) link(controlling bool, ca uint16) (*sec.SecureLink, error) {
	cfg := sec.Config{Controlling: controlling, Link: sec.TCP, Params: sec.Params104, MaxASDU: cs104.MaxASDU,
		CA: ca, Key: s.key, Certificate: s.cert, Trust: s.trust}
	if controlling {
		cfg.AIM, cfg.MAL, cfg.DPA = s.id, s.mal, s.dpa
	} else {
		cfg.AIS, cfg.Roles = s.id, s.roles
	}
	return sec.SecureConfig(cfg, sec.NewEndpoint)
}
