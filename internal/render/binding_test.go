package render

import (
	"slices"
	"testing"
)

func TestBindingFindingsMatchRendererIdentityPolicy(t *testing.T) {
	base := fixture(t, "bridge")
	cases := []struct {
		name string
		edit func(Input) Input
		code string
		path string
	}{
		{
			name: "missing hardware",
			edit: func(in Input) Input { in.Links = in.Links[1:]; return in },
			code: "HARDWARE_MISSING",
			path: "/links/0/permanentMac",
		},
		{
			name: "ambiguous permanent address",
			edit: func(in Input) Input {
				in.Links = append(slices.Clone(in.Links), links(observed{"ens20", "", "52:54:00:aa:bb:01"})...)
				return in
			},
			code: "HARDWARE_AMBIGUOUS",
			path: "/links/0/permanentMac",
		},
		{
			name: "derived name collision",
			edit: func(in Input) Input {
				in.Links = append(slices.Clone(in.Links), links(observed{"br000000040000", "dummy", "02:00:00:00:00:09"})...)
				return in
			},
			code: "NAME_COLLISION",
			path: "/links/3",
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			input := test.edit(base)
			findings, err := BindingFindings(input.Declaration, input.Links)
			if err != nil { t.Fatal(err) }
			if !slices.ContainsFunc(findings, func(f Finding) bool {
				return f.Severity == "error" && f.Code == test.code && f.Path == test.path
			}) {
				t.Fatalf("binding finding missing: %+v", findings)
			}
			for _, finding := range findings {
				if finding.Code != "HARDWARE_MISSING" && finding.Code != "HARDWARE_AMBIGUOUS" && finding.Code != "NAME_COLLISION" {
					t.Fatalf("unrelated render finding leaked: %+v", finding)
				}
			}
		})
	}
}

func TestBindingFindingsRejectMalformedDeclaration(t *testing.T) {
	if _, err := BindingFindings([]byte(`{"links":`), nil); err == nil {
		t.Fatal("malformed declaration accepted")
	}
}
