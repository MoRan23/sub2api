package service

import (
	"errors"

	"github.com/Wei-Shaw/sub2api/internal/pkg/runtimeobs"
	"github.com/gin-gonic/gin"
)

const openAIWSBodyObservationKey = "openai_ws_body_observation"

// StartOpenAIWSBodyObservation is called after the first physical downstream
// read, before admission or slot waits. Its caller owns Close across retries.
func StartOpenAIWSBodyObservation(c *gin.Context, size int) *runtimeobs.BodyTracker {
	tracker := openAIWSBodyObservation(c)
	if tracker == nil {
		tracker = runtimeobs.NewBodyTracker(&runtimeobs.Default)
		c.Set(openAIWSBodyObservationKey, tracker)
	}
	tracker.EnsureCurrent(int64(size))
	return tracker
}
func openAIWSBodyObservation(c *gin.Context) *runtimeobs.BodyTracker {
	if c == nil {
		return nil
	}
	value, _ := c.Get(openAIWSBodyObservationKey)
	tracker, _ := value.(*runtimeobs.BodyTracker)
	return tracker
}
func finishOpenAIWSObservedTurn(tracker *runtimeobs.BodyTracker, err error) {
	var failover *UpstreamFailoverError
	if !errors.As(err, &failover) {
		tracker.CompleteCurrent()
	}
}
