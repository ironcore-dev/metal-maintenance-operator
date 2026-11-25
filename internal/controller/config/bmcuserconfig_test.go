// SPDX-FileCopyrightText: SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package config

import "testing"

func TestParseBMCUserConfig(t *testing.T) {
	cases := []struct {
		name    string
		yaml    string
		wantErr bool
	}{
		{
			name: "valid full config",
			yaml: `
apiVersion: bmcuser.metal.ironcore.dev/v1alpha1
defaultPasswordLength: 24
vendors:
  "Dell Inc.":
    maxPasswordLength: 20
    specialChars: "!#$%*.?-@_"
`,
		},
		{
			name: "valid minimal config (only apiVersion)",
			yaml: `apiVersion: bmcuser.metal.ironcore.dev/v1alpha1`,
		},
		{
			name:    "wrong apiVersion",
			yaml:    `apiVersion: wrong/v1`,
			wantErr: true,
		},
		{
			name: "unknown key rejected (strict)",
			yaml: `
apiVersion: bmcuser.metal.ironcore.dev/v1alpha1
bogusKey: 1
`,
			wantErr: true,
		},
		{
			name: "defaultPasswordLength below minimum",
			yaml: `
apiVersion: bmcuser.metal.ironcore.dev/v1alpha1
defaultPasswordLength: 4
`,
			wantErr: true,
		},
		{
			name: "vendor maxPasswordLength above maximum",
			yaml: `
apiVersion: bmcuser.metal.ironcore.dev/v1alpha1
vendors:
  "Dell Inc.":
    maxPasswordLength: 999
`,
			wantErr: true,
		},
		{
			name: "empty specialChars rejected",
			yaml: `
apiVersion: bmcuser.metal.ironcore.dev/v1alpha1
vendors:
  "Dell Inc.":
    specialChars: ""
`,
			wantErr: true,
		},
		{
			name: "alphanumeric in specialChars rejected",
			yaml: `
apiVersion: bmcuser.metal.ironcore.dev/v1alpha1
vendors:
  "Dell Inc.":
    specialChars: "!#a$"
`,
			wantErr: true,
		},
		{
			name: "non-alphanumeric specialChars accepted",
			yaml: `
apiVersion: bmcuser.metal.ironcore.dev/v1alpha1
vendors:
  "Dell Inc.":
    specialChars: "!@"
`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseBMCUserConfig([]byte(tc.yaml))
			if tc.wantErr && err == nil {
				t.Fatalf("ParseBMCUserConfig(%q) = nil error, want error", tc.name)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("ParseBMCUserConfig(%q) = %v, want nil", tc.name, err)
			}
		})
	}
}

func TestParseBMCUserConfigValues(t *testing.T) {
	cfg, err := ParseBMCUserConfig([]byte(`
apiVersion: bmcuser.metal.ironcore.dev/v1alpha1
defaultPasswordLength: 24
vendors:
  "Dell Inc.":
    maxPasswordLength: 20
    specialChars: "!#$%*.?-@_"
`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.DefaultPasswordLength == nil || *cfg.DefaultPasswordLength != 24 {
		t.Fatalf("DefaultPasswordLength = %v, want 24", cfg.DefaultPasswordLength)
	}
	dell, ok := cfg.Vendors["Dell Inc."]
	if !ok {
		t.Fatal(`expected a "Dell Inc." vendor entry`)
	}
	if dell.MaxPasswordLength == nil || *dell.MaxPasswordLength != 20 {
		t.Fatalf("Dell MaxPasswordLength = %v, want 20", dell.MaxPasswordLength)
	}
	if dell.SpecialChars == nil || *dell.SpecialChars != "!#$%*.?-@_" {
		t.Fatalf("Dell SpecialChars = %v, want the configured set", dell.SpecialChars)
	}
}
