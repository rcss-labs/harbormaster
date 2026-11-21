package cost

import "testing"

// DropSpotRateForTest removes one region's Spot price and returns the function that puts it back. It exists so
// tests outside this package can build a region that has a list price but no Spot price, which the price tables
// never hold. Production code must not call it.
func DropSpotRateForTest(region string) (restore func()) {
	if !testing.Testing() {
		panic("cost.DropSpotRateForTest is for tests only")
	}
	saved, had := fargateSpotRates[region]
	delete(fargateSpotRates, region)
	return func() {
		if had {
			fargateSpotRates[region] = saved
		}
	}
}
