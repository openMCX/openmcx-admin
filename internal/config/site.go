// The site.json bridge: a site.json imported as a configuration document, and a document given back as the
// users and groups the servers read from site.json.
//
// openMCX administration (no 3GPP specification).
package config

import (
	"encoding/json"
	"fmt"
	"os"
)

// The fields of a site.json user that are not part of the profile.
var (
	// Secrets: removed on import, taken back from the base site.json on export.
	siteSecretFields = []string{"password", "sip_password"}
	// Identities, mapped to User fields.
	siteIdentityFields = map[string]bool{"username": true, "mc_id": true, "mcptt_id": true, "sip_uri": true,
		"client_id": true, "groups": true, "mcdata_id": true, "mcvideo_id": true}
)

// ImportReport says what the importer removed and kept aside.
type ImportReport struct {
	Users             int      `json:"users"`
	Groups            int      `json:"groups"`
	SecretsRemoved    []string `json:"secrets_removed"`     // "alice.password"
	SystemNotImported []string `json:"system_not_imported"` // site-level keys outside the model
}

// ------------------------------------------------------------------------------
// ImportSite turns a site.json into a configuration document. Its users lose their
// secrets; everything else a server
// reads is kept as it is. An optional top-level "templates" object (name -> profile
// sections, as the console's templates) becomes the document's templates, so that a new
// system can start with ready-made sets of rights; the servers do not read it.
func ImportSite(raw []byte) (*Document, ImportReport, error) {
	var site map[string]any
	if err := json.Unmarshal(raw, &site); err != nil {
		return nil, ImportReport{}, fmt.Errorf("site.json: %w", err)
	}
	var report ImportReport
	domain, _ := site["domain"].(string)
	doc := &Document{SchemaVersion: SchemaVersion, System: System{Domain: domain}, Users: []User{}, Groups: []map[string]any{}}
	users, _ := site["users"].([]any)
	for _, item := range users {
		src, ok := item.(map[string]any)
		if !ok {
			return nil, report, fmt.Errorf("site.json: a user is not an object")
		}
		user := importUser(clone(src), doc, &report)
		doc.Users = append(doc.Users, user)
	}
	groups, _ := site["groups"].([]any)
	for _, item := range groups {
		group, ok := item.(map[string]any)
		if !ok {
			return nil, report, fmt.Errorf("site.json: a group is not an object")
		}
		doc.Groups = append(doc.Groups, clone(group))
	}
	migrateRights(doc)
	if err := importTemplates(site["templates"], doc); err != nil {
		return nil, report, err
	}
	for _, key := range sortedKeys(site) {
		if key != "users" && key != "groups" && key != "domain" && key != "templates" {
			report.SystemNotImported = append(report.SystemNotImported, key)
		}
	}
	report.Users, report.Groups = len(doc.Users), len(doc.Groups)
	return doc, report, nil
}

// ------------------------------------------------------------------------------
// importTemplates takes the site.json "templates" object, if any: each template a name with an object of profile
// sections (the same form as PUT /v1/templates/{name}).
func importTemplates(value any, doc *Document) error {
	if value == nil {
		return nil
	}
	templates, ok := value.(map[string]any)
	if !ok {
		return fmt.Errorf("site.json: templates is not an object")
	}
	for _, name := range sortedKeys(templates) {
		profile, ok := templates[name].(map[string]any)
		if !ok {
			return fmt.Errorf("site.json: template %q is not an object", name)
		}
		if doc.Templates == nil {
			doc.Templates = map[string]map[string]any{}
		}
		doc.Templates[name] = clone(profile)
	}
	return nil
}

// ------------------------------------------------------------------------------
// importUser turns one site.json user (a copy, changed here) into a document user: its secrets removed and
// reported, its identities taken, and the rest
// kept as its profile.
func importUser(src map[string]any, doc *Document, report *ImportReport) User {
	login, _ := src["username"].(string)
	for _, key := range siteSecretFields {
		if _, ok := src[key]; ok {
			report.SecretsRemoved = append(report.SecretsRemoved, login+"."+key)
			delete(src, key)
		}
	}
	str := func(key string) string { text, _ := src[key].(string); return text }
	user := User{Login: login, Status: "active", MCID: str("mc_id"), MCPTTID: str("mcptt_id"), SIPURI: str("sip_uri"),
		MCDataID: str("mcdata_id"), MCVideoID: str("mcvideo_id"), ClientIDs: []string{}, Groups: stringList(src["groups"])}
	if clientID := str("client_id"); clientID != "" {
		user.ClientIDs = []string{clientID}
	}
	profile := map[string]any{}
	for key, value := range src {
		if !siteIdentityFields[key] {
			profile[key] = value
		}
	}
	if len(profile) > 0 {
		user.Profile = profile
	}
	return user
}

// ------------------------------------------------------------------------------
// ToSiteUsers gives the users and groups of the document as a site.json holds them,
// with each user's secrets taken from base, the site.json the system runs with.
// For an imported document it gives back base's users and groups (the golden check).
func (doc *Document) ToSiteUsers(base []byte) (users, groups []map[string]any, err error) {
	var site map[string]any
	if err := json.Unmarshal(base, &site); err != nil {
		return nil, nil, fmt.Errorf("base site.json: %w", err)
	}
	secrets := map[string]map[string]any{}
	baseUsers, _ := site["users"].([]any)
	for _, item := range baseUsers {
		src, _ := item.(map[string]any)
		login, _ := src["username"].(string)
		secrets[login] = map[string]any{}
		for _, key := range siteSecretFields {
			if value, ok := src[key]; ok {
				secrets[login][key] = value
			}
		}
	}
	for _, user := range doc.Users {
		out := doc.SiteUser(user)
		for key, value := range secrets[user.Login] {
			out[key] = value
		}
		users = append(users, clone(out))
	}
	for _, group := range doc.Groups {
		groups = append(groups, clone(group))
	}
	return users, groups, nil
}

// ------------------------------------------------------------------------------
// stringsAny is a list of strings as a JSON list.
func stringsAny(in []string) []any {
	out := make([]any, len(in))
	for index, text := range in {
		out[index] = text
	}
	return out
}

// ------------------------------------------------------------------------------
// ReadFile is os.ReadFile, kept here so that the command reads through one place.
func ReadFile(path string) ([]byte, error) { return os.ReadFile(path) }

// ------------------------------------------------------------------------------
// SiteUser is a user as the servers read it from site.json (its identities, its
// effective profile), without any secret.
func (doc *Document) SiteUser(user User) map[string]any {
	out := doc.EffectiveProfile(user)
	out["username"] = user.Login
	out["mc_id"], out["mcptt_id"], out["sip_uri"] = user.MCID, user.MCPTTID, user.SIPURI
	if len(user.ClientIDs) > 0 {
		out["client_id"] = user.ClientIDs[0]
	}
	out["groups"] = stringsAny(user.Groups)
	if user.MCDataID != "" {
		out["mcdata_id"] = user.MCDataID
	}
	if user.MCVideoID != "" {
		out["mcvideo_id"] = user.MCVideoID
	}
	return clone(out)
}

// ------------------------------------------------------------------------------
// migrateRights keeps the meaning of a site.json written before the servers applied TS 24.379 and TS 24.281
// clause 6.3.3.1.13: there, a group's priority policy lists alone decided who may raise or cancel an emergency group
// call, an imminent peril call or an emergency alert. Now the user profile's right is needed too, so every user a
// list names gets the matching right (group_call: emergency, imminent, alert and their cancellations; in mcvideo for
// the MCVideo lists). An MCData group without transmit_members let every member transmit; it gets transmit_all, its
// <mcdata-allow-transmit-data-in-this-group> for all members (TS 24.481 clause 7.2.4.2: absent means no one may).
func migrateRights(doc *Document) {
	for _, group := range doc.Groups {
		video, _ := group["mcvideo"].(map[string]any)
		for key, right := range migratedRights {
			for _, id := range policyList(group["priority_policy"], key) {
				grantRight(doc, func(user *User) bool { return user.MCPTTID == id }, "", right)
			}
			if video != nil {
				for _, id := range policyList(video["priority_policy"], key) {
					grantRight(doc, func(user *User) bool { return user.MCVideoID == id }, "mcvideo", right)
				}
			}
		}
		migrateTransmit(group)
	}
	for index := range doc.Users {
		migrateParticipation(doc.Users[index].Profile)
		if video, ok := doc.Users[index].Profile["mcvideo"].(map[string]any); ok {
			migrateParticipation(video)
		}
	}
}

// ------------------------------------------------------------------------------
// migrateParticipation: a user with ad hoc call rights may take part in ad hoc group calls, as before TS 24.379
// clause 17.4.2.2.1 step 6 (<allow-adhoc-group-call-participation>) was applied.
func migrateParticipation(profile map[string]any) {
	if adhoc, ok := profile["adhoc_call"].(map[string]any); ok {
		if _, set := adhoc["participation"]; !set {
			adhoc["participation"] = true
		}
	}
}

// ------------------------------------------------------------------------------
// migrateTransmit: an MCData group without transmit_members gets transmit_all.
func migrateTransmit(group map[string]any) {
	data, _ := group["mcdata"].(map[string]any)
	if data == nil {
		return
	}
	_, listed := data["transmit_members"]
	_, set := data["transmit_all"]
	if !listed && !set {
		data["transmit_all"] = true
	}
}

// migratedRights maps a priority policy list to the group_call right of the users it names.
var migratedRights = map[string]string{"emergency_users": "emergency", "imminent_users": "imminent",
	"imminent_cancel_users": "imminent_cancel", "alert_users": "alert", "alert_cancel_users": "alert_cancel",
	"emergency_cancel_users": "emergency_cancel"}

// ------------------------------------------------------------------------------
// policyList is the identities a priority policy lists under a key.
func policyList(policy any, key string) []string {
	table, _ := policy.(map[string]any)
	items, _ := table[key].([]any)
	var out []string
	for _, item := range items {
		if id, ok := item.(string); ok {
			out = append(out, id)
		}
	}
	return out
}

// ------------------------------------------------------------------------------
// grantRight sets a group_call right in the profile (or its section, such as "mcvideo") of the users that match.
func grantRight(doc *Document, match func(*User) bool, section, right string) {
	for index := range doc.Users {
		user := &doc.Users[index]
		if !match(user) {
			continue
		}
		if user.Profile == nil {
			user.Profile = map[string]any{}
		}
		call := subsection(subsection(user.Profile, section), "group_call")
		call[right] = true
	}
}

// ------------------------------------------------------------------------------
// subsection is a profile's object under a key, created when absent; the profile itself for an empty key.
func subsection(profile map[string]any, key string) map[string]any {
	if key == "" {
		return profile
	}
	inner, _ := profile[key].(map[string]any)
	if inner == nil {
		inner = map[string]any{}
		profile[key] = inner
	}
	return inner
}
