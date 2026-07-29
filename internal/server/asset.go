package server

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"

	"mdreview.dev/mdreview/internal/gatee"
	"mdreview.dev/mdreview/internal/limits"
	"mdreview.dev/mdreview/internal/workspace"
)

var errAssetUnsupportedType = errors.New("asset type is unsupported")

func (server *Server) serveWorkspaceAsset(
	response http.ResponseWriter,
	request *http.Request,
	requestID string,
) {
	documentPath, reference, ok := assetQuery(request)
	if !ok {
		server.writeError(
			response,
			requestID,
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
	finishMeasuredStream := server.measurements.BeginAssetStream()
	defer finishMeasuredStream()

	started := false
	err := server.workspace.ReadAsset(
		request.Context(),
		documentPath,
		reference,
		func(reader io.Reader, _ int64) error {
			if server.measurements != nil {
				reader = measuredAssetReader{
					reader:   reader,
					counters: server.measurements,
				}
			}
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
			requestID,
			http.StatusNotFound,
			"assetNotFound",
			"This image could not be found safely inside the workspace.",
		)
	case errors.Is(err, workspace.ErrAssetTooLarge):
		server.writeError(
			response,
			requestID,
			http.StatusRequestEntityTooLarge,
			"assetTooLarge",
			"This image is larger than 20 MiB.",
		)
	case errors.Is(err, errAssetUnsupportedType):
		server.writeError(
			response,
			requestID,
			http.StatusUnsupportedMediaType,
			"assetUnsupportedType",
			"Use a PNG, JPEG, GIF, or WebP image.",
		)
	default:
		server.writeError(
			response,
			requestID,
			http.StatusInternalServerError,
			"assetUnavailable",
			"This image is temporarily unavailable.",
		)
	}
}

type measuredAssetReader struct {
	reader   io.Reader
	counters *gatee.Counters
}

func (reader measuredAssetReader) Read(buffer []byte) (int, error) {
	read, err := reader.reader.Read(buffer)
	reader.counters.RecordAssetStreamBytes(read)
	return read, err
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
