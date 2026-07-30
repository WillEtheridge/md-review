package review

import "bytes"

// ResolveAnchor calculates an anchor's current attachment without changing its
// persisted original range. Empty source and invalid persisted ranges are
// detached here; Decode rejects them when they occur in a sidecar.
func ResolveAnchor(markdown []byte, anchor Anchor) Attachment {
	if anchor.Type == AnchorDocument {
		return Attachment{State: AttachmentDocument}
	}
	if anchor.Type != AnchorText || anchor.Range == nil || anchor.Source == "" {
		return Attachment{State: AttachmentDetached}
	}

	start := anchor.Range.Start
	end := anchor.Range.End
	if start > end ||
		end > uint64(len(markdown)) ||
		end-start != uint64(len(anchor.Source)) {
		return Attachment{State: AttachmentDetached}
	}
	if bytes.Equal(markdown[int(start):int(end)], []byte(anchor.Source)) {
		return attached(start, end)
	}

	// If the original range no longer matches, allow reattachment only when the
	// exact source occurs once. A repeated match would require guessing which
	// occurrence the reviewer meant, so it remains detached.
	source := []byte(anchor.Source)
	first := bytes.Index(markdown, source)
	if first < 0 {
		return Attachment{State: AttachmentDetached}
	}
	if bytes.Index(markdown[first+1:], source) >= 0 {
		return Attachment{State: AttachmentDetached}
	}
	return attached(uint64(first), uint64(first+len(source)))
}

func attached(start, end uint64) Attachment {
	return Attachment{
		State:        AttachmentAttached,
		CurrentRange: &ByteRange{Start: start, End: end},
	}
}
