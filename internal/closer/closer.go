// Package closer closes deferred resources whose close error can no longer change a result.
//
// openMCX administration (no 3GPP specification).
package closer

import (
	"io"
	"log"
)

// ------------------------------------------------------------------------------
// Quietly closes a resource after its user has read what it needs (an HTTP body, query rows whose Err was
// checked, a file only read); a close error is logged with what names the resource, not lost.
func Quietly(what string, resource io.Closer) {
	if err := resource.Close(); err != nil {
		log.Printf("%s: close: %v", what, err)
	}
}
