package fake

import (
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	efstypes "github.com/aws/aws-sdk-go-v2/service/efs/types"
)

// Plan 05: the fake VPC, subnet, gateway, security group and EFS estate. It is
// seeded from the mockup's VPCS / EFS tables. Subnet ids, CIDRs and zones come
// from seedClusters so the ECS tasks' placement and the EC2 view never disagree.

type NetVpc struct {
	AccountID, Region, Name string
	Vpc                     ec2types.Vpc
}

type NetSubnet struct {
	AccountID, Region, Name string
	// Free is the mockup's free-IP count before running tasks are subtracted.
	Free   int
	Subnet ec2types.Subnet
}

type NetNat struct {
	AccountID, Region string
	Nat               ec2types.NatGateway
}

type NetIgw struct {
	AccountID, Region string
	Igw               ec2types.InternetGateway
}

type NetEndpoint struct {
	AccountID, Region string
	Endpoint          ec2types.VpcEndpoint
}

type NetSecurityGroup struct {
	AccountID, Region string
	Group             ec2types.SecurityGroup
}

type NetFileSystem struct {
	AccountID, Region string
	FileSystem        efstypes.FileSystemDescription
}

type NetAccessPoint struct {
	AccountID, Region string
	AccessPoint       efstypes.AccessPointDescription
}

type NetMountTarget struct {
	AccountID, Region string
	MountTarget       efstypes.MountTargetDescription
}

// NetworkEstate holds everything EC2 and EFS describe. Guarded by Estate.Mu.
type NetworkEstate struct {
	Vpcs             []NetVpc
	Subnets          []NetSubnet
	NatGateways      []NetNat
	InternetGateways []NetIgw
	Endpoints        []NetEndpoint
	SecurityGroups   []NetSecurityGroup
	FileSystems      []NetFileSystem
	AccessPoints     []NetAccessPoint
	MountTargets     []NetMountTarget
}

type vpcSubnetSeed struct {
	name string
	id   string
	free int
	// explicit is set for subnets no seeded cluster uses (the default VPC's).
	explicit *seedSubnet
}

type vpcSeed struct {
	account, region, name, id, cidr string
	isDefault, nat                  bool
	subnets                         []vpcSubnetSeed
}

var defaultVPCSubnet = seedSubnet{"subnet-6f0a1c3e", "172.31.0.0/20", "us-east-1a", true}

var vpcSeeds = []vpcSeed{
	{homelabAccountID, "us-east-1", "homelab-main", "vpc-0a41c7e29b3d", "10.20.0.0/16", false, true, []vpcSubnetSeed{
		{"private-1a", "subnet-03f1a9c2", 231, nil}, {"private-1b", "subnet-0b77e410", 236, nil}, {"public-1a", "subnet-09c2d5ee", 248, nil}}},
	{homelabAccountID, "us-east-1", "shared-services", "vpc-07de19a4c011", "10.21.0.0/16", false, true, []vpcSubnetSeed{
		{"tools-1a", "subnet-0e1c88a1", 247, nil}, {"tools-1c", "subnet-02aa71f3", 249, nil}}},
	{homelabAccountID, "us-east-1", "legacy-default", "vpc-5c1e0f2a", "172.31.0.0/16", true, false, []vpcSubnetSeed{
		{"default-1a", "subnet-6f0a1c3e", 4091, &defaultVPCSubnet}}},
	{homelabAccountID, "eu-west-1", "eu-apps", "vpc-0f3392bb71e2", "10.30.0.0/16", false, true, []vpcSubnetSeed{
		{"private-1a", "subnet-0a90f7c1", 240, nil}, {"private-1b", "subnet-0c6b1e92", 242, nil}}},
	{homelabAccountID, "eu-central-1", "edge", "vpc-03b1d7f0c5a8", "10.40.0.0/16", false, true, []vpcSubnetSeed{
		{"private-1a", "subnet-0d2e5a77", 250, nil}, {"public-1a", "subnet-0f61c3b8", 248, nil}}},
	{stagingAccountID, "us-east-1", "staging", "vpc-0b18e6cd2a91", "10.50.0.0/16", false, true, []vpcSubnetSeed{
		{"private-a", "subnet-01f8a2d4", 249, nil}, {"private-b", "subnet-0e7c3b19", 250, nil}}},
}

// mainVPCEndpointServices are homelab-main's endpoints; the .s3 one is a
// gateway endpoint that consumers must filter out.
var mainVPCEndpointServices = []string{"ecr.api", "ecr.dkr", "logs", "ssmmessages", "s3"}

type efsSeed struct {
	name, id, vpcName string
	gib               float64
	throughput        efstypes.ThroughputMode
	provisionedMiBps  float64
	apID, apPath      string
	apUID             int64
}

var efsSeeds = []efsSeed{
	{"jenkins-home", "fs-0c91a4e7", "shared-services", 38.4, efstypes.ThroughputModeElastic, 0, "fsap-0a1b2c3d4e", "/jenkins", 1000},
	{"reports-data", "fs-02e7d1c9", "homelab-main", 12.1, efstypes.ThroughputModeBursting, 0, "fsap-07f3e9a1c2", "/reports", 1001},
	{"shared-certs", "fs-0a6b33f0", "edge", 0.02, efstypes.ThroughputModeBursting, 0, "fsap-0e44d18b7a", "/certs", 101},
	{"search-index", "fs-0d18c2ab", "eu-apps", 74.9, efstypes.ThroughputModeProvisioned, 64, "fsap-03c7a9e5d1", "/index", 1000},
	{"uploads-archive", "fs-07a2b9e1", "homelab-main", 211.6, efstypes.ThroughputModeElastic, 0, "", "", 0},
}

// hexString returns n deterministic lowercase hex digits derived from key.
func hexString(key string, n int) string {
	var b strings.Builder
	for salt := 0; b.Len() < n; salt++ {
		fmt.Fprintf(&b, "%016x", hashSeed(key, salt))
	}
	return b.String()[:n]
}

func nameTag(name string) []ec2types.Tag {
	return []ec2types.Tag{{Key: aws.String("Name"), Value: aws.String(name)}}
}

func lookupSeedSubnet(sub vpcSubnetSeed) seedSubnet {
	if sub.explicit != nil {
		return *sub.explicit
	}
	for _, c := range seedClusters {
		for _, ss := range c.subnets {
			if ss.id == sub.id {
				return ss
			}
		}
	}
	panic("subnet " + sub.id + " is not seeded by any cluster")
}

// serviceSecurityGroupID is the group serviceNetwork attaches to a service.
func serviceSecurityGroupID(service string) string {
	return "sg-0" + fmt.Sprintf("%08x", hashSeed(service, 0)&0xffffffff)
}

func seedNetwork(e *Estate) {
	n := &e.Network
	for _, v := range vpcSeeds {
		n.Vpcs = append(n.Vpcs, NetVpc{AccountID: v.account, Region: v.region, Name: v.name, Vpc: ec2types.Vpc{
			VpcId: aws.String(v.id), CidrBlock: aws.String(v.cidr), IsDefault: aws.Bool(v.isDefault),
			State: ec2types.VpcStateAvailable, OwnerId: aws.String(v.account), Tags: nameTag(v.name)}})
		natSubnet := firstPublicOrFirstSubnetID(v)
		for _, sub := range v.subnets {
			ss := lookupSeedSubnet(sub)
			n.Subnets = append(n.Subnets, NetSubnet{AccountID: v.account, Region: v.region, Name: sub.name, Free: sub.free, Subnet: ec2types.Subnet{
				SubnetId: aws.String(ss.id), VpcId: aws.String(v.id), CidrBlock: aws.String(ss.cidr), AvailabilityZone: aws.String(ss.az),
				MapPublicIpOnLaunch: aws.Bool(ss.public), State: ec2types.SubnetStateAvailable, OwnerId: aws.String(v.account), Tags: nameTag(sub.name)}})
		}
		if v.isDefault {
			continue
		}
		n.InternetGateways = append(n.InternetGateways, NetIgw{AccountID: v.account, Region: v.region, Igw: ec2types.InternetGateway{
			InternetGatewayId: aws.String("igw-" + hexString("igw:"+v.id, 8)), OwnerId: aws.String(v.account),
			Attachments: []ec2types.InternetGatewayAttachment{{VpcId: aws.String(v.id), State: ec2types.AttachmentStatusAttached}}, Tags: nameTag(v.name + "-igw")}})
		if v.nat {
			n.NatGateways = append(n.NatGateways, NetNat{AccountID: v.account, Region: v.region, Nat: ec2types.NatGateway{
				NatGatewayId: aws.String("nat-" + hexString("nat:"+v.id, 17)), VpcId: aws.String(v.id), SubnetId: aws.String(natSubnet),
				State: ec2types.NatGatewayStateAvailable, ConnectivityType: ec2types.ConnectivityTypePublic, Tags: nameTag(v.name + "-nat")}})
		}
		if v.name == "homelab-main" {
			for _, svc := range mainVPCEndpointServices {
				kind := ec2types.VpcEndpointTypeInterface
				if svc == "s3" {
					kind = ec2types.VpcEndpointTypeGateway
				}
				n.Endpoints = append(n.Endpoints, NetEndpoint{AccountID: v.account, Region: v.region, Endpoint: ec2types.VpcEndpoint{
					VpcEndpointId: aws.String("vpce-0" + hexString("vpce:"+svc, 16)), VpcId: aws.String(v.id),
					ServiceName: aws.String("com.amazonaws." + v.region + "." + svc), VpcEndpointType: kind, State: ec2types.StateAvailable,
					SubnetIds: privateSubnetIDs(v)}})
			}
		}
	}
	seedSecurityGroups(e)
	seedEFSEstate(e)
}

// firstPublicOrFirstSubnetID is where a VPC's NAT gateway lives.
func firstPublicOrFirstSubnetID(v vpcSeed) string {
	for _, sub := range v.subnets {
		if lookupSeedSubnet(sub).public {
			return sub.id
		}
	}
	return v.subnets[0].id
}

func privateSubnetIDs(v vpcSeed) []string {
	var ids []string
	for _, sub := range v.subnets {
		if !lookupSeedSubnet(sub).public {
			ids = append(ids, sub.id)
		}
	}
	return ids
}

// vpcOfCluster finds the VPC whose subnets the cluster's services run in.
func vpcOfCluster(cluster string) (vpcSeed, bool) {
	c := clusterSeed(cluster)
	for _, v := range vpcSeeds {
		for _, sub := range v.subnets {
			if len(c.subnets) > 0 && sub.id == c.subnets[0].id {
				return v, true
			}
		}
	}
	return vpcSeed{}, false
}

// seedSecurityGroups adds one group per service (named after it, with the id
// the service's network configuration references) and an alb-ingress group per
// VPC that hosts a load-balanced service.
func seedSecurityGroups(e *Estate) {
	n := &e.Network
	albDone := map[string]bool{}
	for _, sp := range seedServices {
		v, ok := vpcOfCluster(sp.cluster)
		if !ok {
			continue
		}
		add := func(id, name string) {
			n.SecurityGroups = append(n.SecurityGroups, NetSecurityGroup{AccountID: v.account, Region: v.region, Group: ec2types.SecurityGroup{
				GroupId: aws.String(id), GroupName: aws.String(name), VpcId: aws.String(v.id), OwnerId: aws.String(v.account),
				Description: aws.String(name + " tasks")}})
		}
		add(serviceSecurityGroupID(sp.name), sp.name)
		if sp.lb && !albDone[v.id] {
			albDone[v.id] = true
			add("sg-0"+hexString("alb-ingress:"+v.id, 8), "alb-ingress")
		}
	}
}

func seedEFSEstate(e *Estate) {
	n := &e.Network
	created := e.now().Add(-90 * 24 * time.Hour)
	for _, fs := range efsSeeds {
		var vpc vpcSeed
		for _, v := range vpcSeeds {
			if v.name == fs.vpcName {
				vpc = v
			}
		}
		desc := efstypes.FileSystemDescription{
			FileSystemId: aws.String(fs.id), Name: aws.String(fs.name), OwnerId: aws.String(vpc.account),
			CreationToken: aws.String(fs.name), CreationTime: aws.Time(created),
			LifeCycleState: efstypes.LifeCycleStateAvailable, PerformanceMode: efstypes.PerformanceModeGeneralPurpose,
			ThroughputMode: fs.throughput, Encrypted: aws.Bool(true),
			SizeInBytes: &efstypes.FileSystemSize{Value: int64(fs.gib * (1 << 30)), Timestamp: aws.Time(e.now())},
			Tags:        []efstypes.Tag{{Key: aws.String("Name"), Value: aws.String(fs.name)}},
		}
		if fs.provisionedMiBps > 0 {
			desc.ProvisionedThroughputInMibps = aws.Float64(fs.provisionedMiBps)
		}
		if fs.apID != "" {
			n.AccessPoints = append(n.AccessPoints, NetAccessPoint{AccountID: vpc.account, Region: vpc.region, AccessPoint: efstypes.AccessPointDescription{
				AccessPointId: aws.String(fs.apID), FileSystemId: aws.String(fs.id), Name: aws.String(fs.name + "-ap"), OwnerId: aws.String(vpc.account),
				LifeCycleState: efstypes.LifeCycleStateAvailable, RootDirectory: &efstypes.RootDirectory{Path: aws.String(fs.apPath)},
				PosixUser: &efstypes.PosixUser{Uid: aws.Int64(fs.apUID), Gid: aws.Int64(fs.apUID)}}})
		}
		mounts := 0
		for _, sub := range vpc.subnets {
			ss := lookupSeedSubnet(sub)
			if ss.public {
				continue
			}
			mounts++
			n.MountTargets = append(n.MountTargets, NetMountTarget{AccountID: vpc.account, Region: vpc.region, MountTarget: efstypes.MountTargetDescription{
				MountTargetId: aws.String("fsmt-0" + hexString("fsmt:"+fs.id+sub.id, 16)), FileSystemId: aws.String(fs.id),
				SubnetId: aws.String(ss.id), VpcId: aws.String(vpc.id), AvailabilityZoneName: aws.String(ss.az),
				IpAddress: aws.String(ss.cidr[:strings.LastIndex(ss.cidr, ".")] + ".200"), OwnerId: aws.String(vpc.account),
				NetworkInterfaceId: aws.String("eni-0" + hexString("fsmt-eni:"+fs.id+sub.id, 16)), LifeCycleState: efstypes.LifeCycleStateAvailable}})
		}
		desc.NumberOfMountTargets = int32(mounts)
		n.FileSystems = append(n.FileSystems, NetFileSystem{AccountID: vpc.account, Region: vpc.region, FileSystem: desc})
	}
}
