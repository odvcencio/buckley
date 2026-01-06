package builtin

import (
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDelegationGuard_DepthTracking(t *testing.T) {
	// Save and restore env
	origDepth := os.Getenv(DelegationDepthEnvVar)
	defer os.Setenv(DelegationDepthEnvVar, origDepth)

	guard := &DelegationGuard{
		delegationTimes: make([]time.Time, 0),
		lastDelegation:  make(map[string]time.Time),
	}

	// Test initial depth is 0
	os.Unsetenv(DelegationDepthEnvVar)
	assert.Equal(t, 0, guard.GetCurrentDepth())

	// Test reading depth from env
	os.Setenv(DelegationDepthEnvVar, "2")
	assert.Equal(t, 2, guard.GetCurrentDepth())

	// Test invalid depth defaults to 0
	os.Setenv(DelegationDepthEnvVar, "invalid")
	assert.Equal(t, 0, guard.GetCurrentDepth())
}

func TestDelegationGuard_DepthLimit(t *testing.T) {
	origDepth := os.Getenv(DelegationDepthEnvVar)
	defer os.Setenv(DelegationDepthEnvVar, origDepth)

	guard := &DelegationGuard{
		delegationTimes: make([]time.Time, 0),
		lastDelegation:  make(map[string]time.Time),
	}

	// At depth 0, should allow delegation
	os.Unsetenv(DelegationDepthEnvVar)
	err := guard.CanDelegate("invoke_codex")
	assert.NoError(t, err)

	// At max depth, should block
	os.Setenv(DelegationDepthEnvVar, "3")
	err = guard.CanDelegate("invoke_codex")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "depth limit exceeded")
}

func TestDelegationGuard_RateLimit(t *testing.T) {
	origDepth := os.Getenv(DelegationDepthEnvVar)
	defer os.Setenv(DelegationDepthEnvVar, origDepth)
	os.Unsetenv(DelegationDepthEnvVar)

	guard := &DelegationGuard{
		delegationTimes: make([]time.Time, 0),
		lastDelegation:  make(map[string]time.Time),
	}

	// Fill up rate limit
	now := time.Now()
	for i := 0; i < MaxDelegationsPerWindow; i++ {
		guard.delegationTimes = append(guard.delegationTimes, now)
	}

	// Should be rate limited
	err := guard.CanDelegate("invoke_codex")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "rate limit exceeded")
}

func TestDelegationGuard_Cooldown(t *testing.T) {
	origDepth := os.Getenv(DelegationDepthEnvVar)
	defer os.Setenv(DelegationDepthEnvVar, origDepth)
	os.Unsetenv(DelegationDepthEnvVar)

	guard := &DelegationGuard{
		delegationTimes: make([]time.Time, 0),
		lastDelegation:  make(map[string]time.Time),
	}

	// Record a recent delegation
	guard.lastDelegation["invoke_codex"] = time.Now()

	// Same tool should be on cooldown
	err := guard.CanDelegate("invoke_codex")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "cooldown active")

	// Different tool should be fine
	err = guard.CanDelegate("invoke_claude")
	assert.NoError(t, err)
}

func TestDelegationGuard_PrepareChildEnv(t *testing.T) {
	origDepth := os.Getenv(DelegationDepthEnvVar)
	defer os.Setenv(DelegationDepthEnvVar, origDepth)

	guard := &DelegationGuard{
		delegationTimes: make([]time.Time, 0),
		lastDelegation:  make(map[string]time.Time),
	}

	// Test increment from 0
	os.Unsetenv(DelegationDepthEnvVar)
	env := guard.PrepareChildEnv()
	found := false
	for _, e := range env {
		if e == DelegationDepthEnvVar+"=1" {
			found = true
			break
		}
	}
	assert.True(t, found, "Expected depth=1 in child env")

	// Test increment from 2
	os.Setenv(DelegationDepthEnvVar, "2")
	env = guard.PrepareChildEnv()
	found = false
	for _, e := range env {
		if e == DelegationDepthEnvVar+"=3" {
			found = true
			break
		}
	}
	assert.True(t, found, "Expected depth=3 in child env")
}

func TestDelegationGuard_SelfDelegation(t *testing.T) {
	origDepth := os.Getenv(DelegationDepthEnvVar)
	defer os.Setenv(DelegationDepthEnvVar, origDepth)

	guard := &DelegationGuard{
		delegationTimes: make([]time.Time, 0),
		lastDelegation:  make(map[string]time.Time),
	}

	// At depth 0, not self-delegation
	os.Unsetenv(DelegationDepthEnvVar)
	assert.False(t, guard.IsSelfDelegation("invoke_buckley"))

	// At depth 1, is self-delegation
	os.Setenv(DelegationDepthEnvVar, "1")
	assert.True(t, guard.IsSelfDelegation("invoke_buckley"))

	// invoke_codex is never self-delegation
	assert.False(t, guard.IsSelfDelegation("invoke_codex"))
}

func TestDelegationGuard_CheckAndRecord(t *testing.T) {
	origDepth := os.Getenv(DelegationDepthEnvVar)
	defer os.Setenv(DelegationDepthEnvVar, origDepth)
	os.Unsetenv(DelegationDepthEnvVar)

	guard := &DelegationGuard{
		delegationTimes: make([]time.Time, 0),
		lastDelegation:  make(map[string]time.Time),
	}

	// First call should succeed and record
	err := guard.CheckAndRecord("invoke_codex")
	assert.NoError(t, err)
	assert.Len(t, guard.delegationTimes, 1)
	assert.Contains(t, guard.lastDelegation, "invoke_codex")

	// Second immediate call to same tool should be on cooldown
	err = guard.CheckAndRecord("invoke_codex")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "cooldown")
}

func TestDelegationGuard_Stats(t *testing.T) {
	origDepth := os.Getenv(DelegationDepthEnvVar)
	defer os.Setenv(DelegationDepthEnvVar, origDepth)
	os.Setenv(DelegationDepthEnvVar, "1")

	guard := &DelegationGuard{
		delegationTimes: make([]time.Time, 0),
		lastDelegation:  make(map[string]time.Time),
	}

	stats := guard.DelegationStats()
	require.NotNil(t, stats)

	assert.Equal(t, 1, stats["current_depth"])
	assert.Equal(t, MaxDelegationDepth, stats["max_depth"])
	assert.Equal(t, 0, stats["delegations_in_window"])
	assert.Equal(t, MaxDelegationsPerWindow, stats["max_delegations"])
	assert.Equal(t, true, stats["is_delegated_context"])
	assert.Equal(t, true, stats["can_delegate"])
}

func TestDelegationGuard_OldEntriesCleanup(t *testing.T) {
	origDepth := os.Getenv(DelegationDepthEnvVar)
	defer os.Setenv(DelegationDepthEnvVar, origDepth)
	os.Unsetenv(DelegationDepthEnvVar)

	guard := &DelegationGuard{
		delegationTimes: make([]time.Time, 0),
		lastDelegation:  make(map[string]time.Time),
	}

	// Add old entries that should be cleaned up
	oldTime := time.Now().Add(-2 * DelegationRateWindow)
	for i := 0; i < 5; i++ {
		guard.delegationTimes = append(guard.delegationTimes, oldTime)
	}

	// CanDelegate should clean old entries
	err := guard.CanDelegate("invoke_codex")
	assert.NoError(t, err)

	// Old entries should be gone
	assert.Len(t, guard.delegationTimes, 0)
}
