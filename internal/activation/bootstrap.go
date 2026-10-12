//
// OpenMCX Licence Information : AGPL-3.0-or-later
//

// Package activation builds what a one-time activation URL delivers (slice F1, plan
// section 10): the bootstrap package of one account's client. It holds the system's
// endpoints and service identities, the account's identities, the trust root and
// the MCS UE initial configuration document of TS 24.484 clause 7.2; never a password or key.
//
// Specifications: TS 24.484 V19.6.0 (Rel-19), TS 24.483 V19.2.0 (Rel-19).
package activation

import (
	"encoding/xml"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/openMCX/openmcx-admin/internal/config"
)

// Config is the system side of a bootstrap package, from the service configuration.
type Config struct {
	BaseURL string `json:"base_url"` // this service as clients reach it; the URLs are <base_url>/activate/<code>
	// Pin is the SHA-256 of this service's certificate (base64url), set at start: the
	// link carries it as #pin= (never sent), so that a device that does not trust the
	// system CA yet fetches its package from this service and no other (slice F2).
	Pin string `json:"-"`
	// AddressMap: "host:port" of a server as the package names it -> "address:port" a
	// client reaches it at (phones through a host's published ports); optional.
	AddressMap map[string]string `json:"address_map,omitempty"`
	TTLSeconds int               `json:"ttl_seconds,omitempty"`
	IdMS       struct {
		Issuer      string `json:"issuer"`
		ClientID    string `json:"client_id"`
		RedirectURI string `json:"redirect_uri"`
	} `json:"idms"`
	SIPCoreURI     string `json:"sip_core_uri"`
	CredentialsURL string `json:"credentials_url"`
	Services       struct {
		MCPTT   string `json:"mcptt"`
		MCData  string `json:"mcdata,omitempty"`
		MCVideo string `json:"mcvideo,omitempty"`
	} `json:"services"`
	// The endpoints a client uses (cms, gms, kms, ...), as its configuration names them.
	Endpoints     map[string]string `json:"endpoints"`
	TrustRootFile string            `json:"trust_root_file"`
}

// ------------------------------------------------------------------------------
// Package is the bootstrap package of one user, in OpenMCX's own format "openmcx-activation-1": the system's
// endpoints, the user's identities and first group, the trust root and the UE initial configuration document.
func Package(cfg *Config, doc *config.Document, user config.User) (map[string]any, error) {
	root, err := os.ReadFile(cfg.TrustRootFile)
	if err != nil {
		return nil, fmt.Errorf("trust root: %w", err)
	}
	group := ""
	if len(user.Groups) > 0 {
		group = user.Groups[0]
	}
	clientID := ""
	if len(user.ClientIDs) > 0 {
		clientID = user.ClientIDs[0]
	}
	out := map[string]any{
		"format": "openmcx-activation-1",
		"domain": doc.System.Domain,
		"login":  user.Login,
		"idms":   map[string]string{"issuer": cfg.IdMS.Issuer, "client_id": cfg.IdMS.ClientID, "redirect_uri": cfg.IdMS.RedirectURI},
		"sip":    map[string]string{"core_uri": cfg.SIPCoreURI, "identity": user.SIPURI},
		"mcptt": map[string]string{"user_id": user.MCPTTID, "client_id": clientID, "service_uri": cfg.Services.MCPTT,
			"group_id": group},
		"credentials_url": cfg.CredentialsURL,
		// The device's TLS client certificate comes from the Credential Service too
		// (POST <credentials_url>/v1/client-certificate with a PKCS#10 request).
		"certificate_url": strings.TrimSuffix(cfg.CredentialsURL, "/") + "/v1/client-certificate",
		"endpoints":       cfg.Endpoints,
		"trust_root":      string(root),
	}
	if user.MCDataID != "" && cfg.Services.MCData != "" {
		out["mcdata"] = map[string]string{"user_id": user.MCDataID, "service_uri": cfg.Services.MCData, "group_id": group}
	}
	if user.MCVideoID != "" && cfg.Services.MCVideo != "" {
		out["mcvideo"] = map[string]string{"user_id": user.MCVideoID, "service_uri": cfg.Services.MCVideo, "group_id": group}
	}
	if len(cfg.AddressMap) > 0 {
		out["address_map"] = cfg.AddressMap
	}
	out["mcptt"].(map[string]string)["profile_index"] = strconv.Itoa(ProfileIndex(user))
	initial, err := initialConfiguration(cfg, doc.System.Domain, user)
	if err != nil {
		return nil, err
	}
	out["ue_initial_configuration"] = initial
	return out, nil
}

// The <mcptt-UE-initial-configuration> of TS 24.484 clause 7.2.2.1, in the form openmcx-server serves:
// the application servers, the XCAP roots and the service identities. The lab has no PLMN or
// document protection, so those elements carry fixed lab values.
type ueInitialConfig struct {
	XMLName xml.Name `xml:"urn:3gpp:mcptt:mcpttUEinitConfig:1.0 mcptt-UE-initial-configuration"`
	Domain  string   `xml:"domain,attr"`
	// <Default-user-profile> (TS 24.484 clause 7.2.2.1): the user's MCPTT user profile document, which the client
	// reads at mcptt-user-profile-<index>.xml (clause 8.3.1A).
	DefaultProfile *ueDefaultProfile `xml:"Default-user-profile,omitempty"`
	OnNetwork      ueOnNetwork       `xml:"on-network"`
}

// TS 24.484 clause 7.2.2.3: the UserProfileType of <Default-user-profile>.
type ueDefaultProfile struct {
	UserID string `xml:"User-ID,attr"`
	Index  int    `xml:"user-profile-index,attr"`
}

// ------------------------------------------------------------------------------
// ProfileIndex is the "user-profile-index" of the user's MCPTT user profile (TS 24.484 clause 8.3.2.1): its
// profile_index, 1 when absent, as the CMS numbers it.
func ProfileIndex(user config.User) int {
	switch index := user.Profile["profile_index"].(type) {
	case float64:
		if index >= 1 && index <= 255 {
			return int(index)
		}
	case int:
		if index >= 1 && index <= 255 {
			return index
		}
	}
	return 1
}

type ueOnNetwork struct {
	Timers struct {
		T100, T101, T103, T104, T132 int
	} `xml:"Timers"`
	HPLMN struct {
		PLMN    string `xml:"PLMN,attr"`
		Service struct {
			MCPTT      string `xml:"MCPTT-to-con-ref"`
			CommonCore string `xml:"MC-common-core-to-con-ref"`
			ID         string `xml:"MC-ID-to-con-ref"`
		} `xml:"service"`
	} `xml:"HPLMN"`
	AppServer struct {
		AuthEndpoint  string `xml:"idms-auth-endpoint"`
		TokenEndpoint string `xml:"idms-token-endpoint"`
		HTTPProxy     string `xml:"http-proxy"`
		GMS           string `xml:"gms"`
		CMS           string `xml:"cms"`
		KMS           string `xml:"kms"`
		TLSTunnel     struct {
			Mutual bool `xml:"mutual-authentication"`
		} `xml:"tls-tunnel-auth-method"`
	} `xml:"App-Server-Info"`
	GMSURI                    string `xml:"GMS-URI"`
	GroupCreationXUI          string `xml:"group-creation-XUI"`
	GMSRoot                   string `xml:"GMS-XCAP-root-URI"`
	CMSRoot                   string `xml:"CMS-XCAP-root-URI"`
	IntegrityProtection       bool   `xml:"integrity-protection-enabled"`
	ConfidentialityProtection bool   `xml:"confidentiality-protection-enabled"`
	AnyExt                    struct {
		MCPTT   *serviceDetails `xml:"MCPTT-Service-Details,omitempty"`
		MCVideo *serviceDetails `xml:"MCVideo-Service-Details,omitempty"`
		MCData  *serviceDetails `xml:"MCData-Service-Details,omitempty"`
		SIPCore string          `xml:"urn:openmcx:ns:userProfile:1.0 sip-core,omitempty"`
	} `xml:"anyExt"`
}

type serviceDetails struct {
	IPv6Required bool   `xml:"IPv6-Required"`
	ServerURI    string `xml:"Server-URI"`
}

// ------------------------------------------------------------------------------
// initialConfiguration renders the MCS UE initial configuration document of TS 24.484 clause 7.2 with the data
// semantics of TS 24.484 clause 7.2.2.7: the timers T100, T101, T103, T104 and T132 in seconds, the IdMS
// authorization and token endpoints, the GMS, CMS and KMS, the <GMS-URI>, the GMS and CMS XCAP root URIs
// (<group-creation-XUI> is the GMS XCAP root URI, TS 24.483 clause 8.2.9A) and
// the MCPTT, MCVideo and MCData servers.
func initialConfiguration(cfg *Config, domain string, user config.User) (string, error) {
	var initial ueInitialConfig
	initial.Domain = domain
	if user.MCPTTID != "" {
		initial.DefaultProfile = &ueDefaultProfile{UserID: user.MCPTTID, Index: ProfileIndex(user)}
	}
	on := &initial.OnNetwork
	on.Timers.T100, on.Timers.T101, on.Timers.T103, on.Timers.T104, on.Timers.T132 = 1, 1, 4, 1, 2
	on.HPLMN.PLMN = "001-01"
	on.HPLMN.Service.MCPTT, on.HPLMN.Service.CommonCore, on.HPLMN.Service.ID = "lab", "lab", "lab"
	issuer := strings.TrimSuffix(cfg.IdMS.Issuer, "/")
	on.AppServer.AuthEndpoint, on.AppServer.TokenEndpoint = issuer+"/authorize", issuer+"/token"
	on.AppServer.GMS, on.AppServer.CMS, on.AppServer.KMS = cfg.Endpoints["gms"], cfg.Endpoints["cms"], cfg.Endpoints["kms"]
	on.GMSURI = cfg.Services.MCPTT
	on.GMSRoot = strings.TrimSuffix(cfg.Endpoints["gms"], "/") + "/xcap-root"
	on.CMSRoot = strings.TrimSuffix(cfg.Endpoints["cms"], "/") + "/xcap-root"
	on.GroupCreationXUI = on.GMSRoot
	on.AnyExt.MCPTT = &serviceDetails{ServerURI: cfg.Services.MCPTT}
	if cfg.Services.MCVideo != "" {
		on.AnyExt.MCVideo = &serviceDetails{ServerURI: cfg.Services.MCVideo}
	}
	if cfg.Services.MCData != "" {
		on.AnyExt.MCData = &serviceDetails{ServerURI: cfg.Services.MCData}
	}
	on.AnyExt.SIPCore = cfg.SIPCoreURI
	raw, err := xml.Marshal(initial)
	if err != nil {
		return "", err
	}
	return xml.Header + string(raw), nil
}
