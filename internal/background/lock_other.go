//go:build !darwin && !linux

package background

import (
	"context"
	"fmt"
)

func (i Instance) Lock(context.Context) (func(), error) {
	return nil, fmt.Errorf("background service mode requires macOS launchd or Linux systemd --user")
}
