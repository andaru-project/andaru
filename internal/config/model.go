package config

import (
	"errors"
	"strconv"
)

const requestLimit = 1 << 20
const documentLimit = 4 << 20
const objectLimit = 10000
const schemaURI = "https://andaru.invalid/api/v1/schema"

type Version string

func ParseVersion(s string) (Version, error) {
	if s == "" || len(s) > 19 || (len(s) > 1 && s[0] == '0') {
		return "", errors.New("invalid version")
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return "", errors.New("invalid version")
		}
	}
	if _, e := strconv.ParseInt(s, 10, 64); e != nil {
		return "", errors.New("invalid version")
	}
	return Version(s), nil
}

type FieldError struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}
type validationError []FieldError

func (validationError) Error() string { return "configuration validation failed" }

var errInvalidJSON = errors.New("invalid JSON profile")
var errTooLarge = errors.New("configuration size limit exceeded")

type identity struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}
type secretReference struct {
	Secret string `json:"secret"`
}
type link struct {
	identity
	Kind         string           `json:"kind"`
	MTU          int              `json:"mtu"`
	Enabled      bool             `json:"enabled"`
	PermanentMAC string           `json:"permanentMac,omitempty"`
	Parent       string           `json:"parent,omitempty"`
	VLANID       *int             `json:"vlanId,omitempty"`
	Members      *[]string        `json:"members,omitempty"`
	Mode         string           `json:"mode,omitempty"`
	Username     string           `json:"username,omitempty"`
	Password     *secretReference `json:"password,omitempty"`
}
type ipv4 struct {
	Mode      string   `json:"mode"`
	Addresses []string `json:"addresses,omitempty"`
	Gateway   string   `json:"gateway,omitempty"`
}
type advertisement struct {
	Enabled   bool `json:"enabled"`
	Managed   bool `json:"managed"`
	OtherInfo bool `json:"otherInfo"`
}
type ipv6 struct {
	Mode                string         `json:"mode"`
	Addresses           []string       `json:"addresses,omitempty"`
	Gateway             string         `json:"gateway,omitempty"`
	RequestPrefixLength *int           `json:"requestPrefixLength,omitempty"`
	From                string         `json:"from,omitempty"`
	SubnetID            string         `json:"subnetId,omitempty"`
	RA                  *advertisement `json:"ra,omitempty"`
}

// dns is what the host itself asks; it serves no client. Order is the order servers are tried in.
type dns struct {
	Servers       []string `json:"servers"`
	SearchDomains []string `json:"searchDomains,omitempty"`
}
type addressing struct {
	IPv4 ipv4 `json:"ipv4"`
	IPv6 ipv6 `json:"ipv6"`
	DNS  *dns `json:"dns,omitempty"`
}
type segment struct {
	identity
	Link       string     `json:"link"`
	Zone       string     `json:"zone"`
	Addressing addressing `json:"addressing"`
}
type zone struct{ identity }
type declaration struct {
	SchemaVersion int       `json:"schemaVersion"`
	Links         []link    `json:"links"`
	Segments      []segment `json:"segments"`
	Zones         []zone    `json:"zones"`
}

func emptyDeclaration() declaration {
	return declaration{SchemaVersion: 1, Links: []link{}, Segments: []segment{}, Zones: []zone{}}
}
func linkMembers(l link) []string {
	if l.Members == nil {
		return nil
	}
	return *l.Members
}
