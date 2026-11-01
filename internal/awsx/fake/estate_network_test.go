package fake

import (
	"context"
	"net/netip"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/aws/aws-sdk-go-v2/service/efs"

	"github.com/rcss-labs/harbormaster/internal/awsx"
)

func taskDetail(tk Task, name string) string {
	for _, att := range tk.Task.Attachments {
		for _, d := range att.Details {
			if aws.ToString(d.Name) == name {
				return aws.ToString(d.Value)
			}
		}
	}
	return ""
}

func taskSubnetID(tk Task) string { return taskDetail(tk, "subnetId") }

func TestFakeEstate_TasksUseSeededSubnets(t *testing.T) {
	e := newFake().Estate
	cidrs := map[string]string{}
	for _, s := range e.Network.Subnets {
		cidrs[s.AccountID+"/"+s.Region+"/"+aws.ToString(s.Subnet.SubnetId)] = aws.ToString(s.Subnet.CidrBlock)
	}
	checked := 0
	for _, tk := range e.Tasks {
		if aws.ToString(tk.Task.LastStatus) != "RUNNING" || taskSubnetID(tk) == "" {
			continue
		}
		cidr, ok := cidrs[tk.AccountID+"/"+tk.Region+"/"+taskSubnetID(tk)]
		if !ok {
			t.Fatalf("task %s subnet %s not in fake subnets", aws.ToString(tk.Task.TaskArn), taskSubnetID(tk))
		}
		ip, err := netip.ParseAddr(taskDetail(tk, "privateIPv4Address"))
		if err != nil || !netip.MustParsePrefix(cidr).Contains(ip) {
			t.Fatalf("task ip %v not in %s", ip, cidr)
		}
		checked++
	}
	if checked == 0 {
		t.Fatal("no running awsvpc tasks checked")
	}
}

func TestFakeEstate_HasStandaloneTask(t *testing.T) {
	e := newFake().Estate
	for _, tk := range e.Tasks {
		if aws.ToString(tk.Task.LastStatus) == "RUNNING" && strings.HasPrefix(aws.ToString(tk.Task.Group), "family:") {
			return
		}
	}
	t.Fatal("no running standalone task")
}

func TestFakeEstate_TaskDefinitionsMountEFS(t *testing.T) {
	e := newFake().Estate
	want := map[string][3]string{
		"report-worker":      {"fs-02e7d1c9", "/data/reports", "fsap-07f3e9a1c2"},
		"jenkins-controller": {"fs-0c91a4e7", "/var/jenkins_home", "fsap-0a1b2c3d4e"},
		"search-indexer":     {"fs-0d18c2ab", "/index", "fsap-03c7a9e5d1"},
		"edge-nginx":         {"fs-0a6b33f0", "/etc/nginx/certs", "fsap-0e44d18b7a"},
		"cert-renewer":       {"fs-0a6b33f0", "/certs", "fsap-0e44d18b7a"},
	}
	seen := map[string]bool{}
	for _, td := range e.TaskDefinitions {
		fam := aws.ToString(td.Definition.Family)
		w, ok := want[fam]
		if !ok {
			continue
		}
		seen[fam] = true
		efsVols := 0
		for _, v := range td.Definition.Volumes {
			cfg := v.EfsVolumeConfiguration
			if cfg == nil {
				continue
			}
			efsVols++
			if aws.ToString(cfg.FileSystemId) != w[0] || cfg.TransitEncryption != ecstypes.EFSTransitEncryptionEnabled ||
				cfg.AuthorizationConfig == nil || aws.ToString(cfg.AuthorizationConfig.AccessPointId) != w[2] ||
				cfg.AuthorizationConfig.Iam != ecstypes.EFSAuthorizationConfigIAMEnabled {
				t.Fatalf("%s volume config = %+v", fam, cfg)
			}
		}
		if efsVols != 1 {
			t.Fatalf("%s has %d efs volumes", fam, efsVols)
		}
		if len(td.Definition.ContainerDefinitions) == 0 || len(td.Definition.ContainerDefinitions[0].MountPoints) == 0 {
			t.Fatalf("%s has no container mount point", fam)
		}
		if got := aws.ToString(td.Definition.ContainerDefinitions[0].MountPoints[0].ContainerPath); got != w[1] {
			t.Fatalf("%s container path = %s, want %s", fam, got, w[1])
		}
	}
	if len(seen) != len(want) {
		t.Fatalf("saw task definitions for %d of %d EFS families", len(seen), len(want))
	}
}

func TestFakeEstate_ServiceSecurityGroupsAreDescribable(t *testing.T) {
	f := newFake()
	for _, svc := range f.Estate.Services {
		nc := svc.Service.NetworkConfiguration
		if nc == nil || nc.AwsvpcConfiguration == nil {
			continue
		}
		api := ec2For(t, f, awsx.Target{AccountID: svc.AccountID, Region: svc.Region})
		for _, sg := range nc.AwsvpcConfiguration.SecurityGroups {
			out, err := api.DescribeSecurityGroups(context.Background(), &ec2.DescribeSecurityGroupsInput{GroupIds: []string{sg}})
			if err != nil || len(out.SecurityGroups) != 1 || aws.ToString(out.SecurityGroups[0].GroupName) != aws.ToString(svc.Service.ServiceName) {
				t.Fatalf("service %s sg %s: %v err %v", aws.ToString(svc.Service.ServiceName), sg, out, err)
			}
		}
	}
}

func TestFakePaging_BadTokenDoesNotPanic(t *testing.T) {
	f := newFake()
	for _, tok := range []string{"-5", "abc"} {
		if _, err := ec2For(t, f, homelabUSEast1).DescribeSubnets(context.Background(), &ec2.DescribeSubnetsInput{MaxResults: aws.Int32(2), NextToken: aws.String(tok)}); err != nil {
			t.Fatal(err)
		}
		if _, err := efsFor(t, f, homelabUSEast1).DescribeFileSystems(context.Background(), &efs.DescribeFileSystemsInput{Marker: aws.String(tok)}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestFakeEstate_SubnetSourceOfTruthMatchesClusterSeed(t *testing.T) {
	e := newFake().Estate
	have := map[string]bool{}
	for _, s := range e.Network.Subnets {
		have[aws.ToString(s.Subnet.SubnetId)] = true
	}
	for _, c := range seedClusters {
		for _, sub := range c.subnets {
			if !have[sub.id] {
				t.Fatalf("cluster %s subnet %s missing from fake subnets", c.name, sub.id)
			}
		}
	}
}
