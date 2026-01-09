package docker

import "testing"

func TestHasDigestChanged(t *testing.T) {
	tests := []struct {
		name      string
		oldDigest string
		newDigest string
		want      bool
	}{
		{
			name:      "same digests",
			oldDigest: "sha256:abc123",
			newDigest: "sha256:abc123",
			want:      false,
		},
		{
			name:      "different digests",
			oldDigest: "sha256:abc123",
			newDigest: "sha256:def456",
			want:      true,
		},
		{
			name:      "empty old digest",
			oldDigest: "",
			newDigest: "sha256:abc123",
			want:      true,
		},
		{
			name:      "empty new digest",
			oldDigest: "sha256:abc123",
			newDigest: "",
			want:      true,
		},
		{
			name:      "both empty",
			oldDigest: "",
			newDigest: "",
			want:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := HasDigestChanged(tt.oldDigest, tt.newDigest)
			if got != tt.want {
				t.Errorf("HasDigestChanged() = %v, want %v", got, tt.want)
			}
		})
	}
}
