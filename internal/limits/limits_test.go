package limits

import "testing"

func TestContentLimits(t *testing.T) {
	tests := []struct {
		name string
		got  int64
		want int64
	}{
		{
			name: "Markdown document bytes",
			got:  MaxMarkdownDocumentBytes,
			want: 8_388_608,
		},
		{
			name: "review sidecar bytes",
			got:  MaxReviewSidecarBytes,
			want: 8_388_608,
		},
		{
			name: "relative image asset bytes",
			got:  MaxImageAssetBytes,
			want: 20_971_520,
		},
		{
			name: "retained image blob bytes per tab",
			got:  MaxRetainedImageBlobBytesPerTab,
			want: 41_943_040,
		},
		{
			name: "mutation request body bytes",
			got:  MaxMutationRequestBodyBytes,
			want: 2_097_152,
		},
		{
			name: "persisted message body bytes",
			got:  MaxPersistedMessageBodyBytes,
			want: 65_536,
		},
		{
			name: "text anchor source bytes",
			got:  MaxTextAnchorSourceBytes,
			want: 1_048_576,
		},
		{
			name: ".gitignore file bytes",
			got:  MaxGitignoreFileBytes,
			want: 1_048_576,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.got != test.want {
				t.Fatalf("limit = %d bytes, want %d bytes", test.got, test.want)
			}
		})
	}
}

func TestImageConcurrencyLimits(t *testing.T) {
	if MaxConcurrentImageStreams != 8 {
		t.Fatalf("MaxConcurrentImageStreams = %d, want 8", MaxConcurrentImageStreams)
	}
}

func TestConcurrentImageLoadLimit(t *testing.T) {
	const want = 4
	if MaxConcurrentImageLoads != want {
		t.Fatalf("MaxConcurrentImageLoads = %d, want %d", MaxConcurrentImageLoads, want)
	}
}
