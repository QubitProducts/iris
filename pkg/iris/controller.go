package iris

import (
	"context"
	"fmt"
	"time"

	"github.com/QubitProducts/iris/pkg/v1pb"
	"go.opencensus.io/stats"
	"go.uber.org/zap"
	"k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
)

const (
	maxRetries          = 5
	resyncPeriod        = 5 * time.Minute
	regionAnnotationKey = "failure-domain.beta.kubernetes.io/region"
	zoneAnnotationKey   = "failure-domain.beta.kubernetes.io/zone"
	unknownRegion       = "unknown"
	unknownZone         = "unknown"
)

const (
	annotationPrefix = "iris.qubit.com/"
	canaryAnnotation = "iris.qubit.com/canary"
)

// ClusterAction defines the action performed on a cluster such as an endpoint update or the deletion of the service
type ClusterAction int

const (
	ClusterActionUpdate ClusterAction = iota
	ClusterActionDelete
)

type ClusterEvent struct {
	Key    string
	Action ClusterAction
	Info   *ClusterInfo
}

type ClusterInfo struct {
	ClusterName string
	Endpoints   map[string][]*EndpointInfo
}

type EndpointInfo struct {
	IP          string
	Port        int32
	Annotations map[string]string
	Region      string
	Zone        string
	SubZone     string
}

// Controller implements a Kubernetes controller
type Controller struct {
	conf                *v1pb.Config
	eventChan           chan<- *ClusterEvent
	podInformer         cache.SharedIndexInformer
	nodeInformer        cache.SharedIndexInformer
	endpointInformer    cache.SharedIndexInformer
	endpointQueue       workqueue.RateLimitingInterface
	serviceToClusterMap map[string]*v1pb.Cluster
}

func NewController(kubeClient kubernetes.Interface, conf *v1pb.Config, eventChan chan<- *ClusterEvent) (*Controller, error) {
	c := kubeClient.CoreV1().RESTClient()
	nodeListWatcher := cache.NewListWatchFromClient(c, "nodes", metav1.NamespaceAll, fields.Everything())
	podListWatcher := cache.NewListWatchFromClient(c, "pods", conf.Iris.Namespace, fields.Everything())
	endpointListWatcher := cache.NewListWatchFromClient(c, "endpoints", conf.Iris.Namespace, fields.Everything())
	return createController(nodeListWatcher, podListWatcher, endpointListWatcher, conf, eventChan)
}

func createController(nodeWatch, podWatch, endpointWatch cache.ListerWatcher, conf *v1pb.Config, eventChan chan<- *ClusterEvent) (*Controller, error) {
	c := &Controller{
		conf:                conf,
		eventChan:           eventChan,
		nodeInformer:        cache.NewSharedIndexInformer(nodeWatch, &v1.Node{}, resyncPeriod, cache.Indexers{}),
		podInformer:         cache.NewSharedIndexInformer(podWatch, &v1.Pod{}, resyncPeriod, cache.Indexers{}),
		endpointInformer:    cache.NewSharedIndexInformer(endpointWatch, &v1.Endpoints{}, resyncPeriod, cache.Indexers{}),
		endpointQueue:       workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "qubit_com_iris_controller"),
		serviceToClusterMap: make(map[string]*v1pb.Cluster),
	}

	// Create event handlers
	c.endpointInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			if key, err := cache.MetaNamespaceKeyFunc(obj); err == nil {
				c.endpointQueue.Add(key)
			}
		},
		DeleteFunc: func(obj interface{}) {
			if key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj); err == nil {
				c.endpointQueue.Add(key)
			}
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			if key, err := cache.MetaNamespaceKeyFunc(newObj); err == nil {
				c.endpointQueue.Add(key)
			}
		},
	})

	for _, cluster := range conf.Clusters {
		c.serviceToClusterMap[cluster.ServiceName] = cluster
	}

	return c, nil
}

func (c *Controller) Run(stopChan <-chan struct{}) {
	defer func() {
		c.endpointQueue.ShutDown()
		runtime.HandleCrash()
	}()

	zap.S().Info("Starting Iris controller")
	go c.nodeInformer.Run(stopChan)
	go c.podInformer.Run(stopChan)
	go c.endpointInformer.Run(stopChan)

	if !cache.WaitForCacheSync(stopChan, c.nodeInformer.HasSynced, c.podInformer.HasSynced, c.endpointInformer.HasSynced) {
		runtime.HandleError(fmt.Errorf("Timed out waiting for caches to sync"))
		return
	}

	for i := 0; i < int(c.conf.Iris.Parallelism); i++ {
		go wait.Until(c.runWorker, time.Second, stopChan)
	}

	<-stopChan
	zap.S().Info("Stopping Iris controller")
}

func (c *Controller) runWorker() {
	for {
		key, quit := c.endpointQueue.Get()
		if quit {
			return
		}

		err := c.createClusterEvent(key.(string))
		c.handleError(err, key)
		c.endpointQueue.Done(key)
	}
}

func (c *Controller) createClusterEvent(key string) error {
	obj, exists, err := c.endpointInformer.GetIndexer().GetByKey(key)
	if err != nil {
		zap.S().Warnw("Failed to get key from store", "key", key, "error", err)
		return err
	}

	if exists {
		ep := obj.(*v1.Endpoints)
		if evt := c.buildClusterUpdateEvent(key, ep); evt != nil {
			zap.S().Debugw("Detected cluster update", "key", key, "info", evt.Info)
			c.eventChan <- evt
		}
	} else {
		zap.S().Debugw("Detected cluster deletion", "key", key)
		c.eventChan <- &ClusterEvent{Key: key, Action: ClusterActionDelete}
	}

	return nil
}

func (c *Controller) buildClusterUpdateEvent(key string, ep *v1.Endpoints) *ClusterEvent {
	clusterDef, ok := c.serviceToClusterMap[ep.Name]
	if !ok {
		zap.S().Debugw("Ignoring endpoint event as it does not match any of the configured services", "endpoint_name", ep.Name)
		return nil
	}

	clusterInfo := &ClusterInfo{
		ClusterName: clusterDef.Config.Name,
		Endpoints:   make(map[string][]*EndpointInfo),
	}

	for _, s := range ep.Subsets {
		var port int32
		for _, p := range s.Ports {
			if p.Name == clusterDef.PortName {
				port = p.Port
				break
			}
		}

		if port == 0 {
			zap.S().Debugw("No matching port found in endpoint subset", "port_name", clusterDef.PortName)
			continue
		}

		for _, a := range s.Addresses {
			region, zone, subZone := c.determineLocality(*a.NodeName)
			annotations := c.extractAnnotations(a.TargetRef)

			ei := &EndpointInfo{
				IP:          a.IP,
				Port:        port,
				Region:      region,
				Zone:        zone,
				SubZone:     subZone,
				Annotations: annotations,
			}

			localityKey := fmt.Sprintf("%s/%s/%s", region, zone, subZone)
			clusterInfo.Endpoints[localityKey] = append(clusterInfo.Endpoints[localityKey], ei)
		}
	}

	return &ClusterEvent{Key: key, Action: ClusterActionUpdate, Info: clusterInfo}
}

func (c *Controller) determineLocality(nodeName string) (string, string, string) {
	obj, exists, err := c.nodeInformer.GetIndexer().GetByKey(nodeName)
	if err != nil {
		zap.S().Warnw("Failed to find node", "node", nodeName, "error", err)
		return unknownRegion, unknownZone, nodeName
	}

	if !exists {
		return unknownRegion, unknownZone, nodeName
	}

	node := obj.(*v1.Node)
	region, ok := node.Labels[regionAnnotationKey]
	if !ok {
		region = unknownRegion
	}

	zone, ok := node.Labels[zoneAnnotationKey]
	if !ok {
		zone = unknownZone
	}

	return region, zone, nodeName
}

func (c *Controller) extractAnnotations(ref *v1.ObjectReference) map[string]string {
	//TODO find a less brittle way of obtaining a reference to the object
	if ref == nil {
		return nil
	}

	key := ref.Name
	if len(ref.Namespace) > 0 {
		key = fmt.Sprintf("%s/%s", ref.Namespace, ref.Name)
	}

	obj, exists, err := c.podInformer.GetIndexer().GetByKey(key)
	if err != nil {
		zap.S().Warnw("Failed to get information for target reference", "target_ref", ref, "error", err)
		return nil
	}

	if !exists {
		zap.S().Warnw("Target ref cannot be found", "target_ref", ref)
		return nil
	}

	pod := obj.(*v1.Pod)
	return pod.Annotations
}

func (c *Controller) handleError(err error, key interface{}) {
	if err == nil {
		c.endpointQueue.Forget(key)
		return
	}

	if c.endpointQueue.NumRequeues(key) < maxRetries {
		zap.S().Infow("Retrying sync", "key", key, "error", err)
		c.endpointQueue.AddRateLimited(key)
		return
	}

	zap.S().Errorw("Retries exhausted", "key", key, "error", err)
	c.endpointQueue.Forget(key)
	stats.Record(context.Background(), controllerErrors.M(1))
	runtime.HandleError(err)
}
