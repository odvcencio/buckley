package workspaceguard

import "m31labs.dev/buckley/pkg/launchcontract"

const (
	LaunchProfileDescriptorSchema      = launchcontract.ProfileSchema
	LaunchProviderOpenRouter           = launchcontract.ProviderOpenRouter
	LaunchModelOxAlpha                 = launchcontract.ModelOxAlpha
	LaunchReasoningMax                 = launchcontract.ReasoningMax
	LaunchRetentionNonZDR              = launchcontract.RetentionNonZDR
	LaunchDataCollectionDeny           = launchcontract.DataCollectionDeny
	LaunchRetryOwnerDapr               = launchcontract.RetryOwnerDapr
	LaunchCatalogSourceOpenRouter      = launchcontract.CatalogSourceOpenRouter
	LaunchCatalogTTL                   = launchcontract.CatalogTTL
	LaunchProviderPostAttempts         = launchcontract.ProviderPostAttempts
	LaunchManagerAffordabilityAttempts = launchcontract.ManagerAffordabilityAttempts
	MaxProfileDescriptorBytes          = launchcontract.MaxProfileBytes
)

type LaunchPrivacyContract = launchcontract.PrivacyContract
type LaunchLicenseRequirement = launchcontract.LicenseRequirement
type LaunchProfileLimits = launchcontract.ProfileLimits
type LaunchMaxPrice = launchcontract.MaxPrice
type LaunchPriceGuard = launchcontract.PriceGuard
type LaunchProfileDescriptor = launchcontract.ProfileDescriptor

func ResolveLaunchProfileDescriptor(value string) (LaunchProfileDescriptor, error) {
	return launchcontract.ResolveProfile(value)
}

func DecodeLaunchProfileDescriptor(data []byte) (LaunchProfileDescriptor, error) {
	return launchcontract.DecodeProfile(data)
}
