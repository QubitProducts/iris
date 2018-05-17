package iris

import (
	"sync"

	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

type HealthSrv struct {
	*health.Server
	serviceName string
	mu          sync.RWMutex
	unhealthy   bool
}

func NewHealthSrv(serviceName string) *HealthSrv {
	return &HealthSrv{
		Server:      health.NewServer(),
		serviceName: serviceName,
	}
}

func (h *HealthSrv) MarkUnhealthy() {
	h.mu.Lock()
	h.unhealthy = true
	h.SetServingStatus(h.serviceName, healthpb.HealthCheckResponse_NOT_SERVING)
	h.mu.Unlock()
}

func (h *HealthSrv) MarkHealthy() {
	h.mu.Lock()
	h.unhealthy = false
	h.SetServingStatus(h.serviceName, healthpb.HealthCheckResponse_SERVING)
	h.mu.Unlock()
}

func (h *HealthSrv) IsUnhealthy() bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.unhealthy
}
