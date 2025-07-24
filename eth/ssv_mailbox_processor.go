package eth

import (
	"context"
	"encoding/binary"
	"fmt"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/ssv"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/rpc"
)

type CrossRollupDependency struct {
	StorageKey   common.Hash
	RequiredData bool
	IsInboxRead  bool
}

type CrossRollupMessage struct {
	StorageKey    common.Hash
	StorageValue  []byte
	MessageType   string
	IsOutboxWrite bool
}

type SimulationState struct {
	OriginalSuccess      bool
	Dependencies         []CrossRollupDependency
	OutboundMessages     []CrossRollupMessage
	RequiresCoordination bool
}

type MailboxProcessor struct {
	chainID          uint64
	mailboxAddresses []common.Address
	circClient       interface{}
	twoPCClient      interface{}
}

func NewMailboxProcessor(chainID uint64, mailboxAddrs []common.Address) *MailboxProcessor {
	return &MailboxProcessor{
		chainID:          chainID,
		mailboxAddresses: mailboxAddrs,
	}
}

func (mp *MailboxProcessor) ProcessTransaction(ctx context.Context, backend interface{}, tx *types.Transaction, xtRequestId string) (*SimulationState, error) {
	log.Info("[SSV] Processing cross-rollup transaction", "txHash", tx.Hash().Hex(), "xtRequestId", xtRequestId)

	simState, err := mp.analyzeTransaction(ctx, backend, tx)
	if err != nil {
		return nil, fmt.Errorf("failed to analyze transaction: %w", err)
	}

	if !simState.RequiresCoordination {
		log.Info("[SSV] Transaction requires no cross-rollup coordination", "txHash", tx.Hash().Hex())
		return simState, nil
	}

	log.Info("[SSV] Transaction requires cross-rollup coordination",
		"txHash", tx.Hash().Hex(),
		"dependencies", len(simState.Dependencies),
		"outbound", len(simState.OutboundMessages))

	if err := mp.handleCrossRollupCoordination(ctx, simState, xtRequestId); err != nil {
		return simState, fmt.Errorf("cross-rollup coordination failed: %w", err)
	}

	return simState, nil
}

func (mp *MailboxProcessor) analyzeTransaction(ctx context.Context, backend interface{}, tx *types.Transaction) (*SimulationState, error) {
	blockNrOrHash := rpc.BlockNumberOrHashWithNumber(rpc.LatestBlockNumber)

	// Type assert backend to get the method we need
	type traceBackend interface {
		SimulateTransactionWithSSVTrace(ctx context.Context, tx *types.Transaction, blockNrOrHash rpc.BlockNumberOrHash) (*ssv.SSVTraceResult, error)
	}

	tb, ok := backend.(traceBackend)
	if !ok {
		return nil, fmt.Errorf("backend does not implement required tracing methods")
	}

	traceResult, err := tb.SimulateTransactionWithSSVTrace(ctx, tx, blockNrOrHash)
	if err != nil {
		return nil, fmt.Errorf("simulation failed: %w", err)
	}

	simState := &SimulationState{
		OriginalSuccess:  traceResult.ExecutionResult.Err == nil,
		Dependencies:     make([]CrossRollupDependency, 0),
		OutboundMessages: make([]CrossRollupMessage, 0),
	}

	for _, op := range traceResult.Operations {
		if !mp.isMailboxAddress(op.Address) {
			continue
		}

		switch op.Type {
		case vm.SLOAD:
			dep := mp.analyzeMailboxRead(op)
			if dep != nil {
				simState.Dependencies = append(simState.Dependencies, *dep)
				simState.RequiresCoordination = true
				log.Info("[SSV] Detected cross-rollup dependency",
					"key", common.BytesToHash(op.StorageKey).Hex(),
					"address", op.Address.Hex())
			}

		case vm.SSTORE:
			msg := mp.analyzeMailboxWrite(op)
			if msg != nil {
				simState.OutboundMessages = append(simState.OutboundMessages, *msg)
				simState.RequiresCoordination = true
				log.Info("[SSV] Detected outbound message",
					"key", common.BytesToHash(op.StorageKey).Hex(),
					"address", op.Address.Hex())
			}
		}
	}

	return simState, nil
}

func (mp *MailboxProcessor) analyzeMailboxRead(op ssv.SSVOperation) *CrossRollupDependency {
	storageKey := common.BytesToHash(op.StorageKey)

	if mp.isInboxSlot(storageKey) && mp.isEmptyValue(op.StorageValue) {
		return &CrossRollupDependency{
			StorageKey:   storageKey,
			RequiredData: true,
			IsInboxRead:  true,
		}
	}
	return nil
}

func (mp *MailboxProcessor) analyzeMailboxWrite(op ssv.SSVOperation) *CrossRollupMessage {
	storageKey := common.BytesToHash(op.StorageKey)

	if mp.isOutboxSlot(storageKey) {
		return &CrossRollupMessage{
			StorageKey:    storageKey,
			StorageValue:  op.StorageValue,
			MessageType:   "outbox_data",
			IsOutboxWrite: true,
		}
	}
	return nil
}

func (mp *MailboxProcessor) isInboxSlot(key common.Hash) bool {
	return mp.isMailboxMappingSlot(key, 1)
}

func (mp *MailboxProcessor) isOutboxSlot(key common.Hash) bool {
	return mp.isMailboxMappingSlot(key, 2)
}

// isMailboxMappingSlot checks if the given key corresponds to a mailbox slot derived from the mapping slot and address.
// Uses keccak256(abi.encode(mappingKey, mappingSlot)) calculation to verify match with the expected storage slot.
// mappingSlot is 1 for inbox and 2 for outbox.
// Assumes the first 20 bytes of the key contain the address.
// This is a simplified version and may need adjustments based on actual mailbox contract implementation.
func (mp *MailboxProcessor) isMailboxMappingSlot(key common.Hash, mappingSlot uint64) bool {
	potentialAddr := common.BytesToAddress(key[:20])

	data := make([]byte, 64)
	copy(data[12:32], potentialAddr.Bytes())
	binary.BigEndian.PutUint64(data[56:64], mappingSlot)
	expectedSlot := crypto.Keccak256Hash(data)

	log.Info("[SSV] Checking mailbox mapping slot",
		"key", key.Hex(),
		"potentialAddr", potentialAddr.Hex(),
		"mappingSlot", mappingSlot,
		"expectedSlot", expectedSlot.Hex())

	return key == expectedSlot
}

func (mp *MailboxProcessor) isEmptyValue(value []byte) bool {
	for _, b := range value {
		if b != 0 {
			return false
		}
	}
	return true
}

func (mp *MailboxProcessor) isMailboxAddress(addr common.Address) bool {
	for _, mailboxAddr := range mp.mailboxAddresses {
		if addr == mailboxAddr {
			return true
		}
	}
	return false
}

func (mp *MailboxProcessor) handleCrossRollupCoordination(ctx context.Context, simState *SimulationState, xtRequestId string) error {
	log.Info("[SSV] Starting cross-rollup coordination", "xtRequestId", xtRequestId)

	if len(simState.Dependencies) > 0 {
		log.Info("[SSV] Would request cross-rollup data", "count", len(simState.Dependencies))
	}

	if len(simState.OutboundMessages) > 0 {
		log.Info("[SSV] Would send cross-rollup data", "count", len(simState.OutboundMessages))
	}

	log.Info("[SSV] Would participate in 2PC voting", "xtRequestId", xtRequestId)

	return nil
}

func (mp *MailboxProcessor) CreatePutInboxTransaction(crossRollupData []byte, key common.Hash) (*types.Transaction, error) {
	log.Info("[SSV] CreatePutInboxTransaction called", "key", key.Hex())
	return nil, fmt.Errorf("putInbox transaction creation requires sequencer private key - not needed for current analysis phase")
}
