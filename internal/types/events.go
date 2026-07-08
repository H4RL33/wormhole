package types

// TaskStatusChangedPayload is the typed payload for task.status_changed events (RFC-0001 §8.1).
type TaskStatusChangedPayload struct {
	TaskID     string `json:"task_id"`
	FromStatus string `json:"from_status"`
	ToStatus   string `json:"to_status"`
}

// BuildFailedPayload is the typed payload for build.failed events (RFC-0001 §8.1).
type BuildFailedPayload struct {
	Repo      string `json:"repo"`
	CommitSHA string `json:"commit_sha"`
	Error     string `json:"error"`
}

// DiscoveryLoggedPayload is the typed payload for discovery.logged events (RFC-0001 §8.1).
type DiscoveryLoggedPayload struct {
	Summary string `json:"summary"`
	Detail  string `json:"detail"`
}

// MessagePostedPayload is the typed payload for message.posted events (RFC-0001 §8.1).
type MessagePostedPayload struct {
	Text string `json:"text"`
}

// ReviewRequestedPayload is the typed payload for review.requested events (RFC-0001 §8.1).
type ReviewRequestedPayload struct {
	PRUrl  string `json:"pr_url"`
	Repo   string `json:"repo"`
	Author string `json:"author"`
}
