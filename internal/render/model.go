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
	Mode              string `json:"mode"`
	Priority          string `json:"priority"`
	RouterLifetime    *int   `json:"routerLifetime"`
	ValidLifetime     *int   `json:"validLifetime"`
	PreferredLifetime *int   `json:"preferredLifetime"`
	DNS               *struct {
		Servers       []string `json:"servers"`
		SearchDomains []string `json:"searchDomains"`
	} `json:"dns"`
	PREF64 string `json:"pref64"`
	// The form saved before modes existed.
	Enabled   bool `json:"enabled"`
	Managed   bool `json:"managed"`
	OtherInfo bool `json:"otherInfo"`
}

// mode is the advertisement mode, the legacy form mapped onto it; none at all is "disabled".
func (a *advertisement) mode() string {
	switch {
	case a == nil:
		return "disabled"
	case a.Mode != "":
		return a.Mode
	case !a.Enabled:
		return "disabled"
	case a.Managed:
		return "managed"
	case a.OtherInfo:
		return "stateless"
	}
	return "unmanaged"
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
	PrefixOnly          bool           `json:"prefixOnly"`
	WithoutRA           bool           `json:"withoutRA"`
	SendRelease         bool           `json:"sendRelease"`
	DUID                string         `json:"duid"`
}
type segment struct {
	ID         string `json:"id"`
	Link       string `json:"link"`
	Zone       string `json:"zone"`
	Addressing struct {
		IPv4 ipv4 `json:"ipv4"`
		IPv6 ipv6 `json:"ipv6"`
		DNS  *struct {
			Servers       []string `json:"servers"`
			SearchDomains []string `json:"searchDomains"`
		} `json:"dns"`
	} `json:"addressing"`
}
type address struct {
	ID, Kind, Address, Prefix, First, Last string
	Members                                []string
}
type service struct {
	ID, Protocol          string
	Ports, Types, Members []string
}
type rule struct {
	ID, Action         string
	Enabled, Log       bool
	From, To, Services []string
}
type declaration struct {
	Links    []link    `json:"links"`
	Segments []segment `json:"segments"`
	Zones    []struct {
		ID string `json:"id"`
	} `json:"zones"`
	Objects struct {
		Addresses []address `json:"addresses"`
		Services  []service `json:"services"`
	} `json:"objects"`
	Policy struct {
		Input   []rule `json:"input"`
		Forward []rule `json:"forward"`
	} `json:"policy"`
	NAT struct {
		Outbound struct {
			Mode string `json:"mode"`
		} `json:"outbound"`
		PortForwards []portForward `json:"portForwards"`
		OneToOne     []oneToOne    `json:"oneToOne"`
	} `json:"nat"`
	Management *struct {
		Segment        string   `json:"segment"`
		AllowedSources []string `json:"allowedSources"`
	} `json:"management"`
	DHCP []dhcpEntry `json:"dhcp"`
	DNS  *dnsService `json:"dns"`
}

type natPoint struct {
	Self    bool   `json:"self"`
	Address string `json:"address"`
}
type portForward struct {
	ID, Protocol, Ports, FilterRule  string
	Enabled, FromNegated, NoRedirect bool
	Reflection, Log                  bool
	IngressZones, From               []string
	To                               natPoint
	Target                           *natPoint
	TargetPort                       *int
}
type oneToOne struct {
	ID, Zone, External, Internal string
	Enabled, NoBinat, Reflection bool
	Destination                  []string
}

// filtered reports that the declaration switches the packet filter on: a management segment is set.
func (d declaration) filtered() bool { return d.Management != nil && d.Management.Segment != "" }

// Input is everything a render depends on. Secrets holds real values.
type Input struct {
	Declaration json.RawMessage
	Secrets     map[string]string
	Links       []host.LinkStatus
	Facts       host.RealizerFacts
	// DHCPIdentity is the DHCP client identity to present, by lower-case permanent MAC: a host taken over
	// from dhcpcd keeps its leases only if networkd presents dhcpcd's identity.
	DHCPIdentity map[string]host.DHCPIdentity
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
