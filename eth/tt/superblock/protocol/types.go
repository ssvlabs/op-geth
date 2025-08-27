package protocol

import (
	"fmt"

	pb "github.com/ssvlabs/rollup-shared-publisher/proto/rollup/v1"
)

// MessageType represents SBCP protocol message types
type MessageType int

const (
	_                       MessageType = iota
	MsgStartSlot                        // SP starts slot
	MsgRequestSeal                      // SP requests block seal
	MsgL2Block                          // Sequencer submits block
	MsgStartSC                          // SP starts a cross-chain transaction
	MsgRollBackAndStartSlot             // SP requests rollback and restart slot
)

// String returns a human-readable message type name
func (t MessageType) String() string {
	names := map[MessageType]string{
		MsgStartSlot:            "StartSlot",
		MsgRequestSeal:          "RequestSeal",
		MsgL2Block:              "L2Block",
		MsgStartSC:              "StartSC",
		MsgRollBackAndStartSlot: "RollBackAndStartSlot",
	}

	if name, ok := names[t]; ok {
		return name
	}
	return fmt.Sprintf("Unknown(%d)", t)
}

// IsValid returns true if a message type is valid
func (t MessageType) IsValid() bool {
	return t >= MsgStartSlot && t <= MsgRollBackAndStartSlot
}

// ClassifyMessage returns SBCP message type from a protobuf message
func ClassifyMessage(msg *pb.Message) (MessageType, bool) {
	if msg == nil || msg.Payload == nil {
		return 0, false
	}

	switch msg.Payload.(type) {
	case *pb.Message_StartSlot:
		return MsgStartSlot, true
	case *pb.Message_RequestSeal:
		return MsgRequestSeal, true
	case *pb.Message_L2Block:
		return MsgL2Block, true
	case *pb.Message_StartSc:
		return MsgStartSC, true
	case *pb.Message_RollBackAndStartSlot:
		return MsgRollBackAndStartSlot, true
	default:
		return 0, false
	}
}

// IsSBCPMessage returns true if the message belongs to SBCP protocol
func IsSBCPMessage(msg *pb.Message) bool {
	_, ok := ClassifyMessage(msg)
	return ok
}
