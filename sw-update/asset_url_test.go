package main

import (
	"strings"
	"testing"
)

func TestTemplateAsset(t *testing.T) {
	tests := []struct {
		tmpl, version     string
		wantName, wantURL string
		wantErr           string
	}{
		{
			tmpl: "https://x.com/aocl-{major}-{minor}/AOCL_Windows-setup-{version}-AMD.exe", version: "6.0.0",
			wantName: "AOCL_Windows-setup-6.0.0-AMD.exe", wantURL: "https://x.com/aocl-6-0/AOCL_Windows-setup-6.0.0-AMD.exe",
		},
		{
			tmpl: "https://x.com/{major}.{minor}.{patch}/a.zip", version: "v1.2.3",
			wantName: "a.zip", wantURL: "https://x.com/1.2.3/a.zip",
		},
		{tmpl: "https://x.com/{version}/a.zip?dl=1", version: "1.0", wantName: "a.zip", wantURL: "https://x.com/1.0/a.zip?dl=1"},
		{tmpl: "https://x.com/a-{patch}.zip", version: "1.0", wantErr: "no segment 3"},
		{tmpl: "https://x.com/a-{build}.zip", version: "1.0", wantErr: "unknown placeholder {build}"},
		{tmpl: "https://x.com/", version: "1.0", wantErr: "no file name"},
	}
	for _, tt := range tests {
		t.Run(tt.tmpl, func(t *testing.T) {
			got, err := templateAsset(tt.tmpl, tt.version)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want one containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("templateAsset: %v", err)
			}
			if got.Name != tt.wantName || got.URL != tt.wantURL {
				t.Errorf("got %+v, want name %q url %q", got, tt.wantName, tt.wantURL)
			}
		})
	}
}
