// Package limits defines the fixed bounds for untrusted content and
// frontend-retained image data.
//
// These values are product and security contracts, not tunable configuration.
package limits

const (
	// MaxMarkdownDocumentBytes is the largest Markdown document mdReview reads.
	MaxMarkdownDocumentBytes int64 = 8 * 1024 * 1024

	// MaxReviewSidecarBytes is the largest review sidecar mdReview reads or writes.
	MaxReviewSidecarBytes int64 = 8 * 1024 * 1024

	// MaxImageAssetBytes is the largest single relative image asset mdReview reads.
	MaxImageAssetBytes int64 = 20 * 1024 * 1024

	// MaxRetainedImageBlobBytesPerTab bounds encoded image blobs retained for
	// one tab's active document. Navigation discards the complete cache.
	MaxRetainedImageBlobBytesPerTab int64 = 40 * 1024 * 1024

	// MaxConcurrentImageLoads bounds image asset loads for one active document.
	MaxConcurrentImageLoads = 4

	// MaxConcurrentImageStreams bounds image descriptors and reads across all
	// browser tabs served by one process.
	MaxConcurrentImageStreams = 8

	// MaxMutationRequestBodyBytes is the largest accepted mutation request body.
	MaxMutationRequestBodyBytes int64 = 2 * 1024 * 1024

	// MaxPersistedMessageBodyBytes is the largest UTF-8 message body persisted.
	MaxPersistedMessageBodyBytes int64 = 64 * 1024

	// MaxTextAnchorSourceBytes is the largest UTF-8 source stored in a new text
	// anchor.
	MaxTextAnchorSourceBytes int64 = 1 * 1024 * 1024

	// MaxGitignoreFileBytes is the largest single .gitignore file mdReview reads.
	MaxGitignoreFileBytes int64 = 1 * 1024 * 1024
)
