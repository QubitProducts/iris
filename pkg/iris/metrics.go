package iris

import (
	"go.opencensus.io/stats"
	"go.opencensus.io/stats/view"
	"go.opencensus.io/tag"
)

var (
	ClusterKey, _ = tag.NewKey("cluster")
	RegionKey, _  = tag.NewKey("region")
	ZoneKey, _    = tag.NewKey("zone")
)

var (
	clusterEndpointCount  = stats.Int64("qubit.com/iris/cluster_endpoint_count", "Number of endpoints added to the cluster", stats.UnitDimensionless)
	configReloadFailure   = stats.Int64("qubit.com/iris/config_reload_failure", "Number of config reload failures", stats.UnitDimensionless)
	configReloadSuccess   = stats.Int64("qubit.com/iris/config_reload_success", "Number of config reload successes", stats.UnitDimensionless)
	controllerErrors      = stats.Int64("qubit.com/iris/controller_errors", "Number of unrecovered errors in the controller", stats.UnitDimensionless)
	snapshotUpdateSuccess = stats.Int64("qubit.com/iris/snapshot_update_success", "Number of successful snapshot update events", stats.UnitDimensionless)
	snapshotUpdateFailure = stats.Int64("qubit.com/iris/snapshot_update_failure", "Number of failed snapshot update events", stats.UnitDimensionless)
)

var (
	ClusterEndpointCountView = &view.View{
		Measure:     clusterEndpointCount,
		Aggregation: view.LastValue(),
		TagKeys:     []tag.Key{ClusterKey, RegionKey, ZoneKey},
	}

	ConfigReloadFailureView = &view.View{
		Measure:     configReloadFailure,
		Aggregation: view.Count(),
	}

	ConfigReloadSuccessView = &view.View{
		Measure:     configReloadSuccess,
		Aggregation: view.Count(),
	}

	ControllerErrorsView = &view.View{
		Measure:     controllerErrors,
		Aggregation: view.Count(),
	}

	SnapshotUpdateSuccessView = &view.View{
		Measure:     snapshotUpdateSuccess,
		Aggregation: view.Count(),
	}

	SnapshotUpdateFailureView = &view.View{
		Measure:     snapshotUpdateFailure,
		Aggregation: view.Count(),
	}
)

var DefaultIrisViews = []*view.View{
	ClusterEndpointCountView,
	ConfigReloadFailureView,
	ConfigReloadSuccessView,
	ControllerErrorsView,
	SnapshotUpdateSuccessView,
	SnapshotUpdateFailureView,
}
