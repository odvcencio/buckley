package orchestrator

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	metricBatchJobsStarted = promauto.NewCounter(prometheus.CounterOpts{
		Namespace: "buckley",
		Name:      "batch_jobs_started_total",
		Help:      "Number of Kubernetes batch jobs dispatched by Buckley.",
	})
	metricBatchJobsFailed = promauto.NewCounter(prometheus.CounterOpts{
		Namespace: "buckley",
		Name:      "batch_jobs_failed_total",
		Help:      "Number of Kubernetes batch jobs that failed to start or complete.",
	})
	metricWorkspacePruned = promauto.NewCounter(prometheus.CounterOpts{
		Namespace: "buckley",
		Name:      "batch_workspaces_pruned_total",
		Help:      "Count of task workspaces that were garbage collected.",
	})
)

func recordJobDispatch() {
	metricBatchJobsStarted.Inc()
}

func recordJobFailure() {
	metricBatchJobsFailed.Inc()
}

func recordWorkspacePrune(count int) {
	if count > 0 {
		metricWorkspacePruned.Add(float64(count))
	}
}
