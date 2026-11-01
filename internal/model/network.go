package model

import (
	"encoding/json"
	"time"
)

// VPC is one VPC in an account and region, as cached by the network poller.
type VPC struct {
	AccountID string
	Region    string
	VPCID     string
	Name      string
	CIDR      string
	IsDefault bool
	Raw       json.RawMessage
	PolledAt  time.Time
}

// Subnet is one subnet of a VPC.
type Subnet struct {
	AccountID    string
	Region       string
	SubnetID     string
	VPCID        string
	Name         string
	CIDR         string
	AZ           string
	AvailableIPs int
	Public       bool
	Raw          json.RawMessage
	PolledAt     time.Time
}

// VPCEdgeKind is what a VPCEdge connects the VPC to.
type VPCEdgeKind string

const (
	VPCEdgeIGW      VPCEdgeKind = "igw"
	VPCEdgeNAT      VPCEdgeKind = "nat"
	VPCEdgeEndpoint VPCEdgeKind = "endpoint"
)

// VPCEdge is a gateway, NAT gateway or endpoint attached to a VPC.
type VPCEdge struct {
	AccountID  string
	Region     string
	VPCID      string
	Kind       VPCEdgeKind
	ResourceID string
	SubnetID   string
	Detail     string
	Raw        json.RawMessage
	PolledAt   time.Time
}

// SecurityGroup is one security group in a VPC.
type SecurityGroup struct {
	AccountID string
	Region    string
	GroupID   string
	VPCID     string
	Name      string
	Raw       json.RawMessage
	PolledAt  time.Time
}
