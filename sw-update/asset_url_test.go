package main

import (
	"strings"
	"testing"
)

func TestTemplateAsset(t *testing.T) {
	tests := []struct {
		name, tmpl, version string
		wantName, wantURL   string
		wantErr             string
	}{
		{
			name: "aocl", tmpl: "https://x.com/aocl-{major}-{minor}/AOCL_Windows-setup-{version}-AMD.exe", version: "6.0.0",
			wantName: "AOCL_Windows-setup-6.0.0-AMD.exe", wantURL: "https://x.com/aocl-6-0/AOCL_Windows-setup-6.0.0-AMD.exe",
		},
		{
			name: "segments drop a leading v, version keeps it", tmpl: "https://x.com/{major}.{minor}.{patch}/a-{version}.zip", version: "V1.2.3",
			wantName: "a-V1.2.3.zip", wantURL: "https://x.com/1.2.3/a-V1.2.3.zip",
		},
		{name: "query string", tmpl: "https://x.com/{version}/a.zip?dl=1", version: "1.0", wantName: "a.zip", wantURL: "https://x.com/1.0/a.zip?dl=1"},
		{
			name: "sourceforge /download suffix", tmpl: "https://sf.net/projects/x/files/a-{version}.zip/download", version: "1.0",
			wantName: "a-1.0.zip", wantURL: "https://sf.net/projects/x/files/a-1.0.zip/download",
		},
		{
			name: "URL-special characters are escaped", tmpl: "https://x.com/dl/a-{version}.exe", version: "1.0#x/../y?",
			wantName: "a-1.0#x/../y?.exe", wantURL: "https://x.com/dl/a-1.0%23x%2F..%2Fy%3F.exe",
		},
		{name: "missing segment", tmpl: "https://x.com/a-{patch}.zip", version: "1.0", wantErr: "{patch}: version \"1.0\" has no segment 3"},
		{name: "unknown placeholder", tmpl: "https://x.com/a-{build}.zip", version: "1.0", wantErr: "unknown placeholder {build}"},
		{name: "first error wins", tmpl: "https://x.com/{build}/a-{patch}.zip", version: "1.0", wantErr: "unknown placeholder {build}"},
		{name: "unbalanced brace", tmpl: "https://x.com/a-{major-{minor}.zip", version: "1.0", wantErr: "unbalanced brace"},
		{name: "empty version", tmpl: "https://x.com/a-{version}.zip", version: "", wantErr: "no version"},
		{name: "root path", tmpl: "https://x.com/", version: "1.0", wantErr: "no file name"},
		{name: "no path", tmpl: "https://x.com", version: "1.0", wantErr: "no file name"},
		{name: "trailing slash", tmpl: "https://x.com/dl/{version}/", version: "1.0", wantErr: "no file name"},
		{name: "unparseable", tmpl: "https://x.com/%zz/a-{version}.zip", version: "1.0", wantErr: "asset_url:"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
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
