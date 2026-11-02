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

// The transport security flags, for the client only. go-iecp5 dials TLS for a
// client (cs104/common.go, openConnection). Its server cannot listen with TLS:
// cs104/server.go ListenAndServer calls net.Listen whatever TLSConfig says, and
// a session cannot be built from outside the package. So tlsServer is false,
// and the flags are unknown to "server".

// clientTLS reads the flags. It returns nil without --tls.
func clientTLS(a *args) (*tls.Config, error) {
	on := a.flag("--tls")
	certFile, keyFile, trustFile := a.str("--tls-cert", ""), a.str("--tls-key", ""), a.str("--tls-trust", "")
	if !on {
		if certFile != "" || keyFile != "" || trustFile != "" {
			return nil, usagef("--tls-cert, --tls-key and --tls-trust need --tls")
		}
		return nil, nil
	}
	if certFile == "" || keyFile == "" || trustFile == "" {
		return nil, usagef("--tls needs --tls-cert, --tls-key and --tls-trust")
	}
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, usagef("--tls-cert, --tls-key: %v", err)
	}
	if key, ok := cert.PrivateKey.(*ecdsa.PrivateKey); !ok || key.Curve != elliptic.P256() {
		return nil, usagef("--tls-key: the key must be on secp256r1 (P-256)")
	}
	raw, err := os.ReadFile(trustFile)
	if err != nil {
		return nil, usagef("--tls-trust: %v", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(raw) {
		return nil, usagef("--tls-trust: %s has no certificate", trustFile)
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
		// The certificate says who the station is, whatever the network calls
		// it: the chain is checked below, and a name is not.
		InsecureSkipVerify:    true,
		VerifyPeerCertificate: verifyStation(roots),
	}, nil
}

func verifyStation(roots *x509.CertPool) func([][]byte, [][]*x509.Certificate) error {
	return func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
		if len(rawCerts) == 0 {
			return errors.New("the station presented no certificate")
		}
		intermediates := x509.NewCertPool()
		var leaf *x509.Certificate
		for i, raw := range rawCerts {
			cert, err := x509.ParseCertificate(raw)
			if err != nil {
				return err
			}
			if i == 0 {
				leaf = cert
			} else {
				intermediates.AddCert(cert)
			}
		}
		_, err := leaf.Verify(x509.VerifyOptions{
			Roots: roots, Intermediates: intermediates, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny},
		})
		return err
	}
}

// handshakeWatch is a session cache that never has a session. It is how the
// adapter learns that the TCP connection is up: go-iecp5 does the connect and
// the handshake in one call under one timeout, and says nothing until both are
// over. crypto/tls asks the cache just before it sends its ClientHello.
type handshakeWatch struct{ started func() }

func (w handshakeWatch) Get(string) (*tls.ClientSessionState, bool) {
	w.started()
	return nil, false
}

func (handshakeWatch) Put(string, *tls.ClientSessionState) {}
