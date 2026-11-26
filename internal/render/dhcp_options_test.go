package render

import (
	"encoding/json"
	"slices"
	"testing"
)

func TestDHCPOptionsRender(t *testing.T) {
	in := change(t, fixture(t, "services-recursive"), func(d map[string]any) {
		e := item(d, "dhcp", 0)
		e["gateway"] = "192.168.40.254"
		e["domainName"] = "override.example"
		e["domainSearch"] = []any{"override.example", "corp.example"}
		e["ntpServers"] = []any{"192.168.40.123", "192.168.40.124"}
		e["nextServer"] = "192.168.40.10"
		e["bootFileName"] = "pxelinux.0"
		e["options"] = []any{
			map[string]any{"code": 66, "type": "string", "data": "tftp.lan"},
			map[string]any{"code": 150, "type": "ip", "data": "192.168.40.10, 192.168.40.11"},
			map[string]any{"code": 224, "type": "hex", "data": "aabbcc", "alwaysSend": true},
			map[string]any{"code": 225, "type": "uint8", "data": "7"},
			map[string]any{"code": 226, "type": "uint16", "data": "1024"},
			map[string]any{"code": 227, "type": "uint32", "data": "86400"},
			map[string]any{"code": 228, "type": "boolean", "data": "true"},
			map[string]any{"code": 229, "type": "boolean", "data": "false"},
		}
	})
	res := render(t, in)
	if !res.Realizable {
		t.Fatalf("unexpected findings: %+v", res.Findings)
	}
	idx := slices.IndexFunc(res.Files, func(f File) bool { return f.Path == keaPath })
	if idx < 0 {
		t.Fatal("missing kea-dhcp4.conf")
	}
	var doc struct {
		Dhcp4 struct {
			Subnet4 []struct {
				NextServer   string           `json:"next-server"`
				BootFileName string           `json:"boot-file-name"`
				Options      []map[string]any `json:"option-data"`
				UserContext  struct {
					Andaru struct {
						Segment string `json:"segment"`
						Domain  string `json:"domain"`
					} `json:"andaru"`
				} `json:"user-context"`
			} `json:"subnet4"`
		}
	}
	if err := json.Unmarshal([]byte(res.Files[idx].Content), &doc); err != nil {
		t.Fatal(err)
	}
	s := doc.Dhcp4.Subnet4[0]
	if s.NextServer != "192.168.40.10" || s.BootFileName != "pxelinux.0" {
		t.Fatalf("boot fields: %+v", s)
	}
	// DNS registration domain stays dns.localDomain even when DHCP hands out a custom domainName.
	if s.UserContext.Andaru.Domain != "home.arpa" || s.UserContext.Andaru.Segment == "" {
		t.Fatalf("user-context: %+v", s.UserContext.Andaru)
	}
	byName := map[string]string{}
	byCode := map[int]map[string]any{}
	for _, o := range s.Options {
		if n, ok := o["name"].(string); ok {
			byName[n] = o["data"].(string)
		}
		if c, ok := o["code"].(float64); ok {
			byCode[int(c)] = o
		}
	}
	for k, want := range map[string]string{
		"routers":       "192.168.40.254",
		"domain-name":   "override.example",
		"domain-search": "override.example, corp.example",
		"ntp-servers":   "192.168.40.123, 192.168.40.124",
	} {
		if byName[k] != want {
			t.Errorf("option %s = %q, want %q", k, byName[k], want)
		}
	}
	for code, want := range map[int]string{
		66:  "746674702e6c616e",
		150: "c0a8280ac0a8280b",
		224: "aabbcc",
		225: "07",
		226: "0400",
		227: "00015180",
		228: "01",
		229: "00",
	} {
		o := byCode[code]
		if o == nil || o["csv-format"] != false || o["data"] != want {
			t.Errorf("code %d = %#v, want hex %q", code, o, want)
		}
	}
	if byCode[224]["always-send"] != true || byCode[66]["always-send"] != nil {
		t.Errorf("always-send mismatch: 224=%v 66=%v", byCode[224], byCode[66])
	}
}

func TestDHCP6OptionsRender(t *testing.T) {
	in := change(t, fixture(t, "dhcp6"), func(d map[string]any) {
		e := item(d, "dhcp6", 0)
		e["domainSearch"] = []any{"v6.example", "corp.example"}
		e["ntpServers"] = []any{"fd00:1::123"}
		e["options"] = []any{
			map[string]any{"code": 200, "type": "ip", "data": "fd00:1::5", "alwaysSend": true},
		}
	})
	res := render(t, in)
	if !res.Realizable {
		t.Fatalf("unexpected findings: %+v", res.Findings)
	}
	idx := slices.IndexFunc(res.Files, func(f File) bool { return f.Path == kea6Path })
	if idx < 0 {
		t.Fatal("missing kea-dhcp6.conf")
	}
	var doc struct {
		Dhcp6 struct {
			Subnet6 []struct {
				Options []map[string]any `json:"option-data"`
			} `json:"subnet6"`
		}
	}
	if err := json.Unmarshal([]byte(res.Files[idx].Content), &doc); err != nil {
		t.Fatal(err)
	}
	opts := doc.Dhcp6.Subnet6[0].Options
	var gotSearch, gotSNTP, gotCustom string
	var gotAlways any
	for _, o := range opts {
		if o["name"] == "domain-search" {
			gotSearch = o["data"].(string)
		}
		if o["name"] == "sntp-servers" {
			gotSNTP = o["data"].(string)
		}
		if c, ok := o["code"].(float64); ok && int(c) == 200 {
			gotCustom = o["data"].(string)
			gotAlways = o["always-send"]
		}
	}
	if gotSearch != "v6.example, corp.example" || gotSNTP != "fd00:1::123" || gotCustom != "fd000001000000000000000000000005" || gotAlways != true {
		t.Fatalf("unexpected dhcp6 options: search=%q sntp=%q custom=%q always=%v", gotSearch, gotSNTP, gotCustom, gotAlways)
	}
}
