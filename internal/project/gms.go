//
// OpenMCX Licence Information : AGPL-3.0-or-later
//

// The GMS projection: each revision's groups written to the GMS, and the group changes users made there by XCAP
// taken back in.
//
// Specifications: TS 24.481 V19.3.0 (Rel-19).
package project

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/openMCX/openmcx-admin/internal/closer"
	"github.com/openMCX/openmcx-admin/internal/config"
	"github.com/openMCX/openmcx-admin/internal/store"
)

// GMS writes the groups of each revision to the GMS (slice D2), through its group
// provisioning interface (openmcx-server): the GMS renders and commits them like
// an XCAP write, so the servers and clients learn a change as they learn any other.
// The first run writes every group, which leaves the ones the GMS already has as
// they are; later runs write the groups changed since the last applied revision and
// delete the ones removed. A group only the GMS has (made by a user's XCAP write) is
// never deleted here: slice D3 imports it.
type GMS struct {
	Store    *store.Store
	Domain   string
	URL      string // the GMS
	ClientID string // the server client credentials it accepts
	Secret   string
	Client   *http.Client
}

const gmsConsumer = "gms"

// ------------------------------------------------------------------------------
// groupKey is a group's ID, the key the GMS knows it by.
func groupKey(group map[string]any) string {
	id, _ := group["id"].(string)
	return id
}

// ------------------------------------------------------------------------------
// Once applies the latest pending revision, if any.
func (gms *GMS) Once(ctx context.Context) error {
	pending, err := gms.Store.Pending(ctx, gmsConsumer)
	if err != nil || pending == 0 {
		return err
	}
	last, err := gms.Store.LastApplied(ctx, gmsConsumer)
	if err != nil {
		return err
	}
	if last == 0 {
		// The first run: groups users already changed at the GMS (stored documents a
		// seed from site.json does not have) are taken in first, not overwritten.
		if err := gms.importChanges(ctx, false); err != nil {
			return err
		}
	}
	rev, doc, err := gms.Store.Current(ctx, gms.Domain)
	if err != nil {
		return err
	}
	before := map[string][]byte{}
	if last > 0 {
		old, err := gms.Store.Document(ctx, last)
		if err != nil {
			return err
		}
		for _, group := range old.Groups {
			before[groupKey(group)], _ = json.Marshal(group)
		}
	}
	fail := func(err error) error {
		recordFailure(ctx, gms.Store, gmsConsumer, rev, err)
		return err
	}
	now := map[string]bool{}
	for _, group := range doc.Groups {
		id := groupKey(group)
		now[id] = true
		raw, _ := json.Marshal(group)
		if prev, known := before[id]; last > 0 && known && bytes.Equal(prev, raw) {
			continue
		}
		if err := gms.send(ctx, http.MethodPut, id, raw); err != nil {
			return fail(err)
		}
	}
	for id := range before {
		if !now[id] {
			if err := gms.send(ctx, http.MethodDelete, id, nil); err != nil {
				return fail(err)
			}
		}
	}
	return gms.Store.Applied(ctx, gmsConsumer, rev)
}

// ------------------------------------------------------------------------------
// send writes or deletes a group and records the GMS version it left, so that the
// importer tells MCX Admin's own writes from a user's.
func (gms *GMS) send(ctx context.Context, method, id string, body []byte) error {
	target := strings.TrimSuffix(gms.URL, "/") + "/internal/groups?" + url.Values{"id": {id}}.Encode()
	req, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth(gms.ClientID, gms.Secret)
	resp, err := gms.client().Do(req)
	if err != nil {
		return err
	}
	defer closer.Quietly("response body", resp.Body)
	if resp.StatusCode != http.StatusOK {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("GMS: %s %s: status %d: %s", method, id, resp.StatusCode, strings.TrimSpace(string(detail)))
	}
	var result struct {
		Version *int64 `json:"version"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&result); err == nil && result.Version != nil {
		return gms.Store.SetGMSVersion(ctx, id, *result.Version)
	}
	return nil
}

// ------------------------------------------------------------------------------
// client is the configured HTTP client, or one with a 10 s timeout.
func (gms *GMS) client() *http.Client {
	if gms.Client != nil {
		return gms.Client
	}
	return &http.Client{Timeout: 10 * time.Second}
}

// ------------------------------------------------------------------------------
// Import takes into MCX Admin the group changes made at the GMS by users' XCAP writes
// (TS 24.481 clause 6.3.2 to clause 6.3.12; the 3GPP conformance guard, rule 3): a group whose GMS
// version is not the one MCX Admin's last write left becomes part of a revision with
// source "xcap", and the users' group lists follow its membership. It runs only when
// every revision has reached the GMS, so it never undoes an administrator's change
// still on its way; a concurrent administrator's revision makes it try again later.
func (gms *GMS) Import(ctx context.Context) error { return gms.importChanges(ctx, true) }

// listedGroup is one group of the GMS's listing: its ID, version, and document unless deleted.
type listedGroup struct {
	ID      string         `json:"id"`
	Version int64          `json:"version"`
	Deleted bool           `json:"deleted"`
	Group   map[string]any `json:"group"`
}

// ------------------------------------------------------------------------------
// listGroups is the GMS's listing of every group with its version (its /internal/groups, with the server client
// credentials).
func (gms *GMS) listGroups(ctx context.Context) ([]listedGroup, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(gms.URL, "/")+"/internal/groups", nil)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(gms.ClientID, gms.Secret)
	resp, err := gms.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer closer.Quietly("response body", resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GMS: list groups: status %d", resp.StatusCode)
	}
	var listing struct {
		Groups []listedGroup `json:"groups"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<20)).Decode(&listing); err != nil {
		return nil, err
	}
	return listing.Groups, nil
}

// ------------------------------------------------------------------------------
// mergeListing applies the listed groups whose version differs from the one recorded (known) to doc: a deleted
// group removed, a changed one replaced, a new one added. It returns the versions seen, to record, and the IDs of
// the groups changed.
func mergeListing(doc *config.Document, known map[string]int64, listing []listedGroup) (map[string]int64, []string) {
	index := map[string]int{}
	for position, group := range doc.Groups {
		index[groupKey(group)] = position
	}
	seen := map[string]int64{}
	var changed []string
	for _, listed := range listing {
		version, recorded := known[listed.ID]
		if recorded && version == listed.Version {
			continue
		}
		position, managed := index[listed.ID]
		if !recorded && listed.Version == 0 {
			if managed {
				seen[listed.ID] = 0 // a configured group never changed, as MCX Admin has it
			}
			continue
		}
		switch {
		case listed.Deleted && managed:
			doc.Groups = append(doc.Groups[:position:position], doc.Groups[position+1:]...)
			for id, other := range index {
				if other > position {
					index[id] = other - 1
				}
			}
			delete(index, listed.ID)
		case listed.Deleted:
		case managed:
			doc.Groups[position] = listed.Group
		default:
			doc.Groups = append(doc.Groups, listed.Group)
			index[listed.ID] = len(doc.Groups) - 1
		}
		seen[listed.ID] = listed.Version
		changed = append(changed, listed.ID)
	}
	return seen, changed
}

// ------------------------------------------------------------------------------
// importChanges takes in the groups whose GMS version differs from the one MCX Admin's last write left (see
// Import). With idle, it runs only when the GMS has every revision; the first run of Once calls it without, before
// the first write. A group deleted at the GMS is removed, a changed or new one replaced or added, the users' group
// lists follow, and the versions seen are recorded; a revision that does not validate is refused, audited and not
// tried again.
func (gms *GMS) importChanges(ctx context.Context, idle bool) error {
	if pending, err := gms.Store.Pending(ctx, gmsConsumer); idle && (err != nil || pending > 0) {
		return err
	}
	listing, err := gms.listGroups(ctx)
	if err != nil {
		return err
	}
	known, err := gms.Store.GMSVersions(ctx)
	if err != nil {
		return err
	}
	rev, doc, err := gms.Store.Current(ctx, gms.Domain)
	if err != nil {
		return err
	}
	before := doc.Clone()
	seen, changed := mergeListing(doc, known, listing)
	record := func() error {
		for id, version := range seen {
			if err := gms.Store.SetGMSVersion(ctx, id, version); err != nil {
				return err
			}
		}
		return nil
	}
	if len(changed) == 0 {
		return record()
	}
	doc.FollowMembership(before)
	sort.Strings(changed)
	next, _, err := gms.Store.Commit(ctx, rev, doc, "xcap", "group changes at the GMS: "+strings.Join(changed, ", "), "xcap")
	var invalid *store.ErrInvalid
	switch {
	case errors.Is(err, store.ErrStale):
		return nil // an administrator's revision came first; the next poll sees both
	case errors.As(err, &invalid):
		// Recorded, so that it is not tried again; the audit tells the administrators.
		detail, _ := json.Marshal(map[string]any{"groups": changed, "problems": invalid.Problems})
		if err := gms.Store.Audit(ctx, "xcap", "xcap-import-refused", strings.Join(changed, ", "), string(detail)); err != nil {
			log.Printf("gms import: audit of the refused revision not recorded: %v", err)
		}
		return record()
	case err != nil:
		return err
	}
	log.Printf("gms import: revision %d from XCAP changes of %s", next, strings.Join(changed, ", "))
	return record()
}

// ------------------------------------------------------------------------------
// Run applies pending revisions every interval until the context ends.
func (gms *GMS) Run(ctx context.Context, interval time.Duration) {
	for {
		if err := gms.Once(ctx); err != nil {
			log.Printf("gms projection: %v", err)
		} else if err := gms.Import(ctx); err != nil {
			log.Printf("gms import: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}
