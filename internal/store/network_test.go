package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

var networkPolledAt = time.Date(2026, 10, 31, 9, 0, 0, 0, time.UTC)

func TestReplaceNetwork_UpsertsAndDeletesVanished(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	const account, region = "482109375512", "us-east-1"

	subnetA := model.Subnet{AccountID: account, Region: region, SubnetID: "subnet-a", VPCID: "vpc-1", Name: "a-old", CIDR: "10.0.1.0/24", AZ: "us-east-1a", Raw: json.RawMessage(`{}`), PolledAt: networkPolledAt}
	subnetB := model.Subnet{AccountID: account, Region: region, SubnetID: "subnet-b", VPCID: "vpc-1", Name: "b", CIDR: "10.0.2.0/24", AZ: "us-east-1b", Raw: json.RawMessage(`{}`), PolledAt: networkPolledAt}
	if err := s.ReplaceNetwork(ctx, account, region, nil, []model.Subnet{subnetA, subnetB}, nil, nil); err != nil {
		t.Fatalf("first ReplaceNetwork: %v", err)
	}

	subnetARenamed := subnetA
	subnetARenamed.Name = "a-new"
	subnetC := model.Subnet{AccountID: account, Region: region, SubnetID: "subnet-c", VPCID: "vpc-1", Name: "c", CIDR: "10.0.3.0/24", AZ: "us-east-1c", Raw: json.RawMessage(`{}`), PolledAt: networkPolledAt}
	if err := s.ReplaceNetwork(ctx, account, region, nil, []model.Subnet{subnetARenamed, subnetC}, nil, nil); err != nil {
		t.Fatalf("second ReplaceNetwork: %v", err)
	}

	got, err := s.ListSubnets(ctx, account, region)
	if err != nil {
		t.Fatalf("ListSubnets: %v", err)
	}
	if len(got) != 2 || got[0].SubnetID != "subnet-a" || got[0].Name != "a-new" || got[1].SubnetID != "subnet-c" {
		t.Errorf("subnets after replace = %+v; want subnet-a named a-new and subnet-c only", got)
	}
}

func TestReplaceNetwork_IsScopedToAccountRegion(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	kept := model.VPC{AccountID: "1", Region: "eu-west-1", VPCID: "vpc-eu", Name: "eu", CIDR: "10.1.0.0/16", Raw: json.RawMessage(`{}`), PolledAt: networkPolledAt}
	if err := s.ReplaceNetwork(ctx, "1", "eu-west-1", []model.VPC{kept}, nil, nil, nil); err != nil {
		t.Fatalf("seed eu-west-1: %v", err)
	}

	if err := s.ReplaceNetwork(ctx, "1", "us-east-1", nil, nil, nil, nil); err != nil {
		t.Fatalf("replace us-east-1: %v", err)
	}

	vpcs, err := s.ListVPCs(ctx, "1", "eu-west-1")
	if err != nil {
		t.Fatalf("ListVPCs: %v", err)
	}
	if len(vpcs) != 1 || vpcs[0].VPCID != "vpc-eu" {
		t.Errorf("eu-west-1 VPCs = %+v; want vpc-eu to survive a us-east-1 replace", vpcs)
	}
}

func TestGetVPC_NotFound(t *testing.T) {
	s := storetest.Open(t)
	_, err := s.GetVPC(context.Background(), "1", "us-east-1", "vpc-missing")
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetVPC error = %v, want ErrNotFound", err)
	}
}

func TestListSubnets_WildcardAccountRegion(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	first := model.Subnet{AccountID: "1", Region: "us-east-1", SubnetID: "subnet-1", VPCID: "vpc-1", Raw: json.RawMessage(`{}`), PolledAt: networkPolledAt}
	second := model.Subnet{AccountID: "2", Region: "eu-west-1", SubnetID: "subnet-2", VPCID: "vpc-2", Raw: json.RawMessage(`{}`), PolledAt: networkPolledAt}
	if err := s.ReplaceNetwork(ctx, "1", "us-east-1", nil, []model.Subnet{first}, nil, nil); err != nil {
		t.Fatalf("seed first: %v", err)
	}
	if err := s.ReplaceNetwork(ctx, "2", "eu-west-1", nil, []model.Subnet{second}, nil, nil); err != nil {
		t.Fatalf("seed second: %v", err)
	}

	all, err := s.ListSubnets(ctx, "", "")
	if err != nil {
		t.Fatalf("ListSubnets wildcard: %v", err)
	}
	if len(all) != 2 {
		t.Errorf("wildcard subnets = %d, want 2 across both account/regions", len(all))
	}
}

func TestListVPCEdges_FilteredByVPC(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	edges := []model.VPCEdge{
		{AccountID: "1", Region: "us-east-1", VPCID: "vpc-1", Kind: model.VPCEdgeIGW, ResourceID: "igw-1", Raw: json.RawMessage(`{}`), PolledAt: networkPolledAt},
		{AccountID: "1", Region: "us-east-1", VPCID: "vpc-2", Kind: model.VPCEdgeNAT, ResourceID: "nat-2", SubnetID: "subnet-9", Detail: "public", Raw: json.RawMessage(`{}`), PolledAt: networkPolledAt},
	}
	if err := s.ReplaceNetwork(ctx, "1", "us-east-1", nil, nil, edges, nil); err != nil {
		t.Fatalf("ReplaceNetwork: %v", err)
	}

	got, err := s.ListVPCEdges(ctx, "1", "us-east-1", "vpc-1")
	if err != nil {
		t.Fatalf("ListVPCEdges: %v", err)
	}
	if len(got) != 1 || got[0].ResourceID != "igw-1" || got[0].Kind != model.VPCEdgeIGW {
		t.Errorf("edges for vpc-1 = %+v; want only igw-1", got)
	}
}

func TestListSecurityGroups_ByIDs(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	groups := []model.SecurityGroup{
		{AccountID: "1", Region: "us-east-1", GroupID: "sg-a", VPCID: "vpc-1", Name: "web", Raw: json.RawMessage(`{}`), PolledAt: networkPolledAt},
		{AccountID: "1", Region: "us-east-1", GroupID: "sg-b", VPCID: "vpc-1", Name: "db", Raw: json.RawMessage(`{}`), PolledAt: networkPolledAt},
		{AccountID: "1", Region: "us-east-1", GroupID: "sg-c", VPCID: "vpc-1", Name: "ssh", Raw: json.RawMessage(`{}`), PolledAt: networkPolledAt},
	}
	if err := s.ReplaceNetwork(ctx, "1", "us-east-1", nil, nil, nil, groups); err != nil {
		t.Fatalf("ReplaceNetwork: %v", err)
	}

	got, err := s.ListSecurityGroups(ctx, "1", "us-east-1", []string{"sg-a", "sg-c"})
	if err != nil {
		t.Fatalf("ListSecurityGroups: %v", err)
	}
	if len(got) != 2 || got[0].GroupID != "sg-a" || got[1].GroupID != "sg-c" {
		t.Errorf("security groups by id = %+v; want sg-a and sg-c", got)
	}
}
