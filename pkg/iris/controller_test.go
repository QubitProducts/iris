package iris

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	fakegen "github.com/icrowley/fake"
	"github.com/stretchr/testify/assert"
	"k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	fakecore "k8s.io/client-go/kubernetes/typed/core/v1/fake"
	"k8s.io/client-go/tools/cache/testing"
)

const (
	testNS          = "test"
	numNodesPerZone = 3
	numPodsPerNode  = 5
)

var (
	regions = map[string]bool{
		"r1": true,
		"r2": true,
	}

	zones = map[string]bool{
		"z1": true,
		"z2": true,
		"z3": true,
	}
)

func TestController(t *testing.T) {
	f, err := os.Open("testdata/conf1.yaml")
	assert.NoError(t, err)
	defer f.Close()

	conf, err := LoadConfig(f)
	assert.NoError(t, err)
	InitLogging(conf)

	nodeSource := framework.NewFakeControllerSource()
	podSource := framework.NewFakeControllerSource()
	endpointSource := framework.NewFakeControllerSource()

	eventChan := make(chan *ClusterEvent, 64)

	// collect the events
	clusterInfo := make(map[string]*ClusterInfo)
	go func() {
		for ce := range eventChan {
			switch ce.Action {
			case ClusterActionUpdate:
				clusterInfo[ce.Key] = ce.Info
			case ClusterActionDelete:
				delete(clusterInfo, ce.Key)
			}
		}
	}()

	// start the controller
	controller, err := createController(nodeSource, podSource, endpointSource, conf, eventChan)
	assert.NoError(t, err)

	stopChan := make(chan struct{})
	go controller.Run(stopChan)

	// create a bunch of endpoints
	_, _, endpoints := generateTestObjects(t, nodeSource, podSource, endpointSource)
	time.Sleep(100 * time.Millisecond)

	t.Run("initialization", func(t *testing.T) {
		assert.Len(t, clusterInfo, 2)
		for _, info := range clusterInfo {
			assert.Len(t, info.Endpoints, len(regions)*len(zones)*numNodesPerZone)
			for _, ep := range info.Endpoints {
				assert.Len(t, ep, numPodsPerNode)

				expectedRegion := ep[0].Region
				expectedZone := ep[0].Zone
				expectedSubZone := ep[0].SubZone

				assert.Contains(t, regions, expectedRegion)
				assert.Contains(t, zones, expectedZone)
				assert.Regexp(t, regexp.MustCompile(fmt.Sprintf("^%s%snode_\\d+$", expectedRegion, expectedZone)), expectedSubZone)

				for _, ei := range ep {
					assert.Equal(t, expectedRegion, ei.Region)
					assert.Equal(t, expectedZone, ei.Zone)
					assert.Equal(t, expectedSubZone, ei.SubZone)
					assert.NotZero(t, ei.IP)
					assert.EqualValues(t, 6969, ei.Port)
					assert.Len(t, ei.Annotations, 1)
				}
			}
		}
	})

	t.Run("update", func(t *testing.T) {
		// Update the endpoints
		for _, ep := range endpoints {
			tmpEndpoint := ep
			tmpEndpoint.Subsets[0].Addresses = tmpEndpoint.Subsets[0].Addresses[:1]
			endpointSource.Modify(tmpEndpoint)
		}

		time.Sleep(100 * time.Millisecond)

		assert.Len(t, clusterInfo, 2)
		for _, info := range clusterInfo {
			assert.Len(t, info.Endpoints, 1)
			for _, addrs := range info.Endpoints {
				assert.Len(t, addrs, 1)
			}
		}
	})

	t.Run("deletion", func(t *testing.T) {
		// Delete the endpoints
		for _, ep := range endpoints {
			tmpEndpoint := ep
			endpointSource.Delete(tmpEndpoint)
		}

		time.Sleep(100 * time.Millisecond)

		assert.Len(t, clusterInfo, 0)
	})

	// shutdown the controller
	close(stopChan)
	time.Sleep(100 * time.Millisecond)
	close(eventChan)
}

func generateTestObjects(t *testing.T, nodeSource, podSource, endpointSource *framework.FakeControllerSource) ([]*v1.Node, []*v1.Pod, []*v1.Endpoints) {
	t.Helper()

	fakeClient := fake.NewSimpleClientset()
	fakeNodes := fakeClient.CoreV1().Nodes().(*fakecore.FakeNodes)
	fakePods := fakeClient.CoreV1().Pods(testNS).(*fakecore.FakePods)
	fakeEndpoints := fakeClient.CoreV1().Endpoints(testNS).(*fakecore.FakeEndpoints)

	var nodeList []*v1.Node
	for region, _ := range regions {
		for zone, _ := range zones {
			for i := 0; i < numNodesPerZone; i++ {
				node, err := fakeNodes.Create(&v1.Node{
					TypeMeta: metav1.TypeMeta{
						Kind:       "Node",
						APIVersion: "v1",
					},
					ObjectMeta: metav1.ObjectMeta{
						Name: fmt.Sprintf("%s%snode_%d", region, zone, i),
						Labels: map[string]string{
							regionAnnotationKey: region,
							zoneAnnotationKey:   zone,
						},
					},
				})

				if err != nil {
					panic(err)
				}

				nodeList = append(nodeList, node)
				nodeSource.Add(node)
			}
		}
	}

	var endpointList []*v1.Endpoints
	var podList []*v1.Pod

	for _, svc := range []string{"read-svc", "write-svc", "canary-svc"} {
		svcEndPoints := &v1.Endpoints{
			ObjectMeta: metav1.ObjectMeta{
				Name: svc,
			},
			Subsets: []v1.EndpointSubset{
				v1.EndpointSubset{
					Ports: []v1.EndpointPort{
						v1.EndpointPort{
							Name: "my-port",
							Port: 6969,
						},
					},
				},
			},
		}

		for _, node := range nodeList {
			pods := generatePodsAndEndpoints(svcEndPoints, node, podSource, fakePods)
			podList = append(podList, pods...)
		}

		ep, err := fakeEndpoints.Create(svcEndPoints)
		if err != nil {
			panic(err)
		}

		endpointList = append(endpointList, ep)
		endpointSource.Add(ep)
	}

	return nodeList, podList, endpointList
}

func generatePodsAndEndpoints(ep *v1.Endpoints, node *v1.Node, podSource *framework.FakeControllerSource, fakePods *fakecore.FakePods) []*v1.Pod {
	var podList []*v1.Pod
	for i := 0; i < numPodsPerNode; i++ {
		ip := fakegen.IPv4()
		podName := fmt.Sprintf("%s_%s", strings.ToLower(fakegen.FirstName()), fakegen.DigitsN(5))
		p, err := fakePods.Create(&v1.Pod{
			TypeMeta: metav1.TypeMeta{
				Kind:       "Pod",
				APIVersion: "v1",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:      podName,
				Namespace: testNS,
				Labels: map[string]string{
					"app": "app-name",
				},
				Annotations: map[string]string{
					fmt.Sprintf("%sfoo", annotationPrefix): "bar",
				},
			},
			Status: v1.PodStatus{
				PodIP: ip,
			},
			Spec: v1.PodSpec{
				NodeName: node.Name,
			},
		})

		if err != nil {
			panic(err)
		}

		podList = append(podList, p)
		podSource.Add(p)

		nodeName := node.Name
		ea := v1.EndpointAddress{
			IP:       ip,
			Hostname: podName,
			NodeName: &nodeName,
			TargetRef: &v1.ObjectReference{
				Namespace: testNS,
				Name:      podName,
			},
		}
		ep.Subsets[0].Addresses = append(ep.Subsets[0].Addresses, ea)
	}

	return podList
}
