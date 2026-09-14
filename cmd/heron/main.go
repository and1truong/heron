package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"github.com/and1truong/heron/internal/config"
	"github.com/and1truong/heron/internal/lifecycle"
	"github.com/and1truong/heron/internal/observe"
	proc "github.com/and1truong/heron/internal/process"
	appProxy "github.com/and1truong/heron/internal/proxy"
	"github.com/and1truong/heron/internal/supervisor"
	"github.com/and1truong/heron/internal/tui"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"syscall"
	"time"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
)

func main() {
	if e := run(); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}

func run() error {
	return runArgs(os.Args[1:], os.Stdout)
}

func runArgs(args []string, output io.Writer) error {
	if len(args) > 0 && args[0] == "help" {
		switch {
		case len(args) == 1:
			args = []string{"-h"}
		case len(args) == 2 && (args[1] == "doctor" || args[1] == "tui"):
			args = []string{args[1], "-h"}
		default:
			return fmt.Errorf("help: unknown topic or unexpected arguments: %v (use heron help)", args[1:])
		}
	}
	if len(args) > 0 && args[0] == "doctor" {
		return runDoctorArgs("", args[1:], output)
	}
	interactive := len(args) > 0 && args[0] == "tui"
	if interactive {
		args = args[1:]
	}

	flags := flag.NewFlagSet("heron", flag.ContinueOnError)
	flags.SetOutput(output)
	path := flags.String("c", "", "configuration file (default ~/.config/heron.yaml)")
	flags.Usage = func() {
		fmt.Fprintln(output, "Lazy-start HTTP/gRPC/TCP proxy and local process supervisor.")
		fmt.Fprintln(output, "\nUsage: heron [-c FILE]")
		fmt.Fprintln(output, "       heron tui [-c FILE]")
		fmt.Fprintln(output, "       heron doctor [-c FILE]")
		fmt.Fprintln(output, "       heron help [doctor|tui]")
		fmt.Fprintln(output, "\nCommands:")
		fmt.Fprintln(output, "  tui     Start proxy with interactive app, process, log and event panes")
		fmt.Fprintln(output, "  doctor  Validate configuration without running hooks or app commands")
		fmt.Fprintln(output, "  help    Show general help or help for a command")
		fmt.Fprintln(output, "\nWithout a command, start the proxy and supervise configured services.")
		fmt.Fprintln(output, "\nOptions:")
		flags.PrintDefaults()
		fmt.Fprintln(output, "  -h, --help\n        show help")
		fmt.Fprintln(output, "\nExamples:\n  heron -c ./config.yaml\n  heron doctor -c ./config.yaml\n  heron help doctor")
	}
	if e := flags.Parse(args); e != nil {
		if errors.Is(e, flag.ErrHelp) {
			return nil
		}
		return e
	}
	doctor := !interactive && flags.NArg() == 1 && flags.Arg(0) == "doctor"
	if flags.NArg() != 0 && !doctor {
		return fmt.Errorf("unexpected arguments: %v", flags.Args())
	}
	resolvedPath, e := resolveConfigPath(flags, *path)
	if e != nil {
		return e
	}
	if doctor {
		return runDoctor(resolvedPath, output)
	}
	if interactive {
		if err := tui.CheckTerminal(); err != nil {
			return err
		}
	}
	return runServerMode(resolvedPath, interactive)
}

func runDoctorArgs(defaultPath string, args []string, output io.Writer) error {
	flags := flag.NewFlagSet("heron doctor", flag.ContinueOnError)
	flags.SetOutput(output)
	path := flags.String("c", defaultPath, "configuration file (default ~/.config/heron.yaml)")
	flags.Usage = func() {
		fmt.Fprintln(output, "Validate configuration and list resolved apps without running hooks or app commands.")
		fmt.Fprintln(output, "\nUsage: heron doctor [-c FILE]")
		fmt.Fprintln(output, "\nOptions:")
		flags.PrintDefaults()
		fmt.Fprintln(output, "  -h, --help\n        show help")
		fmt.Fprintln(output, "\nExample:\n  heron doctor -c ./config.yaml")
	}
	if e := flags.Parse(args); e != nil {
		if errors.Is(e, flag.ErrHelp) {
			return nil
		}
		return e
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("doctor: unexpected arguments: %v", flags.Args())
	}
	resolvedPath, e := resolveConfigPath(flags, *path)
	if e != nil {
		return e
	}
	return runDoctor(resolvedPath, output)
}

// Resolve the default only after parsing, so help never requires a home directory.
func resolveConfigPath(flags *flag.FlagSet, path string) (string, error) {
	explicit := false
	flags.Visit(func(f *flag.Flag) {
		if f.Name == "c" {
			explicit = true
		}
	})
	if explicit || path != "" {
		return path, nil
	}
	return config.DefaultPath()
}

func runDoctor(path string, output io.Writer) error {
	cfg, e := config.LoadStrict(path)
	if e != nil {
		return fmt.Errorf("doctor: configuration is invalid: %w", e)
	}

	ids := make([]string, 0, len(cfg.Apps))
	for id := range cfg.Apps {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	fmt.Fprintf(output, "[ok] configuration: %s\n", path)
	rootSource, _ := config.CanonicalPath(path)
	for _, id := range ids {
		app := cfg.Apps[id]
		source := ""
		if app.Source != "" && app.Source != rootSource {
			source = fmt.Sprintf(", source: %s", app.Source)
		}
		endpoints := app.EndpointList()
		if len(endpoints) == 0 {
			fmt.Fprintf(output, "[ok] app %s: process only (pwd: %s%s)\n", id, app.Pwd, source)
			continue
		}
		if len(endpoints) == 1 && endpoints[0].Name == "default" {
			fmt.Fprintf(output, "[ok] app %s: %s -> 127.0.0.1:%d (pwd: %s%s)\n", id, doctorEndpoint(cfg, endpoints[0]), endpoints[0].Port, app.Pwd, source)
			continue
		}
		for _, endpoint := range endpoints {
			fmt.Fprintf(output, "[ok] app %s endpoint %s: %s -> 127.0.0.1:%d (pwd: %s%s)\n", id, endpoint.Name, doctorEndpoint(cfg, endpoint), endpoint.Port, app.Pwd, source)
		}
	}
	fmt.Fprintf(output, "[ok] %d app(s) checked\n", len(ids))
	return nil
}

func doctorEndpoint(cfg config.RuntimeConfig, endpoint config.RuntimeEndpointConfig) string {
	switch endpoint.Protocol {
	case config.ProtocolTCP:
		return fmt.Sprintf("tcp://127.0.0.1:%d", endpoint.ListenPort)
	case config.ProtocolGRPC:
		return fmt.Sprintf("grpc://%s:%d", endpoint.Host, cfg.Port)
	default:
		if endpoint.Host != "" {
			return fmt.Sprintf("http://%s:%d", endpoint.Host, cfg.Port)
		}
		return fmt.Sprintf("http://127.0.0.1:%d%s", cfg.Port, endpoint.Path)
	}
}

func runServer(path string) error {
	return runServerMode(path, false)
}

func runServerMode(path string, interactive bool) error {
	cfg, e := config.Load(path)
	if e != nil {
		return fmt.Errorf("load configuration: %w", e)
	}
	level := slog.LevelInfo
	switch cfg.LogLevel {
	case "debug":
		level = slog.LevelDebug
	case "warning", "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	var observations *observe.Store
	if interactive {
		observations = observe.New()
		logger = slog.New(&observe.Handler{Store: observations, Level: level})
	}
	if e := preflightRequiredPorts(context.Background(), cfg, logger); e != nil {
		return e
	}
	runner := proc.NewRunner(logger)
	hooks := lifecycle.New(cfg.StartUp, cfg.TearDown, runner, logger)
	sup := supervisor.New(cfg, runner, logger)
	drainer := appProxy.NewDrainHandler(appProxy.NewHandler(cfg, sup, logger))
	http2Server := &http2.Server{}
	handler := h2c.NewHandler(drainer, http2Server)
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	if e := http2.ConfigureServer(server, http2Server); e != nil {
		return fmt.Errorf("configure HTTP/2 server: %w", e)
	}
	connections := appProxy.NewConnectionTracker()
	tcpServers := make([]*appProxy.TCPServer, 0)
	for id, app := range cfg.Apps {
		for _, endpoint := range app.EndpointList() {
			if endpoint.Protocol == config.ProtocolTCP {
				tcpServers = append(tcpServers, appProxy.NewTCPEndpointServer(id, endpoint, sup, logger))
			}
		}
	}
	signals, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), cfg.StopTimeout+30*time.Second)
		defer cancel()
		_ = hooks.TearDown(cleanupCtx)
	}()
	if e := hooks.StartUp(signals); e != nil {
		if signals.Err() != nil {
			return nil
		}
		return e
	}
	listeners, e := listenLoopbacks(cfg.Port)
	if e != nil {
		return e
	}
	errc := make(chan error, len(listeners)+len(tcpServers))
	for _, listener := range listeners {
		go func(listener net.Listener) {
			logger.Info("listening", "address", listener.Addr().String())
			errc <- server.Serve(connections.Track(listener))
		}(listener)
	}
	for _, tcpServer := range tcpServers {
		go func(s *appProxy.TCPServer) { errc <- s.ListenAndServe() }(tcpServer)
	}
	uiCtx, cancelUI := context.WithCancel(signals)
	defer cancelUI()
	var uiErrors chan error
	var uiDone chan struct{}
	if interactive {
		uiErrors = make(chan error, 1)
		uiDone = make(chan struct{})
		go func() { defer close(uiDone); uiErrors <- tui.Run(uiCtx, sup, observations) }()
	}
	var serveErr error
	select {
	case serveErr = <-uiErrors:
	case e := <-errc:
		if !errors.Is(e, http.ErrServerClosed) && !errors.Is(e, appProxy.ErrTCPServerClosed) {
			serveErr = e
		}
	case <-signals.Done():
	}
	cancelUI()
	if uiDone != nil {
		<-uiDone
	}
	timeout := cfg.StopTimeout + 30*time.Second
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if e := appProxy.DrainServer(ctx, server, drainer, connections); e != nil {
		logger.Warn("HTTP drain failed", "err", e)
	}
	for _, tcpServer := range tcpServers {
		if e := tcpServer.Shutdown(ctx); e != nil {
			logger.Error("TCP shutdown failed", "address", tcpServer.Addr(), "err", e)
		}
	}
	stopErr := sup.StopAll(ctx)
	return errors.Join(serveErr, stopErr)
}

func listenLoopbacks(port int) ([]net.Listener, error) {
	ipv4, err := net.Listen("tcp4", net.JoinHostPort("127.0.0.1", fmt.Sprint(port)))
	if err != nil {
		return nil, err
	}
	listeners := []net.Listener{ipv4}
	ipv6, err := net.Listen("tcp6", net.JoinHostPort("::1", fmt.Sprint(port)))
	if err != nil {
		if errors.Is(err, syscall.EAFNOSUPPORT) || errors.Is(err, syscall.EPROTONOSUPPORT) || errors.Is(err, syscall.EADDRNOTAVAIL) {
			return listeners, nil
		}
		_ = ipv4.Close()
		return nil, err
	}
	listeners = append(listeners, ipv6)
	return listeners, nil
}
