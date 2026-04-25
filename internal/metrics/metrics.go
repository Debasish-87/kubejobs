package metrics

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

var once sync.Once

var (

	// ---------------- JOB COUNTERS ----------------

	JobsProcessed = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "jobs_processed_total",
			Help: "Total number of processed jobs",
		},
		[]string{"worker"},
	)

	JobsFailed = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "jobs_failed_total",
			Help: "Total number of failed jobs",
		},
		[]string{"worker", "reason"},
	)

	// ---------------- LATENCY ----------------

	JobDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name: "job_duration_seconds",
			Help: "Job processing duration",

			// better buckets (more realistic distribution)
			Buckets: []float64{
				0.1, 0.5, 1, 2, 5, 10, 20, 30,
			},
		},
		[]string{"worker"},
	)

	// ---------------- QUEUE ----------------

	QueueDepth = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "queue_depth",
			Help: "Current queue size",
		},
		[]string{"stream"},
	)

	// ---------------- WORKER HEALTH ----------------

	WorkerActive = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "worker_active",
			Help: "Worker heartbeat status (1=alive)",
		},
		[]string{"worker"},
	)
)

// ---------------- INIT ----------------

func Init() {
	once.Do(func() {

		prometheus.MustRegister(
			JobsProcessed,
			JobsFailed,
			JobDuration,
			QueueDepth,
			WorkerActive,
		)

		// Without this, Prometheus may not show metrics until first increment
		JobsProcessed.WithLabelValues("init").Add(0)
		JobsFailed.WithLabelValues("init", "init").Add(0)
		JobDuration.WithLabelValues("init").Observe(0)
		QueueDepth.WithLabelValues("jobs_stream").Set(0)
		WorkerActive.WithLabelValues("init").Set(1)
	})
}
