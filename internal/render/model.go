// Package render turns a saved network declaration into the files Andaru
// would hand to systemd-networkd and pppd. It performs no I/O.
package render

import (
	"encoding/json"

	"github.com/andaru-project/andaru/internal/host"
)

// The structs below decode the public snapshot JSON; the served JSON Schema is the contract.
type link struct {
	ID           string   `json:"id"`
	Kind         string   `json:"kind"`
	MTU          int      `json:"mtu"`
	Enabled      bool     `json:"enabled"`
	PermanentMac string   `json:"permanentMac"`
	Parent       string   `json:"parent"`
	VLANID       *int     `json:"vlanId"`
	Members      []string `json:"members"`
	Mode         string   `json:"mode"`
	Username     string   `json:"username"`
	Password     *struct {
		Secret string `json:"secret"`
	} `json:"password"`
}
type advertisement struct {
	Enabled   bool `json:"enabled"`
	Managed   bool `json:"managed"`
	OtherInfo bool `json:"otherInfo"`
}
type ipv4 struct {
	Mode      string   `json:"mode"`
	Addresses []string `json:"addresses"`
	Gateway   string   `json:"gateway"`
}
type ipv6 struct {
	Mode                string         `json:"mode"`
	Addresses           []string       `json:"addresses"`
	Gateway             string         `json:"gateway"`
	RequestPrefixLength *int           `json:"requestPrefixLength"`
	From                string         `json:"from"`
	SubnetID            string         `json:"subnetId"`
	RA                  *advertisement `json:"ra"`
}
type segment struct {
	ID         string `json:"id"`
	Link       string `json:"link"`
	Addressing struct {
		IPv4 ipv4 `json:"ipv4"`
		IPv6 ipv6 `json:"ipv6"`
	} `json:"addressing"`
}
type declaration struct {
	Links    []link    `json:"links"`
	Segments []segment `json:"segments"`
}

// Input is everything a render depends on. Secrets holds real values.
type Input struct {
	Declaration json.RawMessage
	Secrets     map[string]string
	Links       []host.LinkStatus
	Facts       host.RealizerFacts
}

// File is one rendered file. Content is safe to show; Real is not.
type File struct {
	Path    string   `json:"path"`
	Mode    string   `json:"mode"`
	Secret  bool     `json:"secret"`
	Content string   `json:"content"`
	Sources []string `json:"sources"`
	real    string
}

// Real returns the content with secret values in place.
func (f File) Real() string {
	if f.Secret {
		return f.real
	}
	return f.Content
}

type Finding struct {
	Severity string `json:"severity"`
	Code     string `json:"code"`
	Path     string `json:"path"`
	Message  string `json:"message"`
}
type Resolution struct {
	Link      string `json:"link"`
	Interface string `json:"interface"`
}
type Result struct {
	Realizable bool         `json:"realizable"`
	Resolved   []Resolution `json:"resolved"`
	Files      []File       `json:"files"`
	Findings   []Finding    `json:"findings"`
}
