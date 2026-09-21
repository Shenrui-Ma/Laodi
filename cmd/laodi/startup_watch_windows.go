package main

import (
	"context"
	"github.com/Shenrui-Ma/Laodi/internal/laodi"
)

func runPlatformStartupWatch(ctx context.Context, state string, args []string) (bool, error) {
	return laodi.RunWindowsStartupSupervisor(ctx, state, args)
}
