//
// OpenMCX Licence Information : AGPL-3.0-or-later
//

// Small helpers of the configuration model: a byte reader and the sorted keys of a map.
//
// OpenMCX administration (no 3GPP specification).
package config

import (
	"bytes"
	"io"
	"sort"
)

// ------------------------------------------------------------------------------
// bytesReader reads a byte slice.
func bytesReader(data []byte) io.Reader { return bytes.NewReader(data) }

// ------------------------------------------------------------------------------
// sortedKeys is the keys of a map in sorted order, for a stable output.
func sortedKeys[Value any](items map[string]Value) []string {
	keys := make([]string, 0, len(items))
	for key := range items {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
