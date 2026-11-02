// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"os"
)

// tlsFlags reads the transport security flags of the contract (IEC 62351-3):
// nil without --tls. Every TLS flag without --tls is a usage error, as is a
// file that cannot be loaded, so that a run never silently goes without the
// security it asked for.
//
// The session is crypto/tls, the library's iotsec.StdTLS without the host name
// check: cs104.Accept and cs104.Open take any net.Conn, and the certificate says
// who the station is, whatever the network calls it.
func (a *args) tlsFlags(secure bool) (*tls.Config, error) {
	on := a.flag("--tls")
	certFile := a.str("--tls-cert", "")
	keyFile := a.str("--tls-key", "")
	trustFile := a.str("--tls-trust", "")
	if !on {
		if certFile != "" || keyFile != "" || trustFile != "" {
			return nil, usagef("the transport security flags need --tls")
		}
		return nil, nil
	}
	if secure {
		return nil, usagef("--tls and --secure cannot be combined")
	}
	if certFile == "" || keyFile == "" || trustFile == "" {
		return nil, usagef("--tls needs --tls-cert, --tls-key and --tls-trust")
	}
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, usagef("--tls-cert, --tls-key: %v", err)
	}
	if k, ok := cert.PrivateKey.(*ecdsa.PrivateKey); !ok || k.Curve != elliptic.P256() {
		return nil, usagef("--tls-key: the key is not on secp256r1")
	}
	raw, err := os.ReadFile(trustFile)
	if err != nil {
		return nil, usagef("--tls-trust: cannot read %s", trustFile)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(raw) {
		return nil, usagef("--tls-trust: no certificate in %s", trustFile)
	}
	return &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{cert},
		// Both directions go through VerifyConnection, which also runs on a
		// resumed session: the chain against --tls-trust, and no name.
		ClientAuth:         tls.RequireAnyClientCert,
		InsecureSkipVerify: true,
		VerifyConnection:   func(cs tls.ConnectionState) error { return verifyPeer(cs, roots) },
	}, nil
}

// verifyPeer accepts a certificate one of the authorities signed.
func verifyPeer(cs tls.ConnectionState, roots *x509.CertPool) error {
	if len(cs.PeerCertificates) == 0 {
		return errors.New("the remote station presented no certificate")
	}
	opts := x509.VerifyOptions{Roots: roots, Intermediates: x509.NewCertPool(),
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny}}
	for _, c := range cs.PeerCertificates[1:] {
		opts.Intermediates.AddCert(c)
	}
	_, err := cs.PeerCertificates[0].Verify(opts)
	return err
}
