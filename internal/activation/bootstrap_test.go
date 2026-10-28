//
// OpenMCX Licence Information : AGPL-3.0-or-later
//

package activation

import (
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
		"<integrity-protection-enabled>true</integrity-protection-enabled>",
		"<confidentiality-protection-enabled>false</confidentiality-protection-enabled>",
	} {
		if !strings.Contains(initial, want) {
			test.Fatalf("missing %s in %s", want, initial)
		}
	}
}
