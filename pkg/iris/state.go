package iris

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/eapache/go-resiliency/batcher"
	envoy "github.com/envoyproxy/go-control-plane/envoy/api/v2"
	"github.com/envoyproxy/go-control-plane/envoy/api/v2/core"
	"github.com/envoyproxy/go-control-plane/envoy/api/v2/endpoint"
	"github.com/envoyproxy/go-control-plane/pkg/cache"
	"github.com/gogo/protobuf/types"
	"github.com/QubitProducts/iris/pkg/v1pb"
	"go.opencensus.io/stats"
	"go.opencensus.io/tag"
	"go.uber.org/zap"
)

const (
	envoyVirtualNode = "IrisEnvoyNode"

	envoyCanaryKey = "canary"
	envoyMDKey     = "envoy.lb"
	IrisMDKey      = "com.qubit.iris"
)

var dummyEvent = struct{}{}

// Hasher implements the cache.NodeHash interface
type Hasher struct{}

func (h *Hasher) ID(node *core.Node) string {
	// Using a constant cache key because we don't need a custom config per Envoy
	return envoyVirtualNode
}

// NewEnvoyCache creates a new Envoy cache to be used by the xDS server
func NewEnvoyCache() cache.SnapshotCache {
	return cache.NewSnapshotCache(true, &Hasher{}, nil)
}

// State holds the currently observed state of the Kube cluster
type State struct {
	config       *v1pb.Config
	envoyCache   cache.SnapshotCache
	eventChan    <-chan *ClusterEvent
	b            *batcher.Batcher
	mu           sync.RWMutex
	clusters     []cache.Resource
	routes       []cache.Resource
	listeners    []cache.Resource
	endpoints    map[string]*envoy.ClusterLoadAssignment
	shutdownChan chan struct{}
}

func NewState(config *v1pb.Config, envoyCache cache.SnapshotCache, eventChan <-chan *ClusterEvent) *State {
	clusters := createClusters(config)
	routes := createRoutes(config)
	listeners := createListeners(config)

	s := &State{
		config:       config,
		envoyCache:   envoyCache,
		eventChan:    eventChan,
		clusters:     clusters,
		routes:       routes,
		listeners:    listeners,
		endpoints:    make(map[string]*envoy.ClusterLoadAssignment),
		shutdownChan: make(chan struct{}),
	}

	s.b = batcher.New(*config.Iris.StateRefreshPeriod, s.refresh)
	go s.eventLoop()
	return s
}

func (s *State) eventLoop() {
	for {
		select {
		case <-s.shutdownChan:
			return
		case evt := <-s.eventChan:
			switch evt.Action {
			case ClusterActionUpdate:
				s.updateCluster(evt)
			case ClusterActionDelete:
				s.deleteCluster(evt)
			}
			s.b.Run(dummyEvent)
		}
	}
}

func (s *State) updateCluster(evt *ClusterEvent) {
	cla := &envoy.ClusterLoadAssignment{
		ClusterName: evt.Info.ClusterName,
	}

	for _, eps := range evt.Info.Endpoints {
		lg := endpoint.LocalityLbEndpoints{
			Locality: &core.Locality{
				Region:  eps[0].Region,
				Zone:    eps[0].Zone,
				SubZone: eps[0].SubZone,
			},
			LbEndpoints: make([]endpoint.LbEndpoint, len(eps)),
		}

		for j, ei := range eps {
			lep := endpoint.LbEndpoint{
				Metadata: extractMetadata(ei.Annotations),
				Endpoint: &endpoint.Endpoint{
					Address: &core.Address{
						Address: &core.Address_SocketAddress{
							SocketAddress: &core.SocketAddress{
								Protocol:      core.TCP,
								Address:       ei.IP,
								PortSpecifier: &core.SocketAddress_PortValue{PortValue: uint32(ei.Port)},
							},
						},
					},
				},
			}

			lg.LbEndpoints[j] = lep
		}

		cla.Endpoints = append(cla.Endpoints, lg)
		updateEndpointMetric(cla.ClusterName, lg.Locality.Region, lg.Locality.Zone, len(lg.LbEndpoints))
	}

	s.mu.Lock()
	s.endpoints[evt.Key] = cla
	s.mu.Unlock()
}

func (s *State) deleteCluster(evt *ClusterEvent) {
	s.mu.Lock()
	delete(s.endpoints, evt.Key)
	s.mu.Unlock()
}

func (s *State) refresh(_ []interface{}) error {
	snapshot := s.createCacheSnapshot()
	err := s.envoyCache.SetSnapshot(envoyVirtualNode, snapshot)
	if err != nil {
		stats.Record(context.Background(), snapshotUpdateFailure.M(1))
		zap.S().Warnw("Failed to update Envoy cache", "error", err)
		return err
	}

	zap.S().Debugw("Envoy cache updated")
	stats.Record(context.Background(), snapshotUpdateSuccess.M(1))
	return nil
}

func (s *State) createCacheSnapshot() cache.Snapshot {
	var endpoints []cache.Resource
	s.mu.RLock()
	for _, ep := range s.endpoints {
		endpoints = append(endpoints, ep)
	}
	s.mu.RUnlock()
	zap.S().Debugw("Endpoint list generated", "endpoints", endpoints)

	version := fmt.Sprintf("v%d", time.Now().Unix())
	return cache.NewSnapshot(version, endpoints, s.clusters, s.routes, s.listeners)
}

func (s *State) Destroy() {
	select {
	case <-s.shutdownChan:
	default:
		close(s.shutdownChan)
	}
}

func createListeners(conf *v1pb.Config) []cache.Resource {
	listeners := make([]cache.Resource, len(conf.Listeners))
	for i, l := range conf.Listeners {
		listeners[i] = l
	}

	zap.S().Debugw("Listener list generated", "listeners", listeners)
	return listeners
}

func createRoutes(conf *v1pb.Config) []cache.Resource {
	routes := make([]cache.Resource, len(conf.Routes))
	for i, r := range conf.Routes {
		routes[i] = r
	}

	zap.S().Debugw("Route list generated", "routes", routes)
	return routes
}

func createClusters(conf *v1pb.Config) []cache.Resource {
	clusters := make([]cache.Resource, len(conf.Clusters))
	for i, c := range conf.Clusters {
		clusters[i] = c.Config
	}

	zap.S().Debugw("Cluster list generated", "clusters", clusters)
	return clusters
}

func extractMetadata(annotations map[string]string) *core.Metadata {
	if len(annotations) == 0 {
		return nil
	}

	md := &core.Metadata{
		FilterMetadata: make(map[string]*types.Struct),
	}

	if canaryStatus, ok := annotations[canaryAnnotation]; ok {
		isCanary, err := strconv.ParseBool(canaryStatus)
		if err != nil {
			zap.S().Warnw("Invalid value for canary annotation", "value", canaryStatus, "error", err)
		} else {
			md.FilterMetadata[envoyMDKey] = &types.Struct{
				Fields: map[string]*types.Value{
					envoyCanaryKey: &types.Value{Kind: &types.Value_BoolValue{BoolValue: isCanary}},
				},
			}
		}
	}

	for annKey, annVal := range annotations {
		if strings.HasPrefix(annKey, annotationPrefix) {
			irisMD, ok := md.FilterMetadata[IrisMDKey]
			if !ok {
				irisMD = &types.Struct{Fields: map[string]*types.Value{}}
				md.FilterMetadata[IrisMDKey] = irisMD
			}

			key := strings.TrimPrefix(annKey, annotationPrefix)
			irisMD.Fields[key] = &types.Value{Kind: &types.Value_StringValue{StringValue: annVal}}
		}
	}

	return md
}

func updateEndpointMetric(clusterName, region, zone string, numEndpoints int) {
	ctx, err := tag.New(context.Background(), tag.Insert(ClusterKey, clusterName), tag.Insert(RegionKey, region), tag.Insert(ZoneKey, zone))
	if err != nil {
		return
	}

	stats.Record(ctx, clusterEndpointCount.M(int64(numEndpoints)))
}
