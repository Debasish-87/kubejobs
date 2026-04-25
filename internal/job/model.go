package job

type Job struct {
	ID         string `json:"id"`
	Status     string `json:"status"`
	CreatedAt  int64  `json:"created_at"`
	StartedAt  int64  `json:"started_at"`
	RetryCount int    `json:"retry_count"`
	WorkerID   string `json:"worker_id"`
}
