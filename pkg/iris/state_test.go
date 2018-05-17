package iris

import (
	"fmt"
	"os"
	"regexp"
	"testing"
	"time"

	envoy "github.com/envoyproxy/go-control-plane/envoy/api/v2"
	"github.com/envoyproxy/go-control-plane/envoy/api/v2/core"
	"github.com/envoyproxy/go-control-plane/envoy/api/v2/endpoint"
	fakegen "github.com/icrowley/fake"
	"github.com/QubitProducts/iris/pkg/v1pb"
	"github.com/stretchr/testify/assert"
)

func TestState(t *testing.T) {
	if testing.Short() {
		t.Skip("State test requires several seconds to complete")
	}

	f, err := os.Open("testdata/conf1.yaml")
	assert.NoError(t, err)
	defer f.Close()

	conf, err := LoadConfig(f)
	assert.NoError(t, err)
	InitLogging(conf)

	ec := NewEnvoyCache()
	eventChan := make(chan *ClusterEvent, 32)
	state := NewState(conf, ec, eventChan)
	defer state.Destroy()

	writeTestEvents(conf, eventChan)
	time.Sleep(250 * time.Millisecond)

	snap := state.createCacheSnapshot()
	assert.Len(t, snap.Listeners.Items, 1)
	assert.Contains(t, snap.Listeners.Items, "app_proxy")

	assert.Len(t, snap.Routes.Items, 1)
	assert.Contains(t, snap.Routes.Items, "app_route")

	assert.Len(t, snap.Clusters.Items, 2)
	assert.Contains(t, snap.Clusters.Items, "grpc_read_cluster", "grpc_write_cluster")

	assert.Len(t, snap.Endpoints.Items, 2)
	assert.Contains(t, snap.Endpoints.Items, "grpc_read_cluster", "grpc_write_cluster")

	readCla := snap.Endpoints.Items["grpc_read_cluster"].(*envoy.ClusterLoadAssignment)
	assert.Len(t, readCla.Endpoints, 18)
	for _, lle := range readCla.Endpoints {
		checkLocality(t, lle.Locality)
		checkEndpoints(t, lle.LbEndpoints)
	}
}

func checkLocality(t *testing.T, locality *core.Locality) {
	t.Helper()

	assert.Contains(t, regions, locality.Region)
	assert.Contains(t, zones, locality.Zone)

	subZoneRegex := regexp.MustCompile(fmt.Sprintf("^%s%s_node\\d+$", locality.Region, locality.Zone))
	assert.Regexp(t, subZoneRegex, locality.SubZone)
}

func checkEndpoints(t *testing.T, endpoints []endpoint.LbEndpoint) {
	t.Helper()

	assert.Len(t, endpoints, numPodsPerNode)
	for _, ep := range endpoints {
		addr := ep.Endpoint.Address.GetSocketAddress()
		assert.NotZero(t, addr.Address)
		assert.Equal(t, uint32(6666), addr.GetPortValue())
		assert.Equal(t, core.TCP, addr.GetProtocol())
		assert.Len(t, ep.Metadata.FilterMetadata, 2)
		assert.Contains(t, ep.Metadata.FilterMetadata, IrisMDKey)
		assert.Len(t, ep.Metadata.FilterMetadata[IrisMDKey].Fields, 2)
		assert.Contains(t, ep.Metadata.FilterMetadata[IrisMDKey].Fields, "foo", "canary")
		assert.Contains(t, ep.Metadata.FilterMetadata, envoyMDKey)
		assert.Len(t, ep.Metadata.FilterMetadata[envoyMDKey].Fields, 1)
		assert.Contains(t, ep.Metadata.FilterMetadata[envoyMDKey].Fields, envoyCanaryKey)
	}
}

func writeTestEvents(conf *v1pb.Config, eventChan chan<- *ClusterEvent) {
	for _, cluster := range conf.Clusters {
		endpointGroups := make(map[string][]*EndpointInfo)

		for region, _ := range regions {
			for zone, _ := range zones {
				for i := 0; i < numNodesPerZone; i++ {
					var endpoints [numPodsPerNode]*EndpointInfo
					for j := 0; j < numPodsPerNode; j++ {
						endpoints[j] = &EndpointInfo{
							IP:      fakegen.IPv4(),
							Port:    6666,
							Region:  region,
							Zone:    zone,
							SubZone: fmt.Sprintf("%s%s_node%d", region, zone, i),
							Annotations: map[string]string{
								fmt.Sprintf("%sfoo", annotationPrefix): "bar",
								canaryAnnotation:                       "true",
							},
						}
					}

					endpointGroups[fmt.Sprintf("%s/%s/%s%s_node%d", region, zone, region, zone, i)] = endpoints[:]
				}
			}
		}

		key := fmt.Sprintf("%s/%s", testNS, cluster.Config.Name)
		evt := &ClusterEvent{
			Key:    key,
			Action: ClusterActionUpdate,
			Info: &ClusterInfo{
				ClusterName: cluster.Config.Name,
				Endpoints:   endpointGroups,
			},
		}

		eventChan <- evt
	}
}
