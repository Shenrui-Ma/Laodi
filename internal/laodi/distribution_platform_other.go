//go:build !windows

package laodi

import (
	"errors"
	"os"
)

func readWindowsSource(*os.Root, string) ([]byte, error) { return nil, errors.New("Windows only") }

func planWindowsDistribution(DistributionOptions) (DistributionPlan, error) {
	return DistributionPlan{}, errors.New("Windows only")
}
func installWindowsDistribution(DistributionPlan) (DistributionResult, error) {
	return DistributionResult{}, errors.New("Windows only")
}
func uninstallWindowsDistribution(DistributionPlan) (DistributionResult, error) {
	return DistributionResult{}, errors.New("Windows only")
}
