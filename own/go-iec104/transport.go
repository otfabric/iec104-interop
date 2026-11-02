// SPDX-License-Identifier: MIT

package main

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
)

// transport is the TLS of IEC 62351-3 as the transport security flags of the
// contract configure it; nil without --tls.
type transport struct {
	identity    tls.Certificate
	authorities *x509.CertPool
}

// transport reads the flags common to server and client. Every TLS flag without
// --tls is a usage error, so that a run never silently goes without the security
// it asked for.
func (f *flags) transport() *transport {
	on := f.has("tls")
	certFile, hasCert := f.take("tls-cert")
	keyFile, hasKey := f.take("tls-key")
	trustFile, hasTrust := f.take("tls-trust")
	if !on {
		if hasCert || hasKey || hasTrust {
			f.fail("the TLS flags need --tls")
		}
		return nil
	}
	if !hasCert || !hasKey || !hasTrust {
		f.fail("--tls needs --tls-cert, --tls-key and --tls-trust")
		return nil
	}
	t := &transport{authorities: x509.NewCertPool()}
	var err error
	if t.identity, err = tls.LoadX509KeyPair(certFile, keyFile); err != nil {
		f.fail("--tls-cert, --tls-key: %v", err)
		return nil
	}
	raw, err := os.ReadFile(trustFile)
	if err != nil {
		f.fail("--tls-trust: %v", err)
		return nil
	}
	if !t.authorities.AppendCertsFromPEM(raw) {
		f.fail("--tls-trust: no certificate in %s", trustFile)
		return nil
	}
	return t
}

// verify checks the certificate of the remote station against the authorities of
// --tls-trust. crypto/tls cannot do that without also checking a host name
// (client) or an extended key usage (server); the contract wants neither.
func (t *transport) verify(state tls.ConnectionState) error {
	if len(state.PeerCertificates) == 0 {
		return errors.New("the remote station presented no certificate")
	}
	intermediates := x509.NewCertPool()
	for _, c := range state.PeerCertificates[1:] {
		intermediates.AddCert(c)
	}
	_, err := state.PeerCertificates[0].Verify(x509.VerifyOptions{Roots: t.authorities, Intermediates: intermediates,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny}})
	if err != nil {
		return fmt.Errorf("the certificate of the remote station: %w", err)
	}
	return nil
}

func (t *transport) client() *tls.Config {
	return &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{t.identity},
		InsecureSkipVerify: true, VerifyConnection: t.verify}
}

func (t *transport) server() *tls.Config {
	return &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{t.identity},
		ClientAuth: tls.RequireAnyClientCert, VerifyConnection: t.verify}
}
