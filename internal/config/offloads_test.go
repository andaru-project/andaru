package config

import (
	"bytes"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

func TestOffloadsSchema(t *testing.T) {
	set, err := schemas()
	if err != nil {
		t.Fatal(err)
	}
	linkSchema := set.objects["links"]
	validate := func(raw string) error {
		t.Helper()
		v, err := jsonschema.UnmarshalJSON(bytes.NewReader([]byte(raw)))
		if err != nil {
			t.Fatal(err)
		}
		return linkSchema.Validate(v)
	}
	const eth = `{"name":"eth0","kind":"ethernet","mtu":1500,"enabled":true,"permanentMac":"52:54:00:00:00:01","offloads":`
	if err := validate(eth + `{"receiveChecksum":true,"transmitChecksum":false,"tcpSegmentation":false,"tcp6Segmentation":false,"genericSegmentation":true,"genericReceive":true,"genericReceiveHardware":false,"largeReceive":false,"vlanReceiveAcceleration":true,"vlanTransmitAcceleration":true,"vlanReceiveFilter":true,"ntupleFilter":false}}`); err != nil {
		t.Fatalf("every managed offload must validate: %v", err)
	}
	for _, tc := range []struct{ name, raw, path string }{
		{"unknown offload", eth + `{"tso":false}}`, ""},
		{"not a boolean", eth + `{"largeReceive":"off"}}`, "/offloads/largeReceive"},
		{"on a VLAN", `{"name":"vl10","kind":"vlan","mtu":1500,"enabled":true,"parent":"@p0","vlanId":10,"offloads":{"largeReceive":false}}`, ""},
		{"on a bridge", `{"name":"br0","kind":"bridge","mtu":1500,"enabled":true,"members":[],"offloads":{}}`, ""},
	} {
		err := validate(tc.raw)
		if err == nil {
			t.Fatalf("%s: expected a rejection", tc.name)
		}
		if tc.path == "" {
			continue
		}
		ve, ok := schemaErrors(err).(validationError)
		if !ok || !func() bool {
			for _, d := range ve {
				if d.Path == tc.path {
					return true
				}
			}
			return false
		}() {
			t.Fatalf("%s: want path %s, got %v", tc.name, tc.path, schemaErrors(err))
		}
	}
}
