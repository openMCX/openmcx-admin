// The dashboard's live status: service probes, accounts, change delivery, pending activations and recent
// activity.
//
// openMCX administration (no 3GPP specification).
package api

import (
	"context"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/openMCX/openmcx-admin/internal/closer"
)

// Probe is one service the dashboard shows (live status): an HTTPS
// health URL, or a TCP address for a service without one (the SIP core).
type Probe struct {
	Name string `json:"name"`
	URL  string `json:"url,omitempty"`
	TCP  string `json:"tcp,omitempty"`
}

type probeResult struct {
	Name      string `json:"name"`
	Up        bool   `json:"up"`
	LatencyMS int64  `json:"latency_ms"`
	Detail    string `json:"detail,omitempty"`
}

// ------------------------------------------------------------------------------
// probe checks one service within 3 s: a TCP connection, or an HTTPS GET of its health URL that must answer 200; it
// reports the latency either way.
func (server *Server) probe(ctx context.Context, probe Probe) probeResult {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	start := time.Now()
	out := probeResult{Name: probe.Name}
	if probe.TCP != "" {
		conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", probe.TCP)
		if err != nil {
			out.Detail = "unreachable"
		} else {
			closer.Quietly("probe connection", conn)
			out.Up = true
		}
	} else {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, probe.URL, nil)
		client := server.ProbeClient
		if client == nil {
			client = http.DefaultClient
		}
		if err == nil {
			var resp *http.Response
			if resp, err = client.Do(req); err == nil {
				closer.Quietly("probe answer", resp.Body)
				out.Up = resp.StatusCode == http.StatusOK
				if !out.Up {
					out.Detail = resp.Status
				}
			}
		}
		if err != nil {
			out.Detail = "unreachable"
		}
	}
	out.LatencyMS = time.Since(start).Milliseconds()
	return out
}

// ------------------------------------------------------------------------------
// status is the dashboard's live view: accounts, revision, change delivery,
// pending activations, the services' health and the latest activity.
func (server *Server) status(writer http.ResponseWriter, request *http.Request, _ string) {
	rev, doc, err := server.Store.Current(request.Context(), server.Domain)
	if err != nil {
		fail(writer, http.StatusInternalServerError, err.Error(), nil)
		return
	}
	users := map[string]int{"active": 0, "pending": 0, "suspended": 0, "retired": 0}
	for _, user := range doc.Users {
		users[user.Status]++
	}
	outbox, err := server.Store.Outbox(request.Context())
	if err != nil {
		fail(writer, http.StatusInternalServerError, err.Error(), nil)
		return
	}
	delivery := []map[string]any{}
	for _, consumer := range server.Store.Consumers {
		applied, err := server.Store.LastApplied(request.Context(), consumer)
		if err != nil {
			fail(writer, http.StatusInternalServerError, err.Error(), nil)
			return
		}
		counts := outbox[consumer]
		delivery = append(delivery, map[string]any{"consumer": consumer, "applied_revision": applied,
			"pending": counts["pending"], "failed": counts["failed"]})
	}
	pending, err := server.Store.PendingActivations(request.Context())
	if err != nil {
		fail(writer, http.StatusInternalServerError, err.Error(), nil)
		return
	}
	activity, err := server.Store.RecentAudit(request.Context(), 10)
	if err != nil {
		fail(writer, http.StatusInternalServerError, err.Error(), nil)
		return
	}
	services := make([]probeResult, len(server.Probes))
	var wg sync.WaitGroup
	for index, probe := range server.Probes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			services[index] = server.probe(request.Context(), probe)
		}()
	}
	wg.Wait()
	writer.Header().Set("ETag", etag(rev))
	writer.Header().Set("Cache-Control", "no-store")
	writeJSON(writer, http.StatusOK, map[string]any{"domain": doc.System.Domain, "revision": rev, "users": users,
		"pending_activations": pending, "delivery": delivery, "services": services, "activity": activity,
		"checked_at": time.Now().UTC()})
}

// ------------------------------------------------------------------------------
// me names the signed-in administrator.
func (server *Server) me(writer http.ResponseWriter, request *http.Request, login string) {
	_, doc, err := server.Store.Current(request.Context(), server.Domain)
	if err != nil {
		fail(writer, http.StatusInternalServerError, err.Error(), nil)
		return
	}
	for _, user := range doc.Users {
		if user.Login == login {
			writeJSON(writer, http.StatusOK, map[string]any{"login": user.Login, "display_name": user.DisplayName,
				"mcptt_id": user.MCPTTID, "domain": doc.System.Domain, "console": server.Console != nil,
				"activation": server.Activation != nil})
			return
		}
	}
	fail(writer, http.StatusNotFound, "no such user", nil)
}
