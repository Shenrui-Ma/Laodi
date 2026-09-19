//go:build !windows

package laodi

import "context"

func WatchContext(ctx context.Context, stateDir string) (context.Context, context.CancelFunc, error) {
	ctx, cancel := context.WithCancel(ctx)
	return ctx, cancel, nil
}
