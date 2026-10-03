// Command mcxadmin-server serves the MCX Admin API (docs/SLICE_B.md).
//
//	mcxadmin-server --config server.json                  serve
//	mcxadmin-server --config server.json --import site.json   seed an empty store from a site.json, then exit
//	mcxadmin-server --config server.json --seed site.json     seed the store if it is empty, then serve (labs)
//
// server.json names files for every secret; none is written in it:
//
//	{"listen": ":8443", "domain": "mcx.test", "tls_cert": "...", "tls_key": "...",
//	 "mysql_dsn_file": "...", "idms": {"url": "https://idms:8443", "client_id": "mcx-server", "secret_file": "...",
//	 "ca_file": "..."}, "projections": {"userdb_file": "/shared/userdb.json"}}
//
// openMCX administration (no 3GPP specification).
package main

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"github.com/openMCX/openmcx-admin/internal/activation"
	"github.com/openMCX/openmcx-admin/internal/api"
	"github.com/openMCX/openmcx-admin/internal/config"
	"github.com/openMCX/openmcx-admin/internal/project"
	"github.com/openMCX/openmcx-admin/internal/store"
	"github.com/openMCX/openmcx-admin/internal/web"
)

type serverConfig struct {
	Listen       string `json:"listen"`
	Domain       string `json:"domain"`
	TLSCert      string `json:"tls_cert"`
	TLSKey       string `json:"tls_key"`
	MySQLDSNFile string `json:"mysql_dsn_file"`
	Projections  struct {
		// The users file of mcx-user-db --watch (slice C); empty: not projected.
		UserDBFile string `json:"userdb_file"`
		// The Credential Service (slice F1); empty: SIP credentials not followed.
		CredentialsURL string `json:"credentials_url"`
		// The GMS (slice D2); empty: groups are not written to it.
		GMSURL string `json:"gms_url"`
	} `json:"projections"`
	IdMS struct {
		URL        string `json:"url"`
		ClientID   string `json:"client_id"`
		SecretFile string `json:"secret_file"`
		CAFile     string `json:"ca_file"`
	} `json:"idms"`
	// One-time activation URLs of clients (slice F1); absent: not offered.
	Activation *activation.Config `json:"activation"`
	// The web console (slice E1); absent: API only.
	Console *web.Config `json:"console"`
	// The services the dashboard checks (health URLs through ca_file, or TCP).
	Status struct {
		Services []api.Probe `json:"services"`
		// The SIP core's operator RPC (its -A CORE_RPC), for the registrations.
		SIPCoreRPC string `json:"sip_core_rpc"`
	} `json:"status"`
}

// ------------------------------------------------------------------------------
// main reads the flags and runs the server, exiting with its error.
func main() {
	configPath := flag.String("config", "", "server configuration (JSON)")
	importSite := flag.String("import", "", "seed an empty store from this site.json and exit")
	seedSite := flag.String("seed", "", "seed the store from this site.json if it is empty, then serve")
	flag.Parse()
	if err := run(*configPath, *importSite, *seedSite); err != nil {
		log.Fatalf("mcxadmin-server: %v", err)
	}
}

// ------------------------------------------------------------------------------
// secretFile reads a secret from the file server.json names, trimmed of surrounding white space; the secret itself
// is never in server.json.
func secretFile(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(raw)), nil
}

// ------------------------------------------------------------------------------
// run opens the store (waiting up to 60 s for the database), seeds it from a site.json when asked, starts the
// projections to the user database, the Credential Service and the GMS, and serves the API and the console over
// TLS. The configuration is checked before anything starts.
func run(configPath, importSite, seedSite string) error {
	cfg, err := loadConfig(configPath, importSite == "")
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	st, err := openStore(ctx, cfg)
	if err != nil {
		return err
	}
	if importSite != "" {
		return seed(ctx, st, cfg.Domain, importSite)
	}
	if seedSite != "" {
		if rev, _, err := st.Current(ctx, cfg.Domain); err != nil {
			return err
		} else if rev == 0 {
			if err := seed(ctx, st, cfg.Domain, seedSite); err != nil {
				return err
			}
		}
	}
	if err := st.EnsureConsumers(ctx); err != nil {
		return err
	}
	srv, err := newServer(cfg, st)
	if err != nil {
		return err
	}
	httpServer := &http.Server{Addr: cfg.Listen, Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second,
		TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12}}
	log.Printf("mcxadmin-server listening on %s for %s", cfg.Listen, cfg.Domain)
	return httpServer.ListenAndServeTLS(cfg.TLSCert, cfg.TLSKey)
}

// ------------------------------------------------------------------------------
// loadConfig reads server.json; to serve, the TLS certificate and key are required.
func loadConfig(path string, serve bool) (*serverConfig, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg serverConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if serve && (cfg.TLSCert == "" || cfg.TLSKey == "") {
		return nil, errors.New("tls_cert and tls_key are required")
	}
	return &cfg, nil
}

// ------------------------------------------------------------------------------
// openStore connects to MySQL, waiting until ctx ends for a database that is still starting, applies the
// migrations and names the consumers the configuration projects to.
func openStore(ctx context.Context, cfg *serverConfig) (*store.Store, error) {
	dsn, err := secretFile(cfg.MySQLDSNFile)
	if err != nil {
		return nil, fmt.Errorf("mysql_dsn_file: %w", err)
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, err
	}
	for db.PingContext(ctx) != nil {
		select {
		case <-ctx.Done():
			return nil, errors.New("database unreachable")
		case <-time.After(time.Second):
		}
	}
	st, err := store.Open(ctx, db)
	if err != nil {
		return nil, err
	}
	if cfg.Projections.CredentialsURL != "" {
		st.Consumers = append(st.Consumers, "sip")
	}
	if cfg.Projections.GMSURL != "" {
		st.Consumers = append(st.Consumers, "gms")
	}
	return st, nil
}

// ------------------------------------------------------------------------------
// newServer builds the API server with its clients (IdMS, Credential Service, SIP core) and starts the
// projections in the background.
func newServer(cfg *serverConfig, st *store.Store) (*api.Server, error) {
	secret, err := secretFile(cfg.IdMS.SecretFile)
	if err != nil {
		return nil, fmt.Errorf("idms.secret_file: %w", err)
	}
	client := &http.Client{Timeout: 5 * time.Second}
	if cfg.IdMS.CAFile != "" {
		pool, err := certificatePool(cfg.IdMS.CAFile)
		if err != nil {
			return nil, fmt.Errorf("idms.ca_file: %w", err)
		}
		client.Transport = &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}
	}
	if cfg.Activation != nil {
		// The link pins this service's own certificate (slice F2).
		pin, err := certificatePin(cfg.TLSCert)
		if err != nil {
			return nil, fmt.Errorf("activation pin: %w", err)
		}
		cfg.Activation.Pin = pin
	}
	idms := &api.IdMS{URL: cfg.IdMS.URL, ClientID: cfg.IdMS.ClientID, Secret: secret, Client: client}
	srv := &api.Server{Store: st, Domain: cfg.Domain, IdMS: idms, Links: idms, Sessions: idms,
		Ready: startProjections(cfg, st, secret, client), ServerClientID: cfg.IdMS.ClientID, ServerSecret: secret,
		Activation: cfg.Activation, Probes: cfg.Status.Services, ProbeClient: client}
	if cfg.Status.SIPCoreRPC != "" {
		// This service's certificate is its credential at the SIP core.
		rpc, err := clientWithCertificate(cfg.IdMS.CAFile, cfg.TLSCert, cfg.TLSKey)
		if err != nil {
			return nil, fmt.Errorf("status.sip_core_rpc: %w", err)
		}
		srv.SIPCore = &api.SIPCoreRPC{URL: cfg.Status.SIPCoreRPC, Client: rpc}
	}
	if cfg.Projections.CredentialsURL != "" {
		srv.Credentials = &api.CredentialService{URL: cfg.Projections.CredentialsURL, ClientID: cfg.IdMS.ClientID, Secret: secret, Client: client}
	}
	if cfg.Console != nil {
		srv.Console = &web.Console{Config: *cfg.Console, TokenURL: strings.TrimSuffix(cfg.IdMS.URL, "/") + "/token", Client: client}
	}
	return srv, nil
}

// ------------------------------------------------------------------------------
// startProjections starts the configured projections (Credential Service, GMS, user database) and returns the
// readiness check: ready once the user database has its file, which it starts from (nil without one).
func startProjections(cfg *serverConfig, st *store.Store, secret string, client *http.Client) func(context.Context) bool {
	if cfg.Projections.CredentialsURL != "" {
		go (&project.SIP{Store: st, Domain: cfg.Domain, URL: cfg.Projections.CredentialsURL, ClientID: cfg.IdMS.ClientID,
			Secret: secret, Client: client}).Run(context.Background(), time.Second)
	}
	if cfg.Projections.GMSURL != "" {
		go (&project.GMS{Store: st, Domain: cfg.Domain, URL: cfg.Projections.GMSURL, ClientID: cfg.IdMS.ClientID,
			Secret: secret, Client: client}).Run(context.Background(), time.Second)
	}
	if cfg.Projections.UserDBFile == "" {
		return nil
	}
	go (&project.UserDB{Store: st, Domain: cfg.Domain, Path: cfg.Projections.UserDBFile}).Run(context.Background(), time.Second)
	return func(ctx context.Context) bool {
		pending, err := st.Pending(ctx, "userdb")
		_, statErr := os.Stat(cfg.Projections.UserDBFile)
		return err == nil && pending == 0 && statErr == nil
	}
}

// ------------------------------------------------------------------------------
// certificatePool is the certificates of a PEM file as a trust pool; a file without one is refused (a wrong path
// or a key file would otherwise leave every TLS connection failing later, with no hint).
func certificatePool(path string) (*x509.CertPool, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(raw) {
		return nil, fmt.Errorf("%s holds no PEM certificate", path)
	}
	return pool, nil
}

// ------------------------------------------------------------------------------
// seed imports a site.json as revision 1; the store must be empty.
func seed(ctx context.Context, st *store.Store, domain, path string) error {
	rev, _, err := st.Current(ctx, domain)
	if err != nil {
		return err
	}
	if rev != 0 {
		return fmt.Errorf("the store already holds revision %d; seeding is for an empty store", rev)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	doc, report, err := config.ImportSite(raw)
	if err != nil {
		return err
	}
	next, _, err := st.Commit(ctx, 0, doc, "import", "seeded from "+path, "import")
	if err != nil {
		return err
	}
	summary, _ := json.Marshal(report)
	log.Printf("seeded revision %d: %s", next, summary)
	return nil
}

// ------------------------------------------------------------------------------
// certificatePin is the SHA-256 of the first certificate in a PEM file, base64url
// without padding: what an activation link pins.
func certificatePin(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	block, _ := pem.Decode(raw)
	if block == nil || block.Type != "CERTIFICATE" {
		return "", errors.New("no PEM certificate")
	}
	sum := sha256.Sum256(block.Bytes)
	return base64.RawURLEncoding.EncodeToString(sum[:]), nil
}

// ------------------------------------------------------------------------------
// clientWithCertificate is an HTTPS client trusting the CA file and presenting this
// service's certificate.
func clientWithCertificate(caFile, certFile, keyFile string) (*http.Client, error) {
	pool := x509.NewCertPool()
	if caFile != "" {
		var err error
		if pool, err = certificatePool(caFile); err != nil {
			return nil, err
		}
	}
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, err
	}
	return &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: pool, Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}}}, nil
}
