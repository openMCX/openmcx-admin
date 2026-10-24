//
// OpenMCX Licence Information : AGPL-3.0-or-later
//

// The 3GPP TLS profile of MCX Admin's TLS server and clients, as for the OpenMCX servers' (openmcx-server
// services/internal/common/tls_profile.go): TS 33.210 clause 6.2, which TS 33.180 clause 6.1 refers to through
// TS 33.310 annex E. TLS 1.2 and 1.3; for TLS 1.2 only ECDHE with AEAD (clause 6.2.3); TLS 1.3 keeps crypto/tls's
// suites (RFC 8446 section 9.1). crypto/tls has no finite field Diffie-Hellman and no SHA-1 signatures in TLS 1.2
// handshakes.
//
// Specifications: TS 33.210 V19.3.0, TS 33.180 V19.5.0, TS 33.310 V19.5.0 (Rel-19); RFC 5246, RFC 8446.

package main

import (
	"crypto/tls"
	"net/http"
)

// tls12CipherSuites: TS 33.210 clause 6.2.3, the two mandatory suites first, then the recommended AES-256 ones and
// ChaCha20-Poly1305 ("Only cipher suites with AEAD (e.g. GCM) and PFS (i.e., ECDHE) shall be supported").
var tls12CipherSuites = []uint16{
	tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256, tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
	tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384, tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
	tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305_SHA256, tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305_SHA256,
}

// tlsProfile applies the profile to a client or server configuration and returns it.
func tlsProfile(config *tls.Config) *tls.Config {
	config.MinVersion = tls.VersionTLS12
	config.CipherSuites = tls12CipherSuites
	return config
}

// applyDefaultTLSProfile puts the profile on http.DefaultTransport, which the clients of the internal packages use
// when they are given none.
func applyDefaultTLSProfile() {
	transport := http.DefaultTransport.(*http.Transport)
	if transport.TLSClientConfig == nil {
		transport.TLSClientConfig = &tls.Config{}
	}
	tlsProfile(transport.TLSClientConfig)
}
