package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"github.com/and1truong/heron/internal/config"
	"github.com/and1truong/heron/internal/control"
	"github.com/and1truong/heron/internal/lifecycle"
	"github.com/and1truong/heron/internal/observe"
	proc "github.com/and1truong/heron/internal/process"
	appProxy "github.com/and1truong/heron/internal/proxy"
	"github.com/and1truong/heron/internal/scheduler"
	"github.com/and1truong/heron/internal/supervisor"
	"github.com/and1truong/heron/internal/tui"
	"github.com/and1truong/heron/internal/webui"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
)

func main() {
	if e := run(); e != nil {
		var status exitError
		if errors.As(e, &status) {
			os.Exit(status.code)
		}
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}

func run() error {
	return runArgs(os.Args[1:], os.Stdout)
}

func runArgs(args []string, output io.Writer) error {
	if len(args) > 0 && (args[0] == "__service-run" || args[0] == "__service-validate") {
		return runServiceWorker(args[0], args[1:])
	}
	if len(args) > 0 && args[0] == "help" {
		switch {
		case len(args) == 1:
			args = []string{"-h"}
		case len(args) == 2 && (args[1] == "doctor" || args[1] == "tui" || args[1] == "ui" || isServiceCommand(args[1])):
			args = []string{args[1], "-h"}
		default:
			return fmt.Errorf("help: unknown topic or unexpected arguments: %v (use heron help)", args[1:])
		}
	}
	if len(args) > 0 && isServiceCommand(args[0]) {
		return runServiceArgs(args[0], args[1:], output)
	}
	if len(args) > 0 && args[0] == "doctor" {
		return runDoctorArgs("", args[1:], output)
	}
	interactive := len(args) > 0 && args[0] == "tui"
	graphical := len(args) > 0 && args[0] == "ui"
	if interactive || graphical {
		args = args[1:]
	}

	flags := flag.NewFlagSet("heron", flag.ContinueOnError)
	flags.SetOutput(output)
	path := flags.String("c", "", "configuration file (default ~/.config/heron.yaml)")
	app := flags.String("app", "", "start only this app and its dependencies")
	flags.Usage = func() {
		fmt.Fprintln(output, "Lazy-start HTTP/gRPC/TCP proxy and local process supervisor.")
		fmt.Fprintln(output, "\nUsage: heron [-c FILE] [--app NAME]")
		fmt.Fprintln(output, "       heron tui [-c FILE] [--app NAME]")
		fmt.Fprintln(output, "       heron ui [-c FILE] [--app NAME]")
		fmt.Fprintln(output, "       heron doctor [-c FILE]")
		fmt.Fprintln(output, "       heron help [doctor|tui|ui|start|stop|restart|status]")
		fmt.Fprintln(output, "       heron start|stop|restart|status [-c FILE] [--timeout 2m]")
		fmt.Fprintln(output, "\nCommands:")
		fmt.Fprintln(output, "  start/stop/restart/status  Manage background service with automatic web UI")
		fmt.Fprintln(output, "  ui      Start proxy with local graphical app manager")
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
	appSet := false
	flags.Visit(func(f *flag.Flag) {
		if f.Name == "app" {
			appSet = true
		}
	})
	if appSet && strings.TrimSpace(*app) == "" {
		return fmt.Errorf("--app requires a non-empty app name")
	}
	doctor := !interactive && !graphical && flags.NArg() == 1 && flags.Arg(0) == "doctor"
	if flags.NArg() != 0 && !doctor {
		return fmt.Errorf("unexpected arguments: %v", flags.Args())
	}
	resolvedPath, e := resolveConfigPath(flags, *path)
	if e != nil {
		return e
	}
	if doctor {
		if appSet {
			return fmt.Errorf("--app is only supported by heron and heron tui")
		}
		return runDoctor(resolvedPath, output)
	}
	return runSelectedMode(resolvedPath, interactive, graphical, *app, output)
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
	return runSelectedServerMode(path, interactive, "")
}

func runSelectedServerMode(path string, interactive bool, app string) error {
	return runSelectedMode(path, interactive, false, app, os.Stdout)
}

type runtimeLifecycle struct {
	starting func() error
	ready    func(string) error
	stopping func()
}

func runSelectedMode(path string, interactive, graphical bool, app string, output io.Writer) error {
	return runModeWithLifecycle(path, interactive, graphical, app, output, nil)
}

func runModeWithLifecycle(path string, interactive, graphical bool, app string, output io.Writer, notifications *runtimeLifecycle) error {
	// Termination applies to the whole worker lifetime, including teardown and
	// gaps between HTTP-requested runtime restarts.
	signals, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	for {
		if signals.Err() != nil {
			return nil
		}
		if notifications != nil {
			if err := notifications.starting(); err != nil {
				return err
			}
		}
		restart, err := runModeOnce(path, interactive, graphical, app, output, notifications, signals)
		if err != nil || !restart {
			return err
		}
	}
}

func runSelectedModeOnce(path string, interactive, graphical bool, app string, output io.Writer) (bool, error) {
	signals, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runModeOnce(path, interactive, graphical, app, output, nil, signals)
}

func runModeOnce(path string, interactive, graphical bool, app string, output io.Writer, notifications *runtimeLifecycle, signals context.Context) (resultRestart bool, resultErr error) {
	cfg, e := config.Load(path)
	if e != nil {
		return false, fmt.Errorf("load configuration: %w", e)
	}
	if app != "" {
		cfg, e = cfg.SelectApp(app)
		if e != nil {
			return false, e
		}
	}
	if interactive {
		if err := tui.CheckTerminal(); err != nil {
			return false, err
		}
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
	if interactive || graphical {
		observations = observe.New()
		handler := slog.Handler(&observe.Handler{Store: observations, Level: level})
		if notifications != nil {
			handler = dualHandler{handler, logger.Handler()}
		}
		logger = slog.New(handler)
	}
	if e := preflightRequiredPorts(context.Background(), cfg, logger); e != nil {
		return false, e
	}
	runner := proc.NewRunner(logger)
	hooks := lifecycle.New(cfg.StartUp, cfg.TearDown, runner, logger)
	tasks := scheduler.New(cfg.ScheduledTasks, runner, logger)
	sup := supervisor.New(cfg, runner, logger)
	sup.SetConfigLoader(func() (config.RuntimeConfig, error) {
		fresh, err := config.Load(path)
		if err != nil {
			return fresh, err
		}
		if app != "" {
			return fresh.SelectApp(app)
		}
		return fresh, nil
	})
	proxyHandler := appProxy.NewHandler(cfg, sup, logger)
	var ui *webui.Server
	if graphical {
		ui, e = webui.New(cfg, path, sup, observations)
		if e != nil {
			return false, fmt.Errorf("create graphical UI: %w", e)
		}
	}
	restartc := make(chan struct{})
	controlHandler := control.New(func() { close(restartc) })
	controlHandler.RestartService = func(ctx context.Context, id string) error {
		return sup.Action(ctx, id, "restart")
	}
	rootHandler := http.Handler(proxyHandler)
	rootHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if control.HandlesHost(r.Host) {
			controlHandler.ServeHTTP(w, r)
			return
		}
		if ui != nil {
			if ui.HandlesHost(r.Host) {
				ui.ServeHTTP(w, r)
				return
			}
		}
		proxyHandler.ServeHTTP(w, r)
	})
	drainer := appProxy.NewDrainHandler(rootHandler)
	http2Server := &http2.Server{}
	handler := h2c.NewHandler(drainer, http2Server)
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	if e := http2.ConfigureServer(server, http2Server); e != nil {
		return false, fmt.Errorf("configure HTTP/2 server: %w", e)
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
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), cfg.StopTimeout+30*time.Second)
		defer cancel()
		if err := hooks.TearDown(cleanupCtx); notifications != nil {
			resultErr = errors.Join(resultErr, err)
		}
	}()
	if e := hooks.StartUp(signals); e != nil {
		if signals.Err() != nil {
			return false, nil
		}
		return false, e
	}
	tasks.Start(context.Background())
	defer tasks.Stop()
	listeners, e := listenLoopbacks(cfg.Port)
	if e != nil {
		return false, e
	}
	defer func() {
		for _, listener := range listeners {
			_ = listener.Close()
		}
	}()
	tcpListeners := make([]net.Listener, 0, len(tcpServers))
	defer func() {
		for _, listener := range tcpListeners {
			_ = listener.Close()
		}
	}()
	for _, tcpServer := range tcpServers {
		listener, err := net.Listen("tcp", tcpServer.Addr())
		if err != nil {
			return false, err
		}
		tcpListeners = append(tcpListeners, listener)
	}
	errc := make(chan error, len(listeners)+len(tcpServers))
	for _, listener := range listeners {
		go func(listener net.Listener) {
			logger.Info("listening", "address", listener.Addr().String())
			errc <- server.Serve(connections.Track(listener))
		}(listener)
	}
	for index, tcpServer := range tcpServers {
		go func(s *appProxy.TCPServer, listener net.Listener) { errc <- s.Serve(listener) }(tcpServer, tcpListeners[index])
	}
	uiCtx, cancelUI := context.WithCancel(signals)
	defer cancelUI()
	var stopWebUI func()
	if ui != nil {
		stopWebUI = ui.Start(uiCtx)
		fmt.Fprintf(output, "Heron UI: %s\n", ui.URL())
	}
	var uiErrors chan error
	var uiDone chan struct{}
	if interactive {
		uiErrors = make(chan error, 1)
		uiDone = make(chan struct{})
		go func() {
			defer close(uiDone)
			uiErrors <- tui.Run(uiCtx, sup, observations)
		}()
	}
	var startErrors chan error
	var startDone chan struct{}
	if app != "" {
		startErrors = make(chan error, 1)
		startDone = make(chan struct{})
		go func() {
			defer close(startDone)
			if err := sup.Action(uiCtx, app, "start"); err != nil {
				logger.Error("app startup failed", "service", app, "err", err)
				if !interactive && !graphical && uiCtx.Err() == nil {
					startErrors <- fmt.Errorf("start app %q: %w", app, err)
				}
			}
		}()
	}
	var serveErr error
	restart := false
	if notifications != nil {
		// Probe the actual HTTP/UI path before publishing readiness. All TCP
		// listeners have already been bound, so asynchronous bind failures cannot
		// result in a false successful service start.
		serveErr = checkUIReady(signals, cfg.Port)
		if serveErr == nil {
			serveErr = notifications.ready(ui.URL())
		}
	}
	if serveErr == nil {
		select {
		case serveErr = <-startErrors:
		case serveErr = <-uiErrors:
		case e := <-errc:
			if !errors.Is(e, http.ErrServerClosed) && !errors.Is(e, appProxy.ErrTCPServerClosed) {
				serveErr = e
			}
		case <-signals.Done():
		case <-restartc:
			restart = true
		}
	}
	if notifications != nil {
		notifications.stopping()
	}
	cancelUI()
	if stopWebUI != nil {
		stopWebUI()
	}
	if uiDone != nil {
		<-uiDone
	}
	tasks.Stop()
	// Stop apps while the frontends drain: a browser-held proxied connection
	// (e.g. a WebSocket) never finishes on its own, but closes as soon as the
	// app behind it stops. Stopping gets its own budget so drain time can
	// never starve it, and heron exits only once apps are really down.
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), cfg.StopTimeout+30*time.Second)
	defer cancelShutdown()
	drainErrc := make(chan error, 1)
	go func() {
		drainErrc <- appProxy.DrainServer(shutdownCtx, server, drainer, connections)
	}()
	stopCtx, cancelStop := context.WithTimeout(context.Background(), cfg.StopTimeout+30*time.Second)
	defer cancelStop()
	stopErr := sup.StopAll(stopCtx)
	if e := <-drainErrc; e != nil {
		logger.Warn("HTTP drain failed", "err", e)
	}
	for _, tcpServer := range tcpServers {
		if e := tcpServer.Shutdown(shutdownCtx); e != nil {
			logger.Error("TCP shutdown failed", "address", tcpServer.Addr(), "err", e)
		}
	}
	if startDone != nil {
		<-startDone
	}
	return restart, errors.Join(serveErr, stopErr)
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
