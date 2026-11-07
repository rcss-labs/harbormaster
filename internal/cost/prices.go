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

// RateFor returns the Fargate rate for a region and an ECS CPU architecture.
// An empty architecture means x86. It returns false for any region or
// architecture without a price.
func RateFor(region, architecture string) (Rate, bool) {
	rates, ok := fargateRates[region]
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
