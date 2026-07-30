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
	"syscall"
	"time"

	"mdreview.dev/mdreview/internal/cli"
	"mdreview.dev/mdreview/internal/filesystem"
	"mdreview.dev/mdreview/internal/review"
	"mdreview.dev/mdreview/internal/server"
	"mdreview.dev/mdreview/internal/skillassets"
	"mdreview.dev/mdreview/internal/version"
	"mdreview.dev/mdreview/internal/workspace"
	"mdreview.dev/mdreview/web"
)

const (
	defaultPort     = 4242
	shutdownTimeout = 5 * time.Second
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
	if options.Command == cli.Version {
		_, err := fmt.Fprintln(output, version.Current)
		return err
	}
	if options.Command != cli.Serve {
		return runSkillManagement(ctx, options, input, output)
	}
	return runServe(ctx, options, output)
}

func runServe(
	ctx context.Context,
	options cli.Options,
	output io.Writer,
) (returnErr error) {
	gateway, err := filesystem.Open(options.Directory)
	if err != nil {
		return fmt.Errorf("open workspace filesystem: %w", err)
	}
	defer gateway.Close()
	canonicalRoot := gateway.Root()

	listener, err := listenLoopback(options.Port, options.PortExplicit)
	if err != nil {
		return err
	}
	defer listener.Close()

	indexedWorkspace, err := workspace.New(gateway, workspace.Options{})
	if err != nil {
		return fmt.Errorf("open workspace: %w", err)
	}

	reviewStore, err := review.NewStore(gateway, review.StoreOptions{})
	if err != nil {
		return fmt.Errorf("open review store: %w", err)
	}

	application, err := loadEmbeddedApplication()
	if err != nil {
		return err
	}
	boundHost := listener.Addr().String()
	handler, err := server.New(server.Config{
		Assets:    application.web,
		Workspace: indexedWorkspace,
		Review:    reviewStore,
		BoundHost: boundHost,
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
