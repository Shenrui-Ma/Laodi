//go:build !windows

package laodi

func planWindowsHookConfig(options HookConfigOptions) (HookConfigPlan, error) {
	return HookConfigPlan{}, ErrWindowsHookContractUnverified
}

func DetectWindowsHookContract(adapter, executable string) (WindowsHookContract, error) {
	return WindowsHookContract{}, ErrWindowsHookContractUnverified
}

func verifyHookInstallState(plan HookConfigPlan) error { return nil }
