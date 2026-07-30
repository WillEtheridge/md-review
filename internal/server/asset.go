package server

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"

	"mdreview.dev/mdreview/internal/limits"
	"mdreview.dev/mdreview/internal/workspace"
)

var errAssetUnsupportedType = errors.New("asset type is unsupported")

func (server *Server) serveWorkspaceAsset(
	response http.ResponseWriter,
	request *http.Request,
) {
	documentPath, reference, ok := assetQuery(request)
	if !ok {
		server.writeError(
			response,
			http.StatusBadRequest,
			"invalidAssetRequest",
			"Choose one relative image from the current Markdown document.",
		)
		return
	}

	select {
	case server.assetPermits <- struct{}{}:
		defer func() {
			<-server.assetPermits
		}()
	case <-request.Context().Done():
		return
	}
	started := false
	err := server.workspace.ReadAsset(
		request.Context(),
		documentPath,
		reference,
		func(reader io.Reader, _ int64) error {
			prefix, err := io.ReadAll(io.LimitReader(reader, 512))
			if err != nil {
				return err
			}
			contentType, ok := allowedImageContentType(prefix)
			if !ok {
				return errAssetUnsupportedType
			}

			response.Header().Set("Content-Type", contentType)
			response.WriteHeader(http.StatusOK)
			started = true

			bounded := &io.LimitedReader{
				R: io.MultiReader(bytes.NewReader(prefix), reader),
				N: limits.MaxImageAssetBytes + 1,
			}
			written, copyErr := io.CopyBuffer(response, bounded, make([]byte, 32*1024))
			if written > limits.MaxImageAssetBytes {
				panic(http.ErrAbortHandler)
			}
			if copyErr != nil {
				if request.Context().Err() != nil {
					return request.Context().Err()
				}
				panic(http.ErrAbortHandler)
			}
			return nil
		},
	)
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return
	}
	if started {
		panic(http.ErrAbortHandler)
	}
	switch {
	case errors.Is(err, workspace.ErrAssetNotFound):
		server.writeError(
			response,
			http.StatusNotFound,
			"assetNotFound",
			"This image could not be found safely inside the workspace.",
		)
	case errors.Is(err, workspace.ErrAssetTooLarge):
		server.writeError(
			response,
			http.StatusRequestEntityTooLarge,
			"assetTooLarge",
			"This image is larger than 20 MiB.",
		)
	case errors.Is(err, errAssetUnsupportedType):
		server.writeError(
			response,
			http.StatusUnsupportedMediaType,
			"assetUnsupportedType",
			"Use a PNG, JPEG, GIF, or WebP image.",
		)
	default:
		server.writeError(
			response,
			http.StatusInternalServerError,
			"assetUnavailable",
			"This image is temporarily unavailable.",
		)
	}
}

func assetQuery(request *http.Request) (string, string, bool) {
	values, err := url.ParseQuery(request.URL.RawQuery)
	if err != nil ||
		len(values) != 2 ||
		len(values["documentPath"]) != 1 ||
		len(values["reference"]) != 1 ||
		values.Get("documentPath") == "" ||
		values.Get("reference") == "" {
		return "", "", false
	}
	return values.Get("documentPath"), values.Get("reference"), true
}

func allowedImageContentType(prefix []byte) (string, bool) {
	detected := http.DetectContentType(prefix)
	switch detected {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		return detected, true
	default:
		return "", false
	}
}
