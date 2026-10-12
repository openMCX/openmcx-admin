//
// OpenMCX Licence Information : AGPL-3.0-or-later
//

package store_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/openMCX/openmcx-admin/internal/store/storetest"
)

// ------------------------------------------------------------------------------
// TestConcurrentActivations: activations issued at once for many users, and again for the same users, all succeed
// (InnoDB deadlocks between them are retried), and each user keeps one
// usable code.
func TestConcurrentActivations(test *testing.T) {
	st, _ := storetest.Open(test)
	ctx := context.Background()
	expires := time.Now().Add(time.Hour)
	var wait sync.WaitGroup
	errs := make(chan error, 80)
	for round := 0; round < 2; round++ {
		for user := 0; user < 40; user++ {
			wait.Add(1)
			go func(round, user int) {
				defer wait.Done()
				login := fmt.Sprintf("u%03d", user)
				errs <- st.NewActivation(ctx, fmt.Sprintf("code-%d-%d", round, user), login, "admin", expires)
			}(round, user)
		}
	}
	wait.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			test.Fatalf("a concurrent activation failed: %v", err)
		}
	}
	if pending, err := st.PendingActivations(ctx); err != nil || pending != 40 {
		test.Fatalf("usable codes %d (%v), want one per user", pending, err)
	}
}
