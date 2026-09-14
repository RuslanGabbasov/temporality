package execution

const (
	EventExecutionCreated   = "execution.created"
	EventExecutionStarted   = "execution.started"
	EventExecutionProgress  = "execution.progress"
	EventExecutionFailed    = "execution.failed"
	EventExecutionCompleted = "execution.completed"
	EventExecutionCancelled = "execution.cancelled"
)

func CanonicalEventTypes() []string {
	return []string{EventExecutionCreated, EventExecutionStarted, EventExecutionProgress, EventExecutionFailed, EventExecutionCompleted, EventExecutionCancelled}
}

func IsCanonicalEventType(eventType string) bool {
	for _, candidate := range CanonicalEventTypes() {
		if eventType == candidate {
			return true
		}
	}
	return false
}

func EventTypeForStatus(status Status) (string, bool) {
	switch status {
	case StatusCreated:
		return EventExecutionCreated, true
	case StatusRunning:
		return EventExecutionStarted, true
	case StatusCompleted:
		return EventExecutionCompleted, true
	case StatusFailed:
		return EventExecutionFailed, true
	case StatusCancelled:
		return EventExecutionCancelled, true
	default:
		return "", false
	}
}
