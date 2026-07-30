package server

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestParseReviewOperationRouteKeepsOpaqueIDsInsideOneSegment(t *testing.T) {
	tests := []struct {
		name     string
		route    reviewOperationRoute
		prefix   string
		suffix   string
		targetID string
	}{
		{"reply", reviewOperationReply, "/api/threads/", "/messages", "thread_/ %"},
		{"status", reviewOperationStatus, "/api/threads/", "/status", "."},
		{"delete", reviewOperationDelete, "/api/threads/", "", ".."},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			segment := "~" + base64.RawURLEncoding.EncodeToString([]byte(test.targetID))
			route, targetID, ok := parseReviewOperationRoute(test.prefix + segment + test.suffix)
			if !ok || route != test.route || targetID != test.targetID {
				t.Fatalf(
					"parseReviewOperationRoute() = (%v, %q, %t), want (%v, %q, true)",
					route,
					targetID,
					ok,
					test.route,
					test.targetID,
				)
			}
		})
	}
}

func TestParseReviewOperationRouteRejectsMalformedOrRouteChangingSegments(t *testing.T) {
	invalidUTF8 := "~" + base64.RawURLEncoding.EncodeToString([]byte{0xff})
	tests := []string{
		"/api/threads/thread_plain/messages",
		"/api/threads/~/messages",
		"/api/threads/~*/messages",
		"/api/threads/" + invalidUTF8 + "/messages",
		"/api/threads/~dGhyZWFk/message",
		"/api/threads/~dGhyZWFk/extra/messages",
		"/api/threads/~dGhyZWFk/",
	}

	for _, urlPath := range tests {
		t.Run(strings.ReplaceAll(urlPath, "/", "_"), func(t *testing.T) {
			if route, targetID, ok := parseReviewOperationRoute(urlPath); ok ||
				route != reviewOperationUnknown ||
				targetID != "" {
				t.Fatalf(
					"parseReviewOperationRoute(%q) = (%v, %q, %t), want rejection",
					urlPath,
					route,
					targetID,
					ok,
				)
			}
		})
	}
}
