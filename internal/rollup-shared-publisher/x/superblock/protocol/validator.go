package protocol

import (
	"fmt"

	pb "github.com/ethereum/go-ethereum/internal/rollup-shared-publisher/proto/rollup/v1"
)

// basicValidator implements basic validation for SBCP messages
type basicValidator struct{}

// NewBasicValidator creates a new basic SBCP message validator
func NewBasicValidator() Validator {
	return &basicValidator{}
}

// ValidateStartSlot validates StartSlot messages
func (v *basicValidator) ValidateStartSlot(startSlot *pb.StartSlot) error {
	if startSlot == nil {
		return fmt.Errorf("StartSlot message is nil")
	}

	if startSlot.Slot == 0 {
		return fmt.Errorf("invalid slot number: %d", startSlot.Slot)
	}

	if startSlot.NextSuperblockNumber == 0 {
		return fmt.Errorf("invalid superblock number: %d", startSlot.NextSuperblockNumber)
	}

	if len(startSlot.L2BlocksRequest) == 0 {
		return fmt.Errorf("no L2 block requests in StartSlot")
	}

	// Validate each L2 block request
	for i, req := range startSlot.L2BlocksRequest {
		if err := v.validateL2BlockRequest(req); err != nil {
			return fmt.Errorf("invalid L2 block request at index %d: %w", i, err)
		}
	}

	return nil
}

// ValidateRequestSeal validates RequestSeal messages
func (v *basicValidator) ValidateRequestSeal(requestSeal *pb.RequestSeal) error {
	if requestSeal == nil {
		return fmt.Errorf("RequestSeal message is nil")
	}

	if requestSeal.Slot == 0 {
		return fmt.Errorf("invalid slot number: %d", requestSeal.Slot)
	}

	// IncludedXts can be empty (no cross-chain transactions in this slot)
	for i, xtID := range requestSeal.IncludedXts {
		if len(xtID) == 0 {
			return fmt.Errorf("empty XT ID at index %d", i)
		}
	}

	return nil
}

// ValidateL2Block validates L2Block messages
func (v *basicValidator) ValidateL2Block(l2Block *pb.L2Block) error {
	if l2Block == nil {
		return fmt.Errorf("L2Block message is nil")
	}

	if l2Block.Slot == 0 {
		return fmt.Errorf("invalid slot number: %d", l2Block.Slot)
	}

	if len(l2Block.ChainId) == 0 {
		return fmt.Errorf("missing chain ID")
	}

	if l2Block.BlockNumber == 0 {
		return fmt.Errorf("invalid block number: %d", l2Block.BlockNumber)
	}

	if len(l2Block.BlockHash) == 0 {
		return fmt.Errorf("missing block hash")
	}

	if len(l2Block.Block) == 0 {
		return fmt.Errorf("missing block data")
	}

	return nil
}

// ValidateStartSC validates StartSC messages
func (v *basicValidator) ValidateStartSC(startSC *pb.StartSC) error {
	if startSC == nil {
		return fmt.Errorf("StartSC message is nil")
	}

	if startSC.Slot == 0 {
		return fmt.Errorf("invalid slot number: %d", startSC.Slot)
	}

	if len(startSC.XtId) == 0 {
		return fmt.Errorf("missing cross-chain transaction ID")
	}

	if startSC.XtRequest == nil {
		return fmt.Errorf("missing cross-chain transaction request")
	}

	// Validate the XTRequest
	if err := v.validateXTRequest(startSC.XtRequest); err != nil {
		return fmt.Errorf("invalid XTRequest: %w", err)
	}

	return nil
}

// ValidateRollBackAndStartSlot validates rollback messages
func (v *basicValidator) ValidateRollBackAndStartSlot(rb *pb.RollBackAndStartSlot) error {
	if rb == nil {
		return fmt.Errorf("RollBackAndStartSlot message is nil")
	}
	if rb.CurrentSlot == 0 {
		return fmt.Errorf("invalid current slot: %d", rb.CurrentSlot)
	}
	if rb.NextSuperblockNumber == 0 {
		return fmt.Errorf("invalid superblock number: %d", rb.NextSuperblockNumber)
	}
	if len(rb.L2BlocksRequest) == 0 {
		return fmt.Errorf("no L2 block requests in RollBackAndStartSlot")
	}
	for i, req := range rb.L2BlocksRequest {
		if err := v.validateL2BlockRequest(req); err != nil {
			return fmt.Errorf("invalid L2 block request at index %d: %w", i, err)
		}
	}
	return nil
}

// validateL2BlockRequest validates individual L2 block requests
func (v *basicValidator) validateL2BlockRequest(req *pb.L2BlockRequest) error {
	if req == nil {
		return fmt.Errorf("L2BlockRequest is nil")
	}

	if len(req.ChainId) == 0 {
		return fmt.Errorf("missing chain ID")
	}

	if req.BlockNumber == 0 {
		return fmt.Errorf("invalid block number: %d", req.BlockNumber)
	}

	// ParentHash can be empty for genesis blocks
	return nil
}

// validateXTRequest validates cross-chain transaction requests
func (v *basicValidator) validateXTRequest(xtReq *pb.XTRequest) error {
	if xtReq == nil {
		return fmt.Errorf("XTRequest is nil")
	}

	if len(xtReq.Transactions) == 0 {
		return fmt.Errorf("no transactions in XTRequest")
	}

	// Validate each transaction request
	for i, txReq := range xtReq.Transactions {
		if err := v.validateTransactionRequest(txReq); err != nil {
			return fmt.Errorf("invalid transaction request at index %d: %w", i, err)
		}
	}

	return nil
}

// validateTransactionRequest validates individual transaction requests
func (v *basicValidator) validateTransactionRequest(txReq *pb.TransactionRequest) error {
	if txReq == nil {
		return fmt.Errorf("TransactionRequest is nil")
	}

	if len(txReq.ChainId) == 0 {
		return fmt.Errorf("missing chain ID")
	}

	if len(txReq.Transaction) == 0 {
		return fmt.Errorf("no transactions provided")
	}

	return nil
}
