//go:build darwin || linux

package background

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestLifecycleLockSerializesAndReleases(t *testing.T) {
	i := Instance{Dir: t.TempDir()}
	unlock, err := i.Lock(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if second, err := i.Lock(ctx); !errors.Is(err, context.DeadlineExceeded) {
		if second != nil {
			second()
		}
		t.Fatalf("second lock: %v", err)
	}
	unlock()
	third, err := i.Lock(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	third()
}
