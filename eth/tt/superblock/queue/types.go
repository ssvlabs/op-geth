package queue

import (
	"time"

	pb "github.com/ssvlabs/rollup-shared-publisher/proto/rollup/v1"
)

type QueuedXTRequest struct {
	Request     *pb.XTRequest `json:"request"`
	XtID        []byte        `json:"xt_id"`
	Priority    int64         `json:"priority"`
	SubmittedAt time.Time     `json:"submitted_at"`
	ExpiresAt   time.Time     `json:"expires_at"`
	Attempts    int           `json:"attempts"`
	From        string        `json:"from"`
}
