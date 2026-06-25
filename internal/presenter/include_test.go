package presenter

import "testing"

func TestParseIncludeOptions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		raw     string
		allowed map[string]struct{}
		want    includeOptions
		wantErr bool
	}{
		{
			name:    "empty",
			raw:     "",
			allowed: includeAllow("payload_refs", "proof", "cursors"),
			want:    includeOptions{},
		},
		{
			name:    "comma separated with whitespace",
			raw:     " payload_refs, proof ",
			allowed: includeAllow("payload_refs", "proof"),
			want:    includeOptions{PayloadRefs: true, Proof: true},
		},
		{
			name:    "duplicates collapse",
			raw:     "payload_refs,payload_refs",
			allowed: includeAllow("payload_refs"),
			want:    includeOptions{PayloadRefs: true},
		},
		{
			name:    "cursors",
			raw:     "cursors",
			allowed: includeAllow("cursors"),
			want:    includeOptions{Cursors: true},
		},
		{
			name:    "empty token rejected",
			raw:     "payload_refs,",
			allowed: includeAllow("payload_refs"),
			wantErr: true,
		},
		{
			name:    "unknown token rejected",
			raw:     "payload_refs,unknown",
			allowed: includeAllow("payload_refs"),
			wantErr: true,
		},
		{
			name:    "endpoint allowlist rejected",
			raw:     "payload_refs",
			allowed: includeAllow("cursors"),
			wantErr: true,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := parseIncludeOptions(tc.raw, tc.allowed)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseIncludeOptions() error = nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("parseIncludeOptions() error = %v", err)
			}
			if got != tc.want {
				t.Fatalf("parseIncludeOptions() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestRequireProofPayloadRefs(t *testing.T) {
	t.Parallel()

	if err := requireProofPayloadRefs(includeOptions{Proof: true}); err == nil {
		t.Fatalf("requireProofPayloadRefs() error = nil, want error")
	}
	if err := requireProofPayloadRefs(includeOptions{PayloadRefs: true, Proof: true}); err != nil {
		t.Fatalf("requireProofPayloadRefs() error = %v", err)
	}
}
