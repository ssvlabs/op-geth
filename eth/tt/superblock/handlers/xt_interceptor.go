package handlers

import (
	"context"
	"fmt"

	pb "github.com/ssvlabs/rollup-shared-publisher/proto/rollup/v1"
	"github.com/ssvlabs/rollup-shared-publisher/x/superblock"
)

type XTInterceptor struct {
	coordinator *superblock.Coordinator
	trackFn     func(string) // Callback to track slot-managed XTs
	fallback    func(context.Context, string, *pb.Message) error
}

func NewXTInterceptor(coordinator *superblock.Coordinator, trackFn func(string)) *XTInterceptor {
	return &XTInterceptor{
		coordinator: coordinator,
		trackFn:     trackFn,
	}
}

func (i *XTInterceptor) SetFallback(f func(context.Context, string, *pb.Message) error) {
	i.fallback = f
}

func (i *XTInterceptor) CanHandle(msg *pb.Message) bool {
	_, ok := msg.Payload.(*pb.Message_XtRequest)
	return ok
}

func (i *XTInterceptor) Handle(ctx context.Context, from string, msg *pb.Message) error {
	payload := msg.Payload.(*pb.Message_XtRequest)
	xtReq := payload.XtRequest
	xtID, _ := xtReq.XtID()

	// Check if we're in a slot and Free state
	if i.coordinator.GetSlotState() == superblock.SlotStateFree {
		i.coordinator.Logger().Info().
			Str("xt_id", xtID.Hex()).
			Uint64("slot", i.coordinator.GetCurrentSlot()).
			Msg("Queueing XT for slot processing")

		// Mark as slot-managed
		if i.trackFn != nil {
			i.trackFn(xtID.Hex())
		}

		// Queue it
		return i.coordinator.SubmitXTRequest(ctx, from, xtReq)
	}

	// Not in slot or not Free - pass through
	if i.fallback != nil {
		i.coordinator.Logger().Debug().
			Str("xt_id", xtID.Hex()).
			Msg("Passing XT to publisher (not in Free state)")
		return i.fallback(ctx, from, msg)
	}

	return fmt.Errorf("cannot process XTRequest in state %s", i.coordinator.GetSlotState())
}
