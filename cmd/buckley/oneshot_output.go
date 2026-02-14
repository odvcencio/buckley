package main

const oneshotMinimalOutputEnv = "BUCKLEY_ONESHOT_MIN_OUTPUT"

func oneshotMinimalOutputEnabled() bool {
	if cliFlags.quiet {
		return true
	}
	if enabled, ok := parseBoolEnv(oneshotMinimalOutputEnv); ok {
		return enabled
	}
	return false
}
