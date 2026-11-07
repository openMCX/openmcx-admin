//
// OpenMCX Licence Information : AGPL-3.0-or-later
//

package activation

import (
	"os"
	"strings"
	"testing"

	"github.com/openMCX/openmcx-admin/internal/config"
)

// ------------------------------------------------------------------------------
// testConfig: an activation configuration with the servers' URIs and, separately, the XCAP root URIs.
func testConfig() *Config {
	cfg := &Config{
		Endpoints: map[string]string{"cms": "https://http-proxy:8443/cms", "gms": "https://http-proxy:8443/gms",
			"kms": "https://kms:8443"},
		XCAPRoots: map[string]string{"cms": "https://http-proxy:8443/cms/xcap-root",
			"gms": "https://http-proxy:8443/gms/xcap-root"},
	}
	cfg.IdMS.Issuer = "https://idms:8443"
	cfg.Services.MCPTT = "sip:mcptt@mcx.test"
	return cfg
}

// ------------------------------------------------------------------------------
// TestInitialConfigurationXCAPRoots: TS 24.484 clause 7.2.2.7: <gms> and <cms> carry the URIs used to contact the
// servers (item 4), <GMS-XCAP-root-URI> and <CMS-XCAP-root-URI> the configured XCAP roots as given (items 7 and 8),
// with no path appended; without XCAP roots no document is made.
func TestInitialConfigurationXCAPRoots(test *testing.T) {
	cfg := testConfig()
	user := config.User{MCPTTID: "sip:bob.mcptt@mcx.test"}
	initial, err := initialConfiguration(cfg, "mcx.test", user)
	if err != nil {
		test.Fatal(err)
	}
	for _, want := range []string{
		"<gms>https://http-proxy:8443/gms</gms>", "<cms>https://http-proxy:8443/cms</cms>",
		"https://http-proxy:8443/gms/xcap-root<", "https://http-proxy:8443/cms/xcap-root<",
	} {
		if !strings.Contains(initial, want) {
			test.Fatalf("missing %s in %s", want, initial)
		}
	}
	if strings.Contains(initial, "xcap-root/xcap-root") {
		test.Fatalf("XCAP root path appended twice: %s", initial)
	}
	cfg.XCAPRoots = nil
	if _, err := initialConfiguration(cfg, "mcx.test", user); err == nil {
		test.Fatal("a document without XCAP roots was made")
	}
}

// ------------------------------------------------------------------------------
// TestClientEndpoints: the package gives the client the XCAP roots as its gms and cms endpoints, the other
// endpoints unchanged, and leaves the configuration as it was.
func TestClientEndpoints(test *testing.T) {
	cfg := testConfig()
	got := clientEndpoints(cfg)
	if got["cms"] != "https://http-proxy:8443/cms/xcap-root" || got["gms"] != "https://http-proxy:8443/gms/xcap-root" ||
		got["kms"] != "https://kms:8443" {
		test.Fatalf("endpoints %v", got)
	}
	if cfg.Endpoints["cms"] != "https://http-proxy:8443/cms" {
		test.Fatalf("configuration changed: %v", cfg.Endpoints)
	}
}

// ------------------------------------------------------------------------------
// TestInitialConfigurationGMSURIAndProtection: <GMS-URI> is the configured GMS subscription proxy PSI, the MCPTT PSI
// when none is set (TS 24.484 clause 7.2.2.1 item 4); the UE-wide protection pair (items 8 and 9) is the deployment's
// own value, "false" when not set.
func TestInitialConfigurationGMSURIAndProtection(test *testing.T) {
	cfg := testConfig()
	cfg.Services.MCPTT = "sip:mcptt@mcx.test"
	user := config.User{MCPTTID: "sip:bob.mcptt@mcx.test"}
	initial, err := initialConfiguration(cfg, "mcx.test", user)
	if err != nil {
		test.Fatal(err)
	}
	for _, want := range []string{
		"<GMS-URI>sip:mcptt@mcx.test</GMS-URI>",
		"<integrity-protection-enabled>false</integrity-protection-enabled>",
		"<confidentiality-protection-enabled>false</confidentiality-protection-enabled>",
	} {
		if !strings.Contains(initial, want) {
			test.Fatalf("missing %s in %s", want, initial)
		}
	}
	cfg.GMSURI = "sip:gms-proxy@mcx.test"
	cfg.UEProtection.Integrity = true
	initial, err = initialConfiguration(cfg, "mcx.test", user)
	if err != nil {
		test.Fatal(err)
	}
	for _, want := range []string{
		"<GMS-URI>sip:gms-proxy@mcx.test</GMS-URI>",
		"<http-proxy></http-proxy>",
		"<tls-tunnel-auth-method><mutual-authentication>false</mutual-authentication></tls-tunnel-auth-method>",
		"<integrity-protection-enabled>true</integrity-protection-enabled>",
		"<confidentiality-protection-enabled>false</confidentiality-protection-enabled>",
	} {
		if !strings.Contains(initial, want) {
			test.Fatalf("missing %s in %s", want, initial)
		}
	}
}

// TestInitialConfigurationHTTPProxy: <http-proxy> is the configured home HTTP proxy (TS 24.484 clause 7.2.2.1 item 3
// c)), with one-way TLS tunnel authentication (item 3 g), TS 24.483 clause 8.2.44B), in the schema's order.
func TestInitialConfigurationHTTPProxy(test *testing.T) {
	cfg := testConfig()
	cfg.Services.MCPTT = "sip:mcptt@mcx.test"
	cfg.HTTPProxy = "https://proxy.mcx.test:8443"
	initial, err := initialConfiguration(cfg, "mcx.test", config.User{MCPTTID: "sip:bob.mcptt@mcx.test"})
	if err != nil {
		test.Fatal(err)
	}
	proxy := strings.Index(initial, "<http-proxy>https://proxy.mcx.test:8443</http-proxy>")
	gms, tunnel := strings.Index(initial, "<gms>"), strings.Index(initial, "<tls-tunnel-auth-method>")
	if proxy < 0 || gms < proxy || tunnel < gms ||
		!strings.Contains(initial, "<mutual-authentication>false</mutual-authentication>") {
		test.Fatalf("home HTTP proxy not in the UE initial configuration: %s", initial)
	}
}

// ------------------------------------------------------------------------------
// TestPackageIMSAKAOffer: with activation.ims_aka.pcscf the package names the preconfigured P-CSCF of the IMS AKA
// offer under sip (TS 24.229 clause 9.2.1); without it the package has none and is as before.
func TestPackageIMSAKAOffer(test *testing.T) {
	cfg := testConfig()
	root := test.TempDir() + "/ca.pem"
	if err := os.WriteFile(root, []byte("-----BEGIN CERTIFICATE-----\n-----END CERTIFICATE-----\n"), 0o600); err != nil {
		test.Fatal(err)
	}
	cfg.TrustRootFile = root
	doc := &config.Document{System: config.System{Domain: "mcx.test"}}
	user := config.User{Login: "alice", MCPTTID: "sip:alice.mcptt@mcx.test", SIPURI: "sip:alice@mcx.test"}
	out, err := Package(cfg, doc, user)
	if err != nil {
		test.Fatal(err)
	}
	if _, offered := out["sip"].(map[string]string)["ims_aka_pcscf"]; offered {
		test.Fatalf("IMS AKA offered without a P-CSCF: %v", out["sip"])
	}
	cfg.IMSAKA.PCSCF = "pcscf.mcx.test:5060"
	out, err = Package(cfg, doc, user)
	if err != nil {
		test.Fatal(err)
	}
	if got := out["sip"].(map[string]string)["ims_aka_pcscf"]; got != "pcscf.mcx.test:5060" {
		test.Fatalf("ims_aka_pcscf = %q", got)
	}
}
