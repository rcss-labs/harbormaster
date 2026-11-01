package fake

import (
	"net/netip"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
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
		if aws.ToString(tk.Task.LastStatus) == "RUNNING" && hasPrefix(aws.ToString(tk.Task.Group), "family:") {
			return
		}
	}
	t.Fatal("no running standalone task")
}

func hasPrefix(s, p string) bool { return len(s) >= len(p) && s[:len(p)] == p }

func TestFakeEstate_TaskDefinitionsMountEFS(t *testing.T) {
	e := newFake().Estate
	want := map[string][2]string{
		"report-worker":      {"fs-02e7d1c9", "/data/reports"},
		"jenkins-controller": {"fs-0c91a4e7", "/var/jenkins_home"},
		"search-indexer":     {"fs-0d18c2ab", "/index"},
		"edge-nginx":         {"fs-0a6b33f0", "/etc/nginx/certs"},
		"cert-renewer":       {"fs-0a6b33f0", "/certs"},
	}
	for _, td := range e.TaskDefinitions {
		fam := aws.ToString(td.Definition.Family)
		w, ok := want[fam]
		if !ok {
			continue
		}
		efsVols := 0
		for _, v := range td.Definition.Volumes {
			cfg := v.EfsVolumeConfiguration
			if cfg == nil {
				continue
			}
			efsVols++
			if aws.ToString(cfg.FileSystemId) != w[0] || cfg.TransitEncryption != ecstypes.EFSTransitEncryptionEnabled ||
				cfg.AuthorizationConfig == nil || aws.ToString(cfg.AuthorizationConfig.AccessPointId) == "" ||
				cfg.AuthorizationConfig.Iam != ecstypes.EFSAuthorizationConfigIAMEnabled {
				t.Fatalf("%s volume config = %+v", fam, cfg)
			}
		}
		if efsVols != 1 {
			t.Fatalf("%s has %d efs volumes", fam, efsVols)
		}
		if got := aws.ToString(td.Definition.ContainerDefinitions[0].MountPoints[0].ContainerPath); got != w[1] {
			t.Fatalf("%s container path = %s, want %s", fam, got, w[1])
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
