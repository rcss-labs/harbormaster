package cost

// Fargate list prices in USD per hour.
//
// Source: AWS Price List bulk API, offer AmazonECS, publication 2026-10-19T22:43:17Z:
//
//	https://pricing.us-east-1.amazonaws.com/offers/v1.0/aws/AmazonECS/20261019224317/{region}/index.json
//
// To refresh: download that index for each priced region, read the Linux
// per-vCPU-hour and per-GB-hour rates for x86 and ARM Fargate, update the table
// below, and change PricesAsOf to the publication date. Regions missing from the
// table have no price and must not fall back to another region.
const (
	HoursPerMonth = 730
	PricesAsOf    = "2026-10-19"
)

// Rate is the hourly price of one vCPU and one GB of memory.
type Rate struct {
	VCPUHour float64
	GBHour   float64
}

// Monthly returns the cost of one task running for HoursPerMonth. cpuUnits uses
// Fargate units (1024 per vCPU) and memoryMiB is in mebibytes.
func (r Rate) Monthly(cpuUnits, memoryMiB int) float64 {
	vcpus := float64(cpuUnits) / 1024
	gigabytes := float64(memoryMiB) / 1024
	return (vcpus*r.VCPUHour + gigabytes*r.GBHour) * HoursPerMonth
}

type regionRates struct {
	x86 Rate
	arm Rate
}

var fargateRates = map[string]regionRates{
	"us-east-1": {
		x86: Rate{VCPUHour: 0.04048, GBHour: 0.004445},
		arm: Rate{VCPUHour: 0.03238, GBHour: 0.00356},
	},
	"eu-west-1": {
		x86: Rate{VCPUHour: 0.04048, GBHour: 0.004445},
		arm: Rate{VCPUHour: 0.03238, GBHour: 0.00356},
	},
	"eu-central-1": {
		x86: Rate{VCPUHour: 0.04656, GBHour: 0.00511},
		arm: Rate{VCPUHour: 0.03725, GBHour: 0.00409},
	},
}

// SpotPricesAsOf is the day the Fargate Spot table below was read. It is independent of PricesAsOf.
//
// Fargate Spot prices are not in the AWS bulk price offer. Source: the public feed
//
//	https://dftu77xade0tc.cloudfront.net/fargate-spot-prices.json
//
// whose metadata says "This file is intended for use only on aws.amazon.com. We do not guarantee its
// availability or accuracy." It is therefore read by hand, never at runtime. To refresh: read the four
// units per region (vCPU-Hours, GB-Hours, ARM-vCPU-Hours, ARM-GB-Hours), update fargateSpotRates and
// change SpotPricesAsOf to the day you read it. Spot prices change; the list and Spot dates move
// independently.
const SpotPricesAsOf = "2026-11-21"

var fargateSpotRates = map[string]regionRates{
	"us-east-1": {
		x86: Rate{VCPUHour: 0.0148185, GBHour: 0.00162718},
		arm: Rate{VCPUHour: 0.01185334, GBHour: 0.00130321},
	},
	"eu-west-1": {
		x86: Rate{VCPUHour: 0.01216278, GBHour: 0.00133556},
		arm: Rate{VCPUHour: 0.00972902, GBHour: 0.00106965},
	},
	"eu-central-1": {
		x86: Rate{VCPUHour: 0.01697019, GBHour: 0.00186249},
		arm: Rate{VCPUHour: 0.01357688, GBHour: 0.00149072},
	},
}

// SpotRateFor returns the Fargate Spot rate for a region and an ECS CPU architecture, with the same
// architecture rules as RateFor. It returns false for a region without a Spot price.
func SpotRateFor(region, architecture string) (Rate, bool) {
	return lookupRate(fargateSpotRates, region, architecture)
}

// RateForClass returns the rate a task of the given class is billed at. A Spot task in a region without a
// Spot price has no rate: it never falls back to the list price.
func RateForClass(region, architecture string, class TaskClass) (Rate, bool) {
	switch class {
	case OnDemand:
		return RateFor(region, architecture)
	case Spot:
		return SpotRateFor(region, architecture)
	default:
		return Rate{}, false
	}
}

// RateFor returns the Fargate rate for a region and an ECS CPU architecture.
// An empty architecture means x86. It returns false for any region or
// architecture without a price.
func RateFor(region, architecture string) (Rate, bool) {
	return lookupRate(fargateRates, region, architecture)
}

func lookupRate(table map[string]regionRates, region, architecture string) (Rate, bool) {
	rates, ok := table[region]
	if !ok {
		return Rate{}, false
	}
	switch architecture {
	case "", "X86_64":
		return rates.x86, true
	case "ARM64":
		return rates.arm, true
	default:
		return Rate{}, false
	}
}
