//
// OpenMCX Licence Information : AGPL-3.0-or-later
//

// Tests of the TS 33.210 clause 6.2 TLS profile (tls_profile.go): a profiled server accepts TLS 1.3 and TLS 1.2 with
// ECDHE and AEAD, and refuses a TLS 1.2 client that offers only CBC suites (crypto/tls has no RSA key exchange
// left to offer), and TLS 1.1; the default transport offers only the profile's TLS 1.2 suites.

package main

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"testing"
)

// ------------------------------------------------------------------------------
func TestTLSProfile(t *testing.T) {
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	server.TLS = tlsProfile(&tls.Config{})
	server.StartTLS()
	defer server.Close()
	pool := server.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs
	for _, c := range []struct {
		name   string
		client *tls.Config
		ok     bool
	}{
		{"TLS 1.3", &tls.Config{MinVersion: tls.VersionTLS13}, true},
		{"TLS 1.2 ECDHE-RSA-AES128-GCM", &tls.Config{MaxVersion: tls.VersionTLS12,
			CipherSuites: []uint16{tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256}}, true},
		{"TLS 1.2 ECDHE with CBC", &tls.Config{MaxVersion: tls.VersionTLS12,
			CipherSuites: []uint16{tls.TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA, tls.TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA256}}, false},
		{"TLS 1.1", &tls.Config{MinVersion: tls.VersionTLS10, MaxVersion: tls.VersionTLS11}, false},
	} {
		c.client.RootCAs = pool
		client := &http.Client{Transport: &http.Transport{TLSClientConfig: c.client}}
		response, err := client.Get(server.URL)
		if err == nil {
			response.Body.Close()
		}
		if (err == nil) != c.ok {
			t.Errorf("%s: handshake ok %v, want %v (%v)", c.name, err == nil, c.ok, err)
		}
	}
}

// ------------------------------------------------------------------------------
func TestDefaultTLSProfile(t *testing.T) {
	applyDefaultTLSProfile()
	config := http.DefaultTransport.(*http.Transport).TLSClientConfig
	if config.MinVersion != tls.VersionTLS12 || len(config.CipherSuites) != len(tls12CipherSuites) {
		t.Errorf("default transport: MinVersion %x, suites %v", config.MinVersion, config.CipherSuites)
	}
}
