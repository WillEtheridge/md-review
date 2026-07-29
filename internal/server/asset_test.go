package server

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"

	"mdreview.dev/mdreview/internal/gatee"
	"mdreview.dev/mdreview/internal/limits"
	"mdreview.dev/mdreview/internal/workspace"
)

func TestWorkspaceAssetServesOnlyDetectedRasterTypes(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		content     []byte
	}{
		{name: "png", contentType: "image/png", content: append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 16)...)},
		{name: "jpeg", contentType: "image/jpeg", content: []byte{0xff, 0xd8, 0xff, 0xdb, 0, 1, 2, 3}},
		{name: "gif87a", contentType: "image/gif", content: []byte("GIF87a-image")},
		{name: "gif89a", contentType: "image/gif", content: []byte("GIF89a-image")},
		{name: "webp", contentType: "image/webp", content: []byte("RIFF\x04\x00\x00\x00WEBPVP8 ")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var documentPath string
			var reference string
			server := newTestServerWithWorkspace(t, fakeWorkspace{
				assetRead: func(
					_ context.Context,
					gotDocumentPath string,
					gotReference string,
					visit func(io.Reader, int64) error,
				) error {
					documentPath = gotDocumentPath
					reference = gotReference
					return visit(bytes.NewReader(test.content), int64(len(test.content)))
				},
			})
			response := httptest.NewRecorder()
			server.ServeHTTP(
				response,
				authenticatedRequest(
					http.MethodGet,
					"http://127.0.0.1:4242/api/asset?documentPath=docs%2Fguide.md&reference=..%2Fimages%2Fdiagram.png",
				),
			)

			if response.Code != http.StatusOK {
				t.Fatalf("status = %d, body %q", response.Code, response.Body.String())
			}
			if documentPath != "docs/guide.md" || reference != "../images/diagram.png" {
				t.Fatalf("scope = document %q, reference %q", documentPath, reference)
			}
			if got := response.Header().Get("Content-Type"); got != test.contentType {
				t.Fatalf("Content-Type = %q, want %q", got, test.contentType)
			}
			for _, absent := range []string{
				"Content-Length",
				"Content-Encoding",
				"ETag",
				"Last-Modified",
			} {
				if got := response.Header().Get(absent); got != "" {
					t.Fatalf("%s = %q, want absent", absent, got)
				}
			}
			if !bytes.Equal(response.Body.Bytes(), test.content) {
				t.Fatalf("body = %q, want %q", response.Body.Bytes(), test.content)
			}
			if got := response.Header().Get("Cache-Control"); got != "no-store" {
				t.Fatalf("Cache-Control = %q, want no-store", got)
			}
			csp := response.Header().Get("Content-Security-Policy")
			if !strings.Contains(csp, "img-src blob:") ||
				strings.Contains(csp, "img-src 'self'") ||
				strings.Contains(csp, "img-src data:") {
				t.Fatalf("CSP = %q", csp)
			}
		})
	}
}

func TestWorkspaceAssetValidatesMethodAndQuery(t *testing.T) {
	server := newTestServer(t)

	response := httptest.NewRecorder()
	server.ServeHTTP(
		response,
		authenticatedRequest(
			http.MethodPost,
			"http://127.0.0.1:4242/api/asset?documentPath=README.md&reference=image.png",
		),
	)
	if response.Code != http.StatusMethodNotAllowed || response.Header().Get("Allow") != http.MethodGet {
		t.Fatalf("method status = %d, Allow %q", response.Code, response.Header().Get("Allow"))
	}

	for _, rawQuery := range []string{
		"",
		"documentPath=README.md",
		"reference=image.png",
		"documentPath=&reference=image.png",
		"documentPath=README.md&reference=",
		"documentPath=README.md&reference=a&reference=b",
		"documentPath=README.md&documentPath=OTHER.md&reference=image.png",
		"documentPath=README.md&reference=image.png&extra=value",
	} {
		response = httptest.NewRecorder()
		server.ServeHTTP(
			response,
			authenticatedRequest(
				http.MethodGet,
				"http://127.0.0.1:4242/api/asset"+querySuffix(rawQuery),
			),
		)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("query %q status = %d, want %d", rawQuery, response.Code, http.StatusBadRequest)
		}
		assertErrorCode(t, response, "invalidAssetRequest")
	}
}

func TestWorkspaceAssetMapsSafeErrors(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{name: "not found", err: workspace.ErrAssetNotFound, status: http.StatusNotFound, code: "assetNotFound"},
		{name: "too large", err: workspace.ErrAssetTooLarge, status: http.StatusRequestEntityTooLarge, code: "assetTooLarge"},
		{name: "unavailable", err: workspace.ErrAssetUnavailable, status: http.StatusInternalServerError, code: "assetUnavailable"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := newTestServerWithWorkspace(t, fakeWorkspace{
				assetRead: func(
					context.Context,
					string,
					string,
					func(io.Reader, int64) error,
				) error {
					return test.err
				},
			})
			response := httptest.NewRecorder()
			server.ServeHTTP(response, assetRequest())
			if response.Code != test.status {
				t.Fatalf("status = %d, want %d", response.Code, test.status)
			}
			assertErrorCode(t, response, test.code)
		})
	}

	for name, content := range map[string][]byte{
		"html": []byte("<!doctype html><title>not an image</title>"),
		"svg":  []byte("<svg xmlns=\"http://www.w3.org/2000/svg\"></svg>"),
		"text": []byte("plain text"),
	} {
		t.Run(name, func(t *testing.T) {
			server := newTestServerWithWorkspace(t, fakeWorkspace{
				assetRead: func(
					_ context.Context,
					_ string,
					_ string,
					visit func(io.Reader, int64) error,
				) error {
					return visit(bytes.NewReader(content), int64(len(content)))
				},
			})
			response := httptest.NewRecorder()
			server.ServeHTTP(response, assetRequest())
			if response.Code != http.StatusUnsupportedMediaType {
				t.Fatalf("status = %d, want %d", response.Code, http.StatusUnsupportedMediaType)
			}
			assertErrorCode(t, response, "assetUnsupportedType")
		})
	}
}

func TestWorkspaceAssetAbortsGrowthBeyondLimit(t *testing.T) {
	png := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 504)...)
	reader := io.MultiReader(
		bytes.NewReader(png),
		io.LimitReader(zeroReader{}, limits.MaxImageAssetBytes+1-int64(len(png))),
	)
	server := newTestServerWithWorkspace(t, fakeWorkspace{
		assetRead: func(
			_ context.Context,
			_ string,
			_ string,
			visit func(io.Reader, int64) error,
		) error {
			return visit(reader, 1024)
		},
	})
	counters := &gatee.Counters{}
	server.measurements = counters
	response := &discardResponseWriter{header: make(http.Header)}

	panicValue := catchPanic(func() {
		server.ServeHTTP(response, assetRequest())
	})
	if !errors.Is(panicError(panicValue), http.ErrAbortHandler) {
		t.Fatalf("panic = %#v, want http.ErrAbortHandler", panicValue)
	}
	if response.written != limits.MaxImageAssetBytes+1 {
		t.Fatalf("written = %d, want %d", response.written, limits.MaxImageAssetBytes+1)
	}
	if len(server.assetPermits) != 0 {
		t.Fatalf("asset permits retained after abort = %d", len(server.assetPermits))
	}
	observed := counters.Snapshot()
	if observed.ActiveAssetStreams != 0 ||
		observed.MaximumAssetStreams != 1 ||
		observed.AssetStreamBytes != uint64(limits.MaxImageAssetBytes+1) {
		t.Fatalf("growth counters = %+v", observed)
	}
}

func TestWorkspaceAssetGrowthMakesRealHTTPFetchFail(t *testing.T) {
	png := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 504)...)
	testServer := httptest.NewUnstartedServer(nil)
	server, err := New(Config{
		Assets: testStaticAssets(),
		Workspace: fakeWorkspace{assetRead: func(
			_ context.Context,
			_ string,
			_ string,
			visit func(io.Reader, int64) error,
		) error {
			reader := io.MultiReader(
				bytes.NewReader(png),
				io.LimitReader(
					zeroReader{},
					limits.MaxImageAssetBytes+1-int64(len(png)),
				),
			)
			return visit(reader, 1024)
		}},
		Review:        fakeReviewStore{},
		InstanceNonce: "instance-nonce",
		BoundHost:     testServer.Listener.Addr().String(),
	})
	if err != nil {
		t.Fatal(err)
	}
	testServer.Config.Handler = server
	testServer.Start()
	t.Cleanup(testServer.Close)

	request, err := http.NewRequest(
		http.MethodGet,
		testServer.URL+"/api/asset?documentPath=README.md&reference=image.png",
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	response, requestErr := testServer.Client().Do(request)
	if requestErr != nil {
		return
	}
	defer response.Body.Close()
	_, readErr := io.Copy(io.Discard, response.Body)
	if readErr == nil {
		t.Fatal("growing asset produced a complete successful HTTP body")
	}
}

func TestWorkspaceAssetGlobalSemaphoreCapsEightStreams(t *testing.T) {
	started := make(chan struct{}, 9)
	release := make(chan struct{})
	var active atomic.Int32
	var maximum atomic.Int32
	server := newTestServerWithWorkspace(t, fakeWorkspace{
		assetRead: func(
			_ context.Context,
			_ string,
			_ string,
			visit func(io.Reader, int64) error,
		) error {
			current := active.Add(1)
			for {
				observed := maximum.Load()
				if current <= observed || maximum.CompareAndSwap(observed, current) {
					break
				}
			}
			started <- struct{}{}
			<-release
			active.Add(-1)
			return visit(bytes.NewReader([]byte("GIF89a-image")), 12)
		},
	})
	counters := &gatee.Counters{}
	server.measurements = counters

	begin := make(chan struct{})
	var ready sync.WaitGroup
	var completed sync.WaitGroup
	ready.Add(9)
	completed.Add(9)
	for range 9 {
		go func() {
			defer completed.Done()
			ready.Done()
			<-begin
			server.ServeHTTP(httptest.NewRecorder(), assetRequest())
		}()
	}
	ready.Wait()
	close(begin)
	for range limits.MaxConcurrentImageStreams {
		<-started
	}
	if got := len(server.assetPermits); got != limits.MaxConcurrentImageStreams {
		t.Fatalf("active permits = %d, want %d", got, limits.MaxConcurrentImageStreams)
	}
	if got := maximum.Load(); got != limits.MaxConcurrentImageStreams {
		t.Fatalf("maximum streams = %d, want %d", got, limits.MaxConcurrentImageStreams)
	}

	release <- struct{}{}
	<-started
	if got := maximum.Load(); got != limits.MaxConcurrentImageStreams {
		t.Fatalf("maximum after ninth = %d, want %d", got, limits.MaxConcurrentImageStreams)
	}
	for range limits.MaxConcurrentImageStreams {
		release <- struct{}{}
	}
	completed.Wait()
	if got := len(server.assetPermits); got != 0 {
		t.Fatalf("permits after completion = %d", got)
	}
	observed := counters.Snapshot()
	if observed.ActiveAssetStreams != 0 ||
		observed.MaximumAssetStreams != limits.MaxConcurrentImageStreams ||
		observed.AssetStreamBytes != 9*uint64(len("GIF89a-image")) {
		t.Fatalf("semaphore counters = %+v", observed)
	}
}

type zeroReader struct{}

func (zeroReader) Read(buffer []byte) (int, error) {
	clear(buffer)
	return len(buffer), nil
}

type discardResponseWriter struct {
	header  http.Header
	status  int
	written int64
}

func (writer *discardResponseWriter) Header() http.Header {
	return writer.header
}

func (writer *discardResponseWriter) WriteHeader(status int) {
	writer.status = status
}

func (writer *discardResponseWriter) Write(buffer []byte) (int, error) {
	writer.written += int64(len(buffer))
	return len(buffer), nil
}

func assetRequest() *http.Request {
	return authenticatedRequest(
		http.MethodGet,
		"http://127.0.0.1:4242/api/asset?documentPath=README.md&reference=image.png",
	)
}

func querySuffix(rawQuery string) string {
	if rawQuery == "" {
		return ""
	}
	return "?" + rawQuery
}

func testStaticAssets() fstest.MapFS {
	return fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte("application shell")},
	}
}

func catchPanic(run func()) (value any) {
	defer func() {
		value = recover()
	}()
	run()
	return nil
}

func panicError(value any) error {
	err, _ := value.(error)
	return err
}
