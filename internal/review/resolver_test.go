package review

import "testing"

func TestResolveAnchorMatrix(t *testing.T) {
	tests := []struct {
		name     string
		markdown string
		anchor   Anchor
		state    AttachmentState
		start    uint64
		end      uint64
	}{
		{
			name:     "document",
			markdown: "# title\n",
			anchor:   Anchor{Type: AnchorDocument},
			state:    AttachmentDocument,
		},
		{
			name:     "original",
			markdown: "before exact after",
			anchor:   textAnchor(7, 12, "exact"),
			state:    AttachmentAttached,
			start:    7,
			end:      12,
		},
		{
			name:     "original wins when duplicate now exists",
			markdown: "exact before exact",
			anchor:   textAnchor(13, 18, "exact"),
			state:    AttachmentAttached,
			start:    13,
			end:      18,
		},
		{
			name:     "unique move",
			markdown: "prefix moved suffix",
			anchor:   textAnchor(0, 5, "moved"),
			state:    AttachmentAttached,
			start:    7,
			end:      12,
		},
		{
			name:     "missing",
			markdown: "different",
			anchor:   textAnchor(0, 5, "exact"),
			state:    AttachmentDetached,
		},
		{
			name:     "duplicate",
			markdown: "exact and exact",
			anchor:   textAnchor(20, 25, "exact"),
			state:    AttachmentDetached,
		},
		{
			name:     "invalid reversed range",
			markdown: "unique",
			anchor:   textAnchor(4, 1, "unique"),
			state:    AttachmentDetached,
		},
		{
			name:     "invalid out of bounds range",
			markdown: "a unique value",
			anchor:   textAnchor(99, 105, "unique"),
			state:    AttachmentDetached,
		},
		{
			name:     "range length does not match source",
			markdown: "a unique value",
			anchor:   textAnchor(2, 9, "unique"),
			state:    AttachmentDetached,
		},
		{
			name:     "empty source",
			markdown: "anything",
			anchor:   textAnchor(0, 0, ""),
			state:    AttachmentDetached,
		},
		{
			name:     "LF",
			markdown: "one\ntwo\n",
			anchor:   textAnchor(4, 7, "two"),
			state:    AttachmentAttached,
			start:    4,
			end:      7,
		},
		{
			name:     "CRLF",
			markdown: "one\r\ntwo\r\n",
			anchor:   textAnchor(5, 8, "two"),
			state:    AttachmentAttached,
			start:    5,
			end:      8,
		},
		{
			name:     "multibyte UTF-8 bytes",
			markdown: "aé界🙂z",
			anchor:   textAnchor(3, 10, "界🙂"),
			state:    AttachmentAttached,
			start:    3,
			end:      10,
		},
		{
			name:     "overlapping duplicate",
			markdown: "aaa",
			anchor:   textAnchor(9, 11, "aa"),
			state:    AttachmentDetached,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var originalRange *ByteRange
			if test.anchor.Range != nil {
				copy := *test.anchor.Range
				originalRange = &copy
			}
			got := ResolveAnchor([]byte(test.markdown), test.anchor)
			if got.State != test.state {
				t.Fatalf("state = %q, want %q", got.State, test.state)
			}
			if test.state != AttachmentAttached {
				if got.CurrentRange != nil {
					t.Fatalf("current range = %+v, want nil", got.CurrentRange)
				}
				return
			}
			if got.CurrentRange == nil ||
				got.CurrentRange.Start != test.start ||
				got.CurrentRange.End != test.end {
				t.Fatalf("current range = %+v, want [%d,%d)", got.CurrentRange, test.start, test.end)
			}
			if originalRange != nil &&
				(test.anchor.Range.Start != originalRange.Start ||
					test.anchor.Range.End != originalRange.End) {
				t.Fatal("resolver mutated persisted anchor")
			}
		})
	}
}

func textAnchor(start, end uint64, source string) Anchor {
	return Anchor{
		Type:   AnchorText,
		Range:  &ByteRange{Start: start, End: end},
		Source: source,
		Text:   source,
	}
}
