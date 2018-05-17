package iris

import (
	"context"
	"sync"

	"github.com/envoyproxy/go-control-plane/pkg/cache"
	"github.com/envoyproxy/go-control-plane/pkg/server"
	"github.com/QubitProducts/iris/pkg/v1pb"
	"go.opencensus.io/stats"
	"go.uber.org/zap"
	"k8s.io/client-go/kubernetes"
)

type Server struct {
	server.Server
	*HealthSrv
	kubeClient   kubernetes.Interface
	envoyCache   cache.SnapshotCache
	mu           sync.Mutex
	state        *State
	shutdownChan chan struct{}
}

func NewServer(kubeClient kubernetes.Interface, healthSrv *HealthSrv) *Server {
	ec := NewEnvoyCache()
	return &Server{
		Server:       server.NewServer(ec, nil),
		HealthSrv:    healthSrv,
		kubeClient:   kubeClient,
		envoyCache:   ec,
		shutdownChan: make(chan struct{}),
	}
}

func (is *Server) ReloadConfig(config *v1pb.Config) error {
	zap.S().Info("Reloading configuration")
	// eventChan is the communication channel between the controller and the state object
	eventChan := make(chan *ClusterEvent, int(config.Iris.StateBufferSize))

	// create controller
	controller, err := NewController(is.kubeClient, config, eventChan)
	if err != nil {
		close(eventChan)
		stats.Record(context.Background(), configReloadFailure.M(1))
		return err
	}

	is.mu.Lock()

	// clear current state
	select {
	case <-is.shutdownChan:
	default:
		close(is.shutdownChan)
	}
	if is.state != nil {
		is.state.Destroy()
	}

	// save new state
	is.state = NewState(config, is.envoyCache, eventChan)
	is.shutdownChan = make(chan struct{})

	is.mu.Unlock()

	// start new controller
	go controller.Run(is.shutdownChan)
	stats.Record(context.Background(), configReloadSuccess.M(1))
	return nil
}

func (is *Server) Shutdown() {
	is.mu.Lock()
	defer is.mu.Unlock()

	select {
	case <-is.shutdownChan:
	default:
		close(is.shutdownChan)
		if is.state != nil {
			is.state.Destroy()
		}
	}
}
