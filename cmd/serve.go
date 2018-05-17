package cmd

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"io/ioutil"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	envoyapi "github.com/envoyproxy/go-control-plane/envoy/api/v2"
	discovery "github.com/envoyproxy/go-control-plane/envoy/service/discovery/v2"
	"github.com/fsnotify/fsnotify"
	"github.com/grpc-ecosystem/go-grpc-middleware"
	"github.com/grpc-ecosystem/go-grpc-middleware/logging/zap"
	"github.com/grpc-ecosystem/go-grpc-middleware/tags"
	"github.com/pkg/errors"
	prom "github.com/prometheus/client_golang/prometheus"
	"github.com/QubitProducts/iris/pkg/iris"
	"github.com/QubitProducts/iris/pkg/v1pb"
	"github.com/soheilhy/cmux"
	"github.com/spf13/cobra"
	"go.opencensus.io/exporter/prometheus"
	"go.opencensus.io/plugin/ocgrpc"
	"go.opencensus.io/stats/view"
	"go.opencensus.io/zpages"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"
	_ "k8s.io/kubernetes/pkg/util/workqueue/prometheus"
)

const httpTimeout = 10 * time.Second

type serveArgs struct {
	confPath      string
	debug         bool
	listenAddr    string
	tlsServerCert string
	tlsKey        string
	tlsCACert     string
	watchConf     bool
}

var serveCmdArgs = serveArgs{}

func createServeCommand() *cobra.Command {
	serveCmd := &cobra.Command{
		Use:   "serve",
		Short: "Start management server",
		Long:  "Start the Envoy management server",
		Run:   doServe,
	}

	serveCmd.Flags().StringVar(&serveCmdArgs.confPath, "conf", "iris.yaml", "Path to the config file")
	serveCmd.Flags().BoolVar(&serveCmdArgs.debug, "debug", false, "Enable debug mode")
	serveCmd.Flags().StringVar(&serveCmdArgs.listenAddr, "listen", ":8080", "Listener address")
	serveCmd.Flags().StringVar(&serveCmdArgs.tlsServerCert, "tls_crt", "", "Path to TLS server certificate")
	serveCmd.Flags().StringVar(&serveCmdArgs.tlsKey, "tls_key", "", "Path to TLS key")
	serveCmd.Flags().StringVar(&serveCmdArgs.tlsCACert, "tls_ca", "", "Path to TLS CA certificate")
	serveCmd.Flags().BoolVar(&serveCmdArgs.watchConf, "watch_conf", false, "Watch the config file and reload on changes")

	return serveCmd
}

func doServe(cmd *cobra.Command, args []string) {
	conf, err := readInConfig(serveCmdArgs.confPath)
	if err != nil {
		os.Exit(1)
	}

	iris.InitLogging(conf)

	kubeClient, err := getKubeClient()
	if err != nil {
		zap.S().Fatalw("Failed to create Kubernetes client", "error", err)
	}

	healthSrv := iris.NewHealthSrv("envoy.service.discovery.v2.AggregatedDiscoveryService")

	irisSrv := iris.NewServer(kubeClient, healthSrv)
	if err = irisSrv.ReloadConfig(conf); err != nil {
		zap.S().Fatalw("Failed to create Iris server", "error", err)
	}

	if serveCmdArgs.watchConf {
		watchStopChan, err := startConfigFileWatcher(irisSrv, serveCmdArgs.confPath)
		if err != nil {
			zap.S().Fatalw("Failed to create config file watch", "error", err)
		}
		defer close(watchStopChan)
	}

	promExporter, err := initOCPromExporter()
	if err != nil {
		zap.S().Fatalw("Failed to create OpenCensus exporter", "error", err)
	}

	grpcListener, httpListener := startListener()

	grpcServer := startGRPCServer(grpcListener, irisSrv)
	httpServer := startHTTPServer(httpListener, healthSrv, promExporter)

	// await interruption
	shutdownChan := make(chan os.Signal, 1)
	signal.Notify(shutdownChan, os.Interrupt)
	<-shutdownChan

	zap.S().Info("Shutting down")
	grpcServer.GracefulStop()

	ctx, cancelFunc := context.WithTimeout(context.Background(), httpTimeout)
	defer cancelFunc()
	httpServer.Shutdown(ctx)
	irisSrv.Shutdown()
}

func readInConfig(path string) (*v1pb.Config, error) {
	f, err := os.Open(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to read config file [%s]: %+v\n", path, err)
		return nil, err
	}
	defer f.Close()

	conf, err := iris.LoadConfig(f)
	if err != nil {
		f.Close()
		fmt.Fprintf(os.Stderr, "Failed to load config [%s]: %+v\n", path, err)
		return nil, err
	}

	if errs := iris.ValidateConfig(conf); len(errs) > 0 {
		for _, err = range errs {
			fmt.Fprintf(os.Stderr, "%+v\n", err)
		}

		return nil, errors.New("Invalid configuration")
	}

	return conf, nil
}

func startListener() (net.Listener, net.Listener) {
	listener, err := net.Listen("tcp", serveCmdArgs.listenAddr)
	if err != nil {
		zap.S().Fatalw("Failed to create listener", "error", err)
	}

	connMux := cmux.New(listener)
	httpListener := connMux.Match(cmux.HTTP1Fast())
	grpcListener := connMux.Match(cmux.Any())

	if serveCmdArgs.tlsServerCert != "" && serveCmdArgs.tlsKey != "" && serveCmdArgs.tlsCACert != "" {
		zap.S().Info("Configuring TLS")
		tlsConf, err := getTLSConfig()
		if err != nil {
			zap.S().Fatalw("Failed to configure TLS", "error", err)
		}

		grpcListener = tls.NewListener(grpcListener, tlsConf)
	}

	go func() {
		if err := connMux.Serve(); err != nil && !strings.Contains(err.Error(), "use of closed network connection") {
			zap.S().Fatalw("Listener failed", "error", err)
		}
	}()

	return grpcListener, httpListener
}

func startConfigFileWatcher(irisSrv *iris.Server, confFilePath string) (chan struct{}, error) {
	absPath, err := filepath.Abs(confFilePath)
	if err != nil {
		return nil, err
	}
	confFileDir := filepath.Dir(absPath)
	confFileName := filepath.Base(absPath)

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}

	stopChan := make(chan struct{})

	go func() {
		defer watcher.Close()
		for {
			select {
			case <-stopChan:
				zap.S().Info("Stopping config file watcher")
				return
			case evt := <-watcher.Events:
				zap.S().Debugw("Detected file event", "event", evt)
				if filepath.Base(evt.Name) != confFileName {
					continue
				}

				if evt.Op&fsnotify.Remove == fsnotify.Remove {
					zap.S().Warnw("Config file removed")
					continue
				}

				zap.S().Infow("Detected a change to config file. Attempting reload", "file", confFilePath)
				conf, err := readInConfig(confFilePath)
				if err != nil {
					zap.S().Errorw("Failed to load config", "error", err)
					continue
				}

				if err = irisSrv.ReloadConfig(conf); err != nil {
					zap.S().Errorw("Failed to reload configuration", "error", err)
					continue
				}

				zap.S().Info("Configuration reloaded successfully")
			case err := <-watcher.Errors:
				zap.S().Warnw("Detected error in config file watcher", "error", err)
			}
		}
	}()

	if err = watcher.Add(confFileDir); err != nil {
		zap.S().Errorw("Failed to create watch on config directory", "directory", confFileDir, "error", err)
		close(stopChan)
		return nil, err
	}

	zap.S().Infow("Watching configuration file for changes", "directory", confFileDir, "file", confFileName)
	return stopChan, nil
}

func initOCPromExporter() (*prometheus.Exporter, error) {
	if err := view.Register(ocgrpc.DefaultServerViews...); err != nil {
		return nil, err
	}

	if err := view.Register(iris.DefaultIrisViews...); err != nil {
		return nil, err
	}

	registry, ok := prom.DefaultRegisterer.(*prom.Registry)
	if !ok {
		zap.S().Warn("Unable to obtain default Prometheus registry. Creating new one.")
		registry = nil
	}

	exporter, err := prometheus.NewExporter(prometheus.Options{Registry: registry})
	if err != nil {
		return nil, err
	}

	view.RegisterExporter(exporter)
	view.SetReportingPeriod(15 * time.Second)

	return exporter, nil
}

func startGRPCServer(listener net.Listener, irisSrv *iris.Server) *grpc.Server {
	grpc.EnableTracing = true
	grpcLogger := zap.L().Named("grpc.log")

	codeToLevel := grpc_zap.CodeToLevel(func(code codes.Code) zapcore.Level {
		if code == codes.OK {
			return zapcore.DebugLevel
		}
		return grpc_zap.DefaultCodeToLevel(code)
	})

	serverOpts := []grpc.ServerOption{
		grpc.StatsHandler(&ocgrpc.ServerHandler{}),
		grpc_middleware.WithUnaryServerChain(
			grpc_ctxtags.UnaryServerInterceptor(),
			grpc_zap.UnaryServerInterceptor(grpcLogger, grpc_zap.WithLevels(codeToLevel)),
		),
		grpc_middleware.WithStreamServerChain(
			grpc_ctxtags.StreamServerInterceptor(),
			grpc_zap.StreamServerInterceptor(grpcLogger, grpc_zap.WithLevels(codeToLevel)),
		),
	}

	grpcServer := grpc.NewServer(serverOpts...)
	envoyapi.RegisterListenerDiscoveryServiceServer(grpcServer, irisSrv)
	envoyapi.RegisterRouteDiscoveryServiceServer(grpcServer, irisSrv)
	envoyapi.RegisterClusterDiscoveryServiceServer(grpcServer, irisSrv)
	envoyapi.RegisterEndpointDiscoveryServiceServer(grpcServer, irisSrv)
	discovery.RegisterAggregatedDiscoveryServiceServer(grpcServer, irisSrv)
	healthpb.RegisterHealthServer(grpcServer, irisSrv)

	reflection.Register(grpcServer)

	go func() {
		zap.S().Info("Starting Iris server")
		if err := grpcServer.Serve(listener); err != nil && err != cmux.ErrListenerClosed {
			zap.S().Fatalw("Iris server failed", "error", err)
		}
	}()

	return grpcServer
}

func getTLSConfig() (*tls.Config, error) {
	certificate, err := tls.LoadX509KeyPair(serveCmdArgs.tlsServerCert, serveCmdArgs.tlsKey)
	if err != nil {
		return nil, errors.Wrap(err, "Failed to load server key pair")
	}

	certPool := x509.NewCertPool()
	bs, err := ioutil.ReadFile(serveCmdArgs.tlsCACert)
	if err != nil {
		return nil, errors.Wrap(err, "Failed to read CA certificate")
	}

	ok := certPool.AppendCertsFromPEM(bs)
	if !ok {
		return nil, errors.Wrap(err, "Failed to add CA certificate to pool")
	}

	tlsConfig := defaultTLSConfig()
	tlsConfig.ClientAuth = tls.VerifyClientCertIfGiven
	tlsConfig.Certificates = []tls.Certificate{certificate}
	tlsConfig.ClientCAs = certPool
	tlsConfig.NextProtos = []string{"h2"}

	return tlsConfig, nil
}

func defaultTLSConfig() *tls.Config {
	// See https://blog.cloudflare.com/exposing-go-on-the-internet/
	return &tls.Config{
		MinVersion:               tls.VersionTLS12,
		PreferServerCipherSuites: true,
		CurvePreferences: []tls.CurveID{
			tls.CurveP256,
			tls.X25519,
		},
		CipherSuites: []uint16{
			tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305,
			tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305,
			tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
		},
	}
}

func startHTTPServer(listener net.Listener, healthSrv *iris.HealthSrv, promExporter *prometheus.Exporter) *http.Server {
	logger := zap.L().Named("http")

	mux := http.NewServeMux()
	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		io.Copy(ioutil.Discard, r.Body)
		r.Body.Close()

		if healthSrv.IsUnhealthy() {
			w.WriteHeader(http.StatusServiceUnavailable)
			io.WriteString(w, "NOT SERVING\n")
		} else {
			w.WriteHeader(http.StatusOK)
			io.WriteString(w, "SERVING\n")
		}
	})
	mux.Handle("/metrics", promExporter)

	if serveCmdArgs.debug {
		mux.Handle("/debug/pprof/", http.HandlerFunc(pprof.Index))
		mux.Handle("/debug/pprof/cmdline", http.HandlerFunc(pprof.Cmdline))
		mux.Handle("/debug/pprof/profile", http.HandlerFunc(pprof.Profile))
		mux.Handle("/debug/pprof/symbol", http.HandlerFunc(pprof.Symbol))
		mux.Handle("/debug/pprof/trace", http.HandlerFunc(pprof.Trace))
		mux.Handle("/debug/", http.StripPrefix("/debug", zpages.Handler))
	}

	httpServer := &http.Server{
		Handler:           mux,
		ErrorLog:          zap.NewStdLog(logger),
		ReadHeaderTimeout: httpTimeout,
		WriteTimeout:      httpTimeout,
		IdleTimeout:       httpTimeout,
	}

	go func() {
		zap.S().Infow("Starting Iris HTTP server")
		if err := httpServer.Serve(listener); err != cmux.ErrListenerClosed {
			zap.S().Fatalw("Failed to start Iris HTTP server", "error", err)
		}
	}()

	return httpServer
}
