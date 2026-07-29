// Package main wires mdReview's foreground process lifecycle.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"mdreview.dev/mdreview/internal/cli"
	"mdreview.dev/mdreview/internal/filesystem"
	"mdreview.dev/mdreview/internal/gatee"
	"mdreview.dev/mdreview/internal/review"
	mdruntime "mdreview.dev/mdreview/internal/runtime"
	"mdreview.dev/mdreview/internal/server"
	"mdreview.dev/mdreview/internal/skillassets"
	"mdreview.dev/mdreview/internal/workspace"
	"mdreview.dev/mdreview/web"

	"golang.org/x/sys/unix"
)

const (
	applicationVersion = "v0.1.0"
	defaultPort        = 4242
	readinessTimeout   = 2 * time.Second
	shutdownTimeout    = 5 * time.Second
)

type embeddedApplication struct {
	web   fs.FS
	skill []byte
}

func loadEmbeddedApplication() (embeddedApplication, error) {
	webAssets, err := web.Assets()
	if err != nil {
		return embeddedApplication{}, fmt.Errorf("load embedded browser assets: %w", err)
	}

	skill, err := skillassets.ReadSkill()
	if err != nil {
		return embeddedApplication{}, fmt.Errorf("load embedded Agent Skill: %w", err)
	}

	return embeddedApplication{
		web:   webAssets,
		skill: skill,
	}, nil
}

func main() {
	ctx, stop := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
		syscall.SIGHUP,
	)
	defer stop()

	if err := run(ctx, os.Args[1:], os.Stdin, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "mdreview: %v\n", err)
		os.Exit(1)
	}
}

func run(
	ctx context.Context,
	arguments []string,
	input io.Reader,
	output io.Writer,
) error {
	options, err := cli.Parse(arguments, os.Getwd)
	if err != nil {
		return err
	}
	expectedParent := 0
	if options.Command == cli.Serve && options.ManagedSession {
		// The snapshot precedes every potentially blocking startup operation.
		// ArmParentDeath later detects reparenting only from this point onward.
		expectedParent = unix.Getppid()
	}
	if options.Command == cli.Version {
		_, err := fmt.Fprintln(output, applicationVersion)
		return err
	}
	if options.Command != cli.Serve {
		return runSkillManagement(ctx, options, input, output)
	}
	return runServe(ctx, options, output, expectedParent)
}

func runServe(
	ctx context.Context,
	options cli.Options,
	output io.Writer,
	expectedParent int,
) (returnErr error) {
	canonicalRoot, err := canonicalDirectory(options.Directory)
	if err != nil {
		return err
	}

	verifyReady := server.HealthVerifier(nil)
	lease, existing, err := mdruntime.Acquire(ctx, mdruntime.Config{
		Root:        canonicalRoot,
		VerifyReady: verifyReady,
	})
	if err != nil {
		return fmt.Errorf("claim workspace instance: %w", err)
	}
	if existing != nil {
		printExistingInstance(output, canonicalRoot, existing.URL)
		return nil
	}
	defer lease.Close()

	if options.ManagedSession {
		if err := mdruntime.ValidateSession(int(os.Stdin.Fd())); err != nil {
			return fmt.Errorf("validate managed session: %w", err)
		}
		if err := mdruntime.ArmParentDeath(expectedParent, unix.SIGTERM); err != nil {
			return fmt.Errorf("arm managed session: %w", err)
		}
		defer func() {
			if err := mdruntime.DisarmParentDeath(); err != nil {
				returnErr = errors.Join(
					returnErr,
					fmt.Errorf("disarm managed session: %w", err),
				)
			}
		}()
	}

	listener, err := listenLoopback(options.Port, options.PortExplicit)
	if err != nil {
		return err
	}
	defer listener.Close()

	var measurements *gatee.Counters
	if os.Getenv(gatee.EnvironmentVariable) == "1" {
		measurements = &gatee.Counters{}
	}
	indexedWorkspace, err := workspace.Open(canonicalRoot, workspace.Options{
		FilesystemMode: filesystem.Auto,
		Measurements:   measurements,
	})
	if err != nil {
		return fmt.Errorf("open workspace: %w", err)
	}
	defer indexedWorkspace.Close()

	reviewFilesystem, err := filesystem.Open(canonicalRoot, filesystem.Auto)
	if err != nil {
		return fmt.Errorf("open review filesystem: %w", err)
	}
	defer reviewFilesystem.Close()
	reviewStore, err := review.NewStore(reviewFilesystem, review.StoreOptions{
		Measurements: measurements,
	})
	if err != nil {
		return fmt.Errorf("open review store: %w", err)
	}

	application, err := loadEmbeddedApplication()
	if err != nil {
		return err
	}
	boundHost := listener.Addr().String()
	handler, err := server.New(server.Config{
		Assets:        application.web,
		Workspace:     indexedWorkspace,
		Review:        reviewStore,
		InstanceNonce: lease.InstanceNonce(),
		BoundHost:     boundHost,
		Measurements:  measurements,
	})
	if err != nil {
		return fmt.Errorf("configure HTTP server: %w", err)
	}

	instanceURL := loopbackURL(boundHost)
	httpServer := &http.Server{
		Handler:           handler.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    16 * 1024,
	}
	serveErrors := make(chan error, 1)
	go func() {
		serveErrors <- httpServer.Serve(listener)
	}()
	defer httpServer.Close()

	readyContext, cancelReady := context.WithTimeout(ctx, readinessTimeout)
	err = waitUntilReady(
		readyContext,
		verifyReady,
		mdruntime.ReadyState{
			Root:          canonicalRoot,
			InstanceNonce: lease.InstanceNonce(),
			URL:           instanceURL,
		},
		serveErrors,
	)
	cancelReady()
	if err != nil {
		return err
	}
	if err := lease.PublishReady(instanceURL); err != nil {
		return fmt.Errorf("publish ready instance: %w", err)
	}

	snapshot, err := indexedWorkspace.Snapshot(ctx)
	if err != nil {
		return fmt.Errorf("read initial workspace state: %w", err)
	}
	printStartedInstance(output, canonicalRoot, snapshot.DocumentCount, instanceURL)

	select {
	case <-ctx.Done():
		shutdownContext, cancelShutdown := context.WithTimeout(
			context.Background(),
			shutdownTimeout,
		)
		defer cancelShutdown()
		if err := httpServer.Shutdown(shutdownContext); err != nil {
			return fmt.Errorf("shut down HTTP server: %w", err)
		}
		if serveErr := <-serveErrors; !errors.Is(serveErr, http.ErrServerClosed) {
			return fmt.Errorf("serve HTTP: %w", serveErr)
		}
		return nil
	case serveErr := <-serveErrors:
		if errors.Is(serveErr, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve HTTP: %w", serveErr)
	}
}

func canonicalDirectory(directory string) (string, error) {
	absolute, err := filepath.Abs(directory)
	if err != nil {
		return "", fmt.Errorf("make workspace directory absolute: %w", err)
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", fmt.Errorf("canonicalise workspace directory: %w", err)
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return "", fmt.Errorf("inspect workspace directory: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("workspace path is not a directory")
	}
	return canonical, nil
}

func listenLoopback(port uint16, explicit bool) (net.Listener, error) {
	return listenLoopbackWith(port, explicit, func(address string) (net.Listener, error) {
		return net.Listen("tcp4", address)
	})
}

func listenLoopbackWith(
	port uint16,
	explicit bool,
	listen func(address string) (net.Listener, error),
) (net.Listener, error) {
	if explicit {
		address := fmt.Sprintf("127.0.0.1:%d", port)
		listener, err := listen(address)
		if err != nil {
			return nil, fmt.Errorf("port %d is unavailable: %w", port, err)
		}
		return listener, nil
	}

	listener, err := listen(fmt.Sprintf("127.0.0.1:%d", defaultPort))
	if err == nil {
		return listener, nil
	}
	listener, fallbackErr := listen("127.0.0.1:0")
	if fallbackErr != nil {
		return nil, fmt.Errorf(
			"bind default or automatic loopback port: %w",
			errors.Join(err, fallbackErr),
		)
	}
	return listener, nil
}

func loopbackURL(boundHost string) string {
	address := url.URL{
		Scheme: "http",
		Host:   boundHost,
		Path:   "/",
	}
	return address.String()
}

func waitUntilReady(
	ctx context.Context,
	verify func(context.Context, mdruntime.ReadyState) error,
	state mdruntime.ReadyState,
	serveErrors <-chan error,
) error {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()

	for {
		if err := verify(ctx, state); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for HTTP readiness: %w", ctx.Err())
		case serveErr := <-serveErrors:
			return fmt.Errorf("serve HTTP before readiness: %w", serveErr)
		case <-ticker.C:
		}
	}
}

func printStartedInstance(
	output io.Writer,
	canonicalRoot string,
	documentCount int,
	instanceURL string,
) {
	fmt.Fprintf(
		output,
		"mdReview\n\nDirectory: %s\nDocuments: %d\nURL:       %s\n\n"+
			"Waiting for a browser connection. Press Ctrl+C to stop.\n",
		canonicalRoot,
		documentCount,
		instanceURL,
	)
}

func printExistingInstance(output io.Writer, canonicalRoot, instanceURL string) {
	fmt.Fprintf(
		output,
		"mdReview\n\nDirectory: %s\nURL:       %s\n\n"+
			"An existing mdReview instance is already serving this directory.\n",
		canonicalRoot,
		instanceURL,
	)
}
