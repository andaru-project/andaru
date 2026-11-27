package config

import (
	"errors"
	"slices"
	"strconv"

	"github.com/andaru-project/andaru/internal/authz"
)

const requestLimit = 1 << 20
const documentLimit = 4 << 20
const objectLimit = 10000
const schemaURI = "https://andaru.invalid/api/v1/schema"

// schemaVersion is the saved configuration's form: 4 since the firewall's rules are one list between zones and
// its objects are aliases. Documents of the forms before it load through upgradeLegacy.
const schemaVersion = 4

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
	Secret     string `json:"secret"`
	Unresolved bool   `json:"unresolved,omitempty"`
}
type link struct {
	identity
	Kind               string           `json:"kind"`
	MTU                int              `json:"mtu"`
	Enabled            bool             `json:"enabled"`
	PermanentMAC       string           `json:"permanentMac,omitempty"`
	Parent             string           `json:"parent,omitempty"`
	VLANID             *int             `json:"vlanId,omitempty"`
	Members            *[]string        `json:"members,omitempty"`
	Mode               string           `json:"mode,omitempty"`
	STP                *bool            `json:"stp,omitempty"`
	STPPriority        *int             `json:"stpPriority,omitempty"`
	ForwardDelay       *int             `json:"forwardDelay,omitempty"`
	HelloTime          *int             `json:"helloTime,omitempty"`
	MaxAge             *int             `json:"maxAge,omitempty"`
	MulticastSnooping  *bool            `json:"multicastSnooping,omitempty"`
	LACPRate           string           `json:"lacpRate,omitempty"`
	MIIMonitorMs       *int             `json:"miiMonitorMs,omitempty"`
	TransmitHashPolicy string           `json:"transmitHashPolicy,omitempty"`
	LLDP               string           `json:"lldp,omitempty"`
	EmitLLDP           string           `json:"emitLldp,omitempty"`
	Offloads           map[string]bool  `json:"offloads,omitempty"`
	Username           string           `json:"username,omitempty"`
	Password           *secretReference `json:"password,omitempty"`
}
type ipv4 struct {
	Mode      string   `json:"mode"`
	Addresses []string `json:"addresses,omitempty"`
	Gateway   string   `json:"gateway,omitempty"`
}
type advertisement struct {
	Mode              string `json:"mode,omitempty"`
	Priority          string `json:"priority,omitempty"`
	RouterLifetime    *int   `json:"routerLifetime,omitempty"`
	ValidLifetime     *int   `json:"validLifetime,omitempty"`
	PreferredLifetime *int   `json:"preferredLifetime,omitempty"`
	DNS               *dns   `json:"dns,omitempty"`
	PREF64            string `json:"pref64,omitempty"`
	// The form saved before modes existed. It loads unchanged, so old versions keep their bytes, and a write replaces it.
	Enabled   *bool `json:"enabled,omitempty"`
	Managed   *bool `json:"managed,omitempty"`
	OtherInfo *bool `json:"otherInfo,omitempty"`
}

// mode is the advertisement mode, the legacy form mapped onto it; none at all is "disabled".
func (a *advertisement) mode() string {
	switch {
	case a == nil:
		return "disabled"
	case a.Mode != "":
		return a.Mode
	case !*a.Enabled: // the schema requires all three legacy fields together
		return "disabled"
	case *a.Managed:
		return "managed"
	case *a.OtherInfo:
		return "stateless"
	}
	return "unmanaged"
}

type ipv6 struct {
	Mode                string         `json:"mode"`
	Addresses           []string       `json:"addresses,omitempty"`
	Gateway             string         `json:"gateway,omitempty"`
	RequestPrefixLength *int           `json:"requestPrefixLength,omitempty"`
	From                string         `json:"from,omitempty"`
	SubnetID            string         `json:"subnetId,omitempty"`
	RA                  *advertisement `json:"ra,omitempty"`
	PrefixOnly          bool           `json:"prefixOnly,omitempty"`
	WithoutRA           bool           `json:"withoutRA,omitempty"`
	SendRelease         bool           `json:"sendRelease,omitempty"`
	DUID                string         `json:"duid,omitempty"`
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
type zone struct {
	identity
	Color string `json:"color,omitempty"`
}

// firewallZone is the built-in zone that is the box itself. Nothing stores it: it has no interfaces and no
// name to change, and a rule names it as its destination to reach the firewall.
const firewallZone = "zon_firewall"

// address is an address alias: one host, network or range, a group of those, or a list fetched from a URL or
// resolved from host names every refreshSeconds, whose members are not saved.
type address struct {
	identity
	Kind           string   `json:"kind"`
	Address        string   `json:"address,omitempty"`
	Prefix         string   `json:"prefix,omitempty"`
	First          string   `json:"first,omitempty"`
	Last           string   `json:"last,omitempty"`
	Members        []string `json:"members,omitempty"`
	URL            string   `json:"url,omitempty"`
	Names          []string `json:"names,omitempty"`
	RefreshSeconds int      `json:"refreshSeconds,omitempty"`
}

// dynamic reports an alias whose members are fetched or resolved: it may hold either family.
func (a address) dynamic() bool { return a.Kind == "url" || a.Kind == "hostnames" }

// portAlias is a port alias: tcp or udp ports, icmp or icmpv6 types, or a group of those. Its ID keeps the
// svc_ prefix of the services it was saved as.
type portAlias struct {
	identity
	Protocol string   `json:"protocol"`
	Ports    []string `json:"ports,omitempty"`
	Types    []string `json:"types,omitempty"`
	Members  []string `json:"members,omitempty"`
}

// ruleSide is where a rule's traffic comes from or goes to: the zone it arrives in or leaves by, and the
// addresses within it. Both must match. No zones is any network, never the firewall; no addresses is every
// address. Not turns the addresses into "none of these".
type ruleSide struct {
	Zones     []string `json:"zones"`
	Addresses []string `json:"addresses"`
	Not       bool     `json:"not,omitempty"`
}

// rule is one firewall rule; its position in the list is its execution order.
type rule struct {
	identity
	Enabled bool     `json:"enabled"`
	Action  string   `json:"action"`
	From    ruleSide `json:"from"`
	To      ruleSide `json:"to"`
	Ports   []string `json:"ports"`
	Log     bool     `json:"log"`
}

// toFirewall reports that the rule's destination is the box, so it belongs to the input chain.
func (r rule) toFirewall() bool { return slices.Contains(r.To.Zones, firewallZone) }

type aliasSet struct {
	Addresses []address   `json:"addresses"`
	Ports     []portAlias `json:"ports"`
}
type outbound struct {
	Mode string `json:"mode"`
}
type natSettings struct {
	Outbound     outbound      `json:"outbound,omitzero"`
	PortForwards []portForward `json:"portForwards,omitempty"`
	OneToOne     []oneToOne    `json:"oneToOne,omitempty"`
}

// natAddress is a port forward's destination as the client sees it: the firewall itself or an address alias.
type natAddress struct {
	Self    bool   `json:"self,omitempty"`
	Address string `json:"address,omitempty"`
}

// natTarget is where a port forward sends a connection: the firewall itself or a literal IPv4 address.
type natTarget struct {
	Self    bool   `json:"self,omitempty"`
	Address string `json:"address,omitempty"`
}

// portForward is pfSense's port forward: first match in list order.
type portForward struct {
	identity
	Enabled      bool       `json:"enabled"`
	IngressZones []string   `json:"ingressZones"`
	Protocol     string     `json:"protocol"`
	From         []string   `json:"from"`
	FromNegated  bool       `json:"fromNegated,omitempty"`
	To           natAddress `json:"to"`
	ToNegated    bool       `json:"toNegated,omitempty"`
	Ports        string     `json:"ports"`
	NoRedirect   bool       `json:"noRedirect,omitempty"`
	Target       *natTarget `json:"target,omitempty"`
	TargetPort   *int       `json:"targetPort,omitempty"`
	FilterRule   string     `json:"filterRule,omitempty"`
	Reflection   bool       `json:"reflection,omitempty"`
	Log          bool       `json:"log,omitempty"`
	// Family is ipv4 when empty; an ipv6 forward reaches only the firewall itself.
	Family string `json:"family,omitempty"`
}

// oneToOne is pfSense's 1:1 NAT: one address or prefix mapped both ways.
type oneToOne struct {
	identity
	Enabled            bool     `json:"enabled"`
	Zone               string   `json:"zone"`
	External           string   `json:"external,omitempty"`
	Internal           string   `json:"internal"`
	Destination        []string `json:"destination"`
	DestinationNegated bool     `json:"destinationNegated,omitempty"`
	NoBinat            bool     `json:"noBinat,omitempty"`
	Reflection         bool     `json:"reflection,omitempty"`
}
type management struct {
	Segment        string   `json:"segment"`
	AllowedSources []string `json:"allowedSources,omitempty"`
}

// dhcp6Entry serves DHCPv6 on one segment with a static /64, and may delegate prefixes to routers on it.
type dhcp6Entry struct {
	identity
	Segment          string            `json:"segment"`
	Ranges           []dhcpRange       `json:"ranges,omitempty"`
	LeaseTime        int               `json:"leaseTime,omitzero"`
	DNSServers       []string          `json:"dnsServers,omitempty"`
	DomainSearch     []string          `json:"domainSearch,omitempty"`
	NTPServers       []string          `json:"ntpServers,omitempty"`
	Options          []dhcpOption      `json:"options,omitempty"`
	StaticLeases     []staticLease6    `json:"staticLeases,omitempty"`
	PrefixDelegation *prefixDelegation `json:"prefixDelegation,omitempty"`
}
type staticLease6 struct {
	DUID    string `json:"duid"`
	Address string `json:"address,omitempty"`
	Prefix  string `json:"prefix,omitempty"`
	// PrefixID is a position in the pool on a delegated segment, whose prefix changes with the provider's.
	PrefixID string `json:"prefixId,omitempty"`
	Hostname string `json:"hostname,omitempty"`
}

// prefixDelegation is a literal pool on a static segment, or on a delegated one a pool at SubnetID of Length
// under the WAN's prefix.
type prefixDelegation struct {
	Prefix          string `json:"prefix,omitempty"`
	SubnetID        string `json:"subnetId,omitempty"`
	Length          int    `json:"length,omitzero"`
	DelegatedLength int    `json:"delegatedLength"`
	ExcludedPrefix  string `json:"excludedPrefix,omitempty"`
}

// dhcpEntry serves DHCPv4 on one segment.
type dhcpEntry struct {
	identity
	Segment      string        `json:"segment"`
	Ranges       []dhcpRange   `json:"ranges"`
	LeaseTime    int           `json:"leaseTime,omitzero"`
	Gateway      string        `json:"gateway,omitempty"`
	DNSServers   []string      `json:"dnsServers,omitempty"`
	DomainName   string        `json:"domainName,omitempty"`
	DomainSearch []string      `json:"domainSearch,omitempty"`
	NTPServers   []string      `json:"ntpServers,omitempty"`
	NextServer   string        `json:"nextServer,omitempty"`
	BootFileName string        `json:"bootFileName,omitempty"`
	Options      []dhcpOption  `json:"options,omitempty"`
	StaticLeases []staticLease `json:"staticLeases,omitempty"`
}
type dhcpOption struct {
	Code       int    `json:"code"`
	Type       string `json:"type"`
	Data       string `json:"data"`
	AlwaysSend bool   `json:"alwaysSend,omitempty"`
}
type dhcpRange struct {
	First string `json:"first"`
	Last  string `json:"last"`
}
type staticLease struct {
	MAC      string `json:"mac"`
	Address  string `json:"address"`
	Hostname string `json:"hostname,omitempty"`
}

// dnsService is the DNS service for the firewall and the segments it serves; it names no backend.
type dnsService struct {
	Mode           string     `json:"mode"`
	Upstreams      []upstream `json:"upstreams,omitempty"`
	Segments       []string   `json:"segments"`
	LocalDomain    string     `json:"localDomain,omitempty"`
	SearchDomains  []string   `json:"searchDomains,omitempty"`
	RegisterLeases *bool      `json:"registerLeases,omitempty"`
	// VirtualAddresses the DNS service also listens on.
	VirtualAddresses []string `json:"virtualAddresses,omitempty"`
}
type upstream struct {
	Address  string `json:"address"`
	Protocol string `json:"protocol"`
	TLSName  string `json:"tlsName,omitempty"`
}

// The filter sections are omitted while empty, so a declaration without a filter keeps its form.
type declaration struct {
	SchemaVersion int          `json:"schemaVersion"`
	Links         []link       `json:"links"`
	Segments      []segment    `json:"segments"`
	Zones         []zone       `json:"zones"`
	Roles         []authz.Role `json:"roles,omitzero"`
	Aliases       aliasSet     `json:"aliases,omitzero"`
	Rules         []rule       `json:"rules,omitzero"`
	NAT           natSettings  `json:"nat,omitzero"`
	Management    *management  `json:"management,omitempty"`
	DHCP          []dhcpEntry  `json:"dhcp,omitzero"`
	DHCP6         []dhcp6Entry `json:"dhcp6,omitzero"`
	DNS           *dnsService  `json:"dns,omitempty"`
	// VirtualAddresses are held by the firewall on no LAN, for its own services.
	VirtualAddresses []virtualAddress `json:"virtualAddresses,omitzero"`
	Notifications    notifications    `json:"notifications,omitzero"`
	Addons           []addonConfig    `json:"addons,omitempty"`
}

type addonInterface struct {
	Name string `json:"name"`
	Zone string `json:"zone,omitempty"`
}

type addonBusGrants struct {
	Publish   map[string]string `json:"publish,omitempty"`
	Subscribe map[string]string `json:"subscribe,omitempty"`
}

type addonConfig struct {
	ID         string           `json:"id"`
	Enabled    bool             `json:"enabled"`
	Settings   map[string]any   `json:"settings,omitempty"`
	Interfaces []addonInterface `json:"interfaces,omitempty"`
	BusGrants  addonBusGrants   `json:"busGrants,omitzero"`
}

type notifications struct {
	Channels []channel `json:"channels,omitempty"`
}

// channel is where notifications are sent. Enabled and the filter's minimum severity get their defaults on save.
type channel struct {
	identity
	Enabled  *bool         `json:"enabled,omitempty"`
	Kind     string        `json:"kind"`
	Filter   channelFilter `json:"filter"`
	Webhook  *webhook      `json:"webhook,omitempty"`
	Ntfy     *ntfy         `json:"ntfy,omitempty"`
	Pushover *pushover     `json:"pushover,omitempty"`
	Telegram *telegram     `json:"telegram,omitempty"`
	Email    *email        `json:"email,omitempty"`
}
type channelFilter struct {
	MinSeverity string   `json:"minSeverity,omitempty"`
	Sources     []string `json:"sources,omitempty"`
}

// webhook's URL is a secret, since webhook URLs carry tokens; Authorization is the Authorization header's value.
type webhook struct {
	URL           secretReference  `json:"url"`
	Authorization *secretReference `json:"authorization,omitempty"`
}

// ntfy is a topic on an ntfy server; Token, when set, is sent as a bearer token.
type ntfy struct {
	Server string           `json:"server"`
	Topic  string           `json:"topic"`
	Token  *secretReference `json:"token,omitempty"`
}

// pushover is a Pushover application's token and the user key it sends to.
type pushover struct {
	Token secretReference `json:"token"`
	User  secretReference `json:"user"`
}

// telegram is a Telegram bot's token and the chat it sends to.
type telegram struct {
	Token  secretReference `json:"token"`
	ChatID string          `json:"chatId"`
}

// email is an SMTP server, reached over implicit TLS or STARTTLS, and the addresses a message is sent from and to.
// Username and Password are both set or both left out.
type email struct {
	Host     string           `json:"host"`
	Port     int              `json:"port"`
	Security string           `json:"security"`
	Username string           `json:"username,omitempty"`
	Password *secretReference `json:"password,omitempty"`
	From     string           `json:"from"`
	To       []string         `json:"to"`
}

// channelSecret is one of a channel's secret references, at its path below the channel; an optional one left out is nil.
type channelSecret struct {
	path string
	ref  *secretReference
}

// secrets are c's secret references, with the optional ones it leaves out.
func (c *channel) secrets() []channelSecret {
	switch {
	case c.Webhook != nil:
		return []channelSecret{{"/webhook/url", &c.Webhook.URL}, {"/webhook/authorization", c.Webhook.Authorization}}
	case c.Ntfy != nil:
		return []channelSecret{{"/ntfy/token", c.Ntfy.Token}}
	case c.Pushover != nil:
		return []channelSecret{{"/pushover/token", &c.Pushover.Token}, {"/pushover/user", &c.Pushover.User}}
	case c.Telegram != nil:
		return []channelSecret{{"/telegram/token", &c.Telegram.Token}}
	case c.Email != nil:
		return []channelSecret{{"/email/password", c.Email.Password}}
	}
	return nil
}

// virtualAddress is an address the firewall holds on its dummy interface, answered on every segment the filter lets reach it;
// or, of kind proxyArp, an address or prefix the firewall answers ARP for on one segment's link without holding it;
// or, of kind proxyNdp, an inside host's IPv6 address the firewall answers neighbour discovery for on one segment's
// link and routes to the host's segment.
type virtualAddress struct {
	identity
	Kind        string `json:"kind,omitempty"`
	Address     string `json:"address"`
	Segment     string `json:"segment,omitempty"`
	HostSegment string `json:"hostSegment,omitempty"`
}

// proxied says whether v is answered by proxy ARP rather than held by the firewall.
func (v virtualAddress) proxied() bool { return v.Kind == "proxyArp" }

// proxyNDP says whether v is an inside host's address answered by proxy NDP and routed to its host segment.
func (v virtualAddress) proxyNDP() bool { return v.Kind == "proxyNdp" }

// held says whether the firewall holds v: neither proxy ARP nor proxy NDP answers for it.
func (v virtualAddress) held() bool { return !v.proxied() && !v.proxyNDP() }

func emptyDeclaration() declaration {
	return declaration{SchemaVersion: schemaVersion, Links: []link{}, Segments: []segment{}, Zones: []zone{}}
}
func linkMembers(l link) []string {
	if l.Members == nil {
		return nil
	}
	return *l.Members
}
