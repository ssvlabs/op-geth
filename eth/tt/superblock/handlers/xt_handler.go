package handlers

import (
	"context"

	pb "github.com/ssvlabs/rollup-shared-publisher/proto/rollup/v1"
	"github.com/ssvlabs/rollup-shared-publisher/x/superblock"
)

type XTHandler struct {
	coordinator *superblock.Coordinator
	queueFn     func(context.Context, string, *pb.XTRequest) error
}

func NewXTHandler(
	coordinator *superblock.Coordinator,
	queueFn func(context.Context, string, *pb.XTRequest) error,
) *XTHandler {
	return &XTHandler{
		coordinator: coordinator,
		queueFn:     queueFn,
	}
}

func (h *XTHandler) CanHandle(msg *pb.Message) bool {
	_, ok := msg.Payload.(*pb.Message_XtRequest)
	return ok
}

func (h *XTHandler) Handle(ctx context.Context, from string, msg *pb.Message) error {
	payload := msg.Payload.(*pb.Message_XtRequest)
	xtReq := payload.XtRequest

	// In SBCP mode, ALL XTRequests go through slot queue
	return h.queueFn(ctx, from, xtReq)
}
