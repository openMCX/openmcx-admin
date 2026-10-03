// Package config is the configuration model of MCX Admin: the MC user profiles and
// groups of one MCX system, as one versioned document (docs/SLICE_A.md).
//
// Specifications: TS 23.280 V19.10.1 (Rel-19), TS 24.484 V19.6.0 (Rel-19), TS 24.481 V19.3.0 (Rel-19).
package config

import (
	"encoding/json"
	"fmt"
	"os"
)

// SchemaVersion is the version of the document this package reads and writes.
const SchemaVersion = 1

// Document is a whole configuration: what an upload, an export and the importer carry.
type Document struct {
	SchemaVersion int                       `json:"schema_version"`
	System        System                    `json:"system"`
	Templates     map[string]map[string]any `json:"templates,omitempty"`
	Users         []User                    `json:"users"`
	// Groups are the group records the servers read today, unchanged.
	Groups []map[string]any `json:"groups"`
}

// System names the MCX system the document belongs to.
type System struct {
	Domain string `json:"domain"`
}

// User is an MC user: its account, identities (the MC ID and MC service IDs of TS 23.280 clause 8.1.1 and
// clause 8.1.2), groups and profile. It holds no secret.
type User struct {
	Login       string   `json:"login"`
	DisplayName string   `json:"display_name,omitempty"`
	Email       string   `json:"email,omitempty"`
	Status      string   `json:"status"`
	Template    string   `json:"template,omitempty"`
	MCID        string   `json:"mc_id"`
	MCPTTID     string   `json:"mcptt_id"`
	SIPURI      string   `json:"sip_uri"`
	ClientIDs   []string `json:"client_ids"`
	MCDataID    string   `json:"mcdata_id,omitempty"`
	MCVideoID   string   `json:"mcvideo_id,omitempty"`
	Groups      []string `json:"groups"`
	// Profile holds the rights sections the servers read (private_call, adhoc_call,
	// mcdata, mcvideo, location, ...), over the template's.
	Profile map[string]any `json:"profile,omitempty"`
}

// Statuses of an account.
var Statuses = map[string]bool{"pending": true, "active": true, "suspended": true, "retired": true}

// ------------------------------------------------------------------------------
// EffectiveProfile is the user's template with its own profile merged over it: the rights the servers read and
// the CMS renders in the user profile documents (TS 24.484 clause 8.3, clause 9.3 and clause 10.3). Templates
// are openMCX's own.
func (doc *Document) EffectiveProfile(user User) map[string]any {
	base := map[string]any{}
	if user.Template != "" {
		base = clone(doc.Templates[user.Template])
	}
	return merge(base, clone(user.Profile))
}

// ------------------------------------------------------------------------------
// merge puts over onto base: objects merge key by key, other values replace.
func merge(base, over map[string]any) map[string]any {
	if base == nil {
		base = map[string]any{}
	}
	for key, value := range over {
		if vo, ok := value.(map[string]any); ok {
			if bo, ok := base[key].(map[string]any); ok {
				base[key] = merge(bo, vo)
				continue
			}
		}
		base[key] = value
	}
	return base
}

// ------------------------------------------------------------------------------
// clone deep-copies a JSON value.
func clone[Value any](value Value) Value {
	raw, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	var out Value
	if err := json.Unmarshal(raw, &out); err != nil {
		panic(err)
	}
	return out
}

// ------------------------------------------------------------------------------
// Load reads a document from a file.
func Load(path string) (*Document, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(raw)
}

// ------------------------------------------------------------------------------
// Parse reads a document, refusing unknown fields and other schema versions.
func Parse(raw []byte) (*Document, error) {
	var doc Document
	dec := json.NewDecoder(bytesReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("configuration document: %w", err)
	}
	if doc.SchemaVersion != SchemaVersion {
		return nil, fmt.Errorf("configuration document: schema_version %d, expected %d", doc.SchemaVersion, SchemaVersion)
	}
	return &doc, nil
}
