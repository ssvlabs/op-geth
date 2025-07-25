package eth

import (
	"bytes"
	"context"
	"fmt"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/ssv"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	network "github.com/ethereum/go-ethereum/internal/publisherapi/spnetwork"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/rpc"
	spconsensus "github.com/ssvlabs/rollup-shared-publisher/pkg/consensus"
	sptypes "github.com/ssvlabs/rollup-shared-publisher/pkg/proto"
	"math/big"
	"strconv"
	"strings"
	"time"
)

const mailboxABI = `[{"inputs":[{"internalType":"uint256","name":"chainSrc","type":"uint256"},{"internalType":"uint256","name":"chainDest","type":"uint256"},{"internalType":"address","name":"sender","type":"address"},{"internalType":"address","name":"receiver","type":"address"},{"internalType":"uint256","name":"sessionId","type":"uint256"},{"internalType":"bytes","name":"label","type":"bytes"}],"name":"read","outputs":[{"internalType":"bytes","name":"message","type":"bytes"}],"stateMutability":"view","type":"function"},{"inputs":[{"internalType":"uint256","name":"chainSrc","type":"uint256"},{"internalType":"uint256","name":"chainDest","type":"uint256"},{"internalType":"address","name":"receiver","type":"address"},{"internalType":"uint256","name":"sessionId","type":"uint256"},{"internalType":"bytes","name":"data","type":"bytes"},{"internalType":"bytes","name":"label","type":"bytes"}],"name":"write","outputs":[],"stateMutability":"nonpayable","type":"function"},{"inputs":[{"internalType":"uint256","name":"chainSrc","type":"uint256"},{"internalType":"uint256","name":"chainDest","type":"uint256"},{"internalType":"address","name":"receiver","type":"address"},{"internalType":"uint256","name":"sessionId","type":"uint256"},{"internalType":"bytes","name":"data","type":"bytes"},{"internalType":"bytes","name":"label","type":"bytes"}],"name":"putInbox","outputs":[],"stateMutability":"nonpayable","type":"function"}]`

type MailboxCall struct {
	ChainSrc  *big.Int
	ChainDest *big.Int
	Sender    common.Address
	Receiver  common.Address
	SessionId *big.Int
	Data      []byte
	Label     []byte
	IsRead    bool
	IsWrite   bool
}

type CrossRollupDependency struct {
	SourceChainID uint64
	DestChainID   uint64
	Sender        common.Address
	Receiver      common.Address
	SessionID     *big.Int
	Label         []byte
	RequiredData  bool
	IsInboxRead   bool
}

type CrossRollupMessage struct {
	SourceChainID uint64
	DestChainID   uint64
	Sender        common.Address
	Receiver      common.Address
	SessionID     *big.Int
	Data          []byte
	Label         []byte
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
	sequencerClients map[string]network.Client
	coordinator      *spconsensus.Coordinator
	backend          interface{}
}

func NewMailboxProcessor(chainID uint64, mailboxAddrs []common.Address, sequencerClients map[string]network.Client, coordinator *spconsensus.Coordinator, backend interface{}) *MailboxProcessor {
	return &MailboxProcessor{
		chainID:          chainID,
		mailboxAddresses: mailboxAddrs,
		sequencerClients: sequencerClients,
		coordinator:      coordinator,
		backend:          backend,
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

	return simState, nil
}

func (mp *MailboxProcessor) analyzeTransaction(ctx context.Context, backend interface{}, tx *types.Transaction) (*SimulationState, error) {
	blockNrOrHash := rpc.BlockNumberOrHashWithNumber(rpc.LatestBlockNumber)

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

		// For CALL operations, analyze the call data
		if op.Type == vm.CALL && len(op.CallData) > 0 {
			if dep := mp.analyzeMailboxRead(op); dep != nil {
				simState.Dependencies = append(simState.Dependencies, *dep)
				simState.RequiresCoordination = true
				log.Info("[SSV] Detected cross-rollup dependency",
					"sourceChain", dep.SourceChainID,
					"destChain", dep.DestChainID,
					"address", op.Address.Hex())
			}

			if msg := mp.analyzeMailboxWrite(op); msg != nil {
				simState.OutboundMessages = append(simState.OutboundMessages, *msg)
				simState.RequiresCoordination = true
				log.Info("[SSV] Detected outbound message",
					"sourceChain", msg.SourceChainID,
					"destChain", msg.DestChainID,
					"address", op.Address.Hex())
			}
		}
	}

	return simState, nil
}

func (mp *MailboxProcessor) parseMailboxCall(callData []byte) (*MailboxCall, error) {
	if len(callData) < 4 {
		return nil, fmt.Errorf("invalid call data length")
	}

	parsedABI, err := abi.JSON(strings.NewReader(mailboxABI))
	if err != nil {
		return nil, err
	}

	methodSig := callData[:4]

	// Check if it's a read call
	if bytes.Equal(methodSig, parsedABI.Methods["read"].ID) {
		values, err := parsedABI.Methods["read"].Inputs.Unpack(callData[4:])
		if err != nil {
			return nil, err
		}

		if len(values) != 6 {
			return nil, fmt.Errorf("expected 6 values for read call, got %d", len(values))
		}

		chainSrc, ok := values[0].(*big.Int)
		if !ok {
			return nil, fmt.Errorf("invalid chainSrc type")
		}

		chainDest, ok := values[1].(*big.Int)
		if !ok {
			return nil, fmt.Errorf("invalid chainDest type")
		}

		sender, ok := values[2].(common.Address)
		if !ok {
			return nil, fmt.Errorf("invalid sender type")
		}

		receiver, ok := values[3].(common.Address)
		if !ok {
			return nil, fmt.Errorf("invalid receiver type")
		}

		sessionId, ok := values[4].(*big.Int)
		if !ok {
			return nil, fmt.Errorf("invalid sessionId type")
		}

		label, ok := values[5].([]byte)
		if !ok {
			return nil, fmt.Errorf("invalid label type")
		}

		return &MailboxCall{
			ChainSrc:  chainSrc,
			ChainDest: chainDest,
			Sender:    sender,
			Receiver:  receiver,
			SessionId: sessionId,
			Label:     label,
			IsRead:    true,
		}, nil
	}

	// Check if it's a write call
	if bytes.Equal(methodSig, parsedABI.Methods["write"].ID) {
		values, err := parsedABI.Methods["write"].Inputs.Unpack(callData[4:])
		if err != nil {
			return nil, err
		}

		if len(values) != 6 {
			return nil, fmt.Errorf("expected 6 values for write call, got %d", len(values))
		}

		chainSrc, ok := values[0].(*big.Int)
		if !ok {
			return nil, fmt.Errorf("invalid chainSrc type")
		}

		chainDest, ok := values[1].(*big.Int)
		if !ok {
			return nil, fmt.Errorf("invalid chainDest type")
		}

		receiver, ok := values[2].(common.Address)
		if !ok {
			return nil, fmt.Errorf("invalid receiver type")
		}

		sessionId, ok := values[3].(*big.Int)
		if !ok {
			return nil, fmt.Errorf("invalid sessionId type")
		}

		data, ok := values[4].([]byte)
		if !ok {
			return nil, fmt.Errorf("invalid data type")
		}

		label, ok := values[5].([]byte)
		if !ok {
			return nil, fmt.Errorf("invalid label type")
		}

		return &MailboxCall{
			ChainSrc:  chainSrc,
			ChainDest: chainDest,
			Sender:    common.Address{}, // msg.sender not in call data
			Receiver:  receiver,
			SessionId: sessionId,
			Data:      data,
			Label:     label,
			IsWrite:   true,
		}, nil
	}

	return nil, fmt.Errorf("unknown method signature")
}

func (mp *MailboxProcessor) analyzeMailboxRead(op ssv.SSVOperation) *CrossRollupDependency {
	call, err := mp.parseMailboxCall(op.CallData)
	if err != nil || !call.IsRead {
		return nil
	}

	// If this is reading from a different chain, it's a dependency
	if call.ChainSrc.Uint64() != mp.chainID {
		return &CrossRollupDependency{
			SourceChainID: call.ChainSrc.Uint64(),
			DestChainID:   call.ChainDest.Uint64(),
			Sender:        call.Sender,
			Receiver:      call.Receiver,
			SessionID:     call.SessionId,
			Label:         call.Label,
			RequiredData:  true,
			IsInboxRead:   true,
		}
	}
	return nil
}

func (mp *MailboxProcessor) analyzeMailboxWrite(op ssv.SSVOperation) *CrossRollupMessage {
	call, err := mp.parseMailboxCall(op.CallData)
	if err != nil || !call.IsWrite {
		return nil
	}

	// Use msg.sender from the operation for the actual sender
	sender := op.From

	// If this is writing to a different chain, it's an outbound message
	if call.ChainDest.Uint64() != mp.chainID {
		return &CrossRollupMessage{
			SourceChainID: call.ChainSrc.Uint64(),
			DestChainID:   call.ChainDest.Uint64(),
			Sender:        sender,
			Receiver:      call.Receiver,
			SessionID:     call.SessionId,
			Data:          call.Data,
			Label:         call.Label,
			MessageType:   "outbox_data",
			IsOutboxWrite: true,
		}
	}
	return nil
}

func (mp *MailboxProcessor) handleCrossRollupCoordination(ctx context.Context, simState *SimulationState, xtID *sptypes.XtID) error {
	log.Info("[SSV] Starting cross-rollup coordination", "xtID", xtID.Hex())

	// Send outbound CIRC messages
	for _, outMsg := range simState.OutboundMessages {
		if err := mp.sendCIRCMessage(ctx, &outMsg, xtID); err != nil {
			return fmt.Errorf("failed to send CIRC message: %w", err)
		}
	}

	// Wait for required CIRC messages and create putInbox transactions
	for _, dep := range simState.Dependencies {
		if err := mp.waitForCIRCMessage(ctx, xtID, &dep); err != nil {
			return fmt.Errorf("failed to wait for CIRC message: %w", err)
		}
	}

	log.Info("[SSV] Cross-rollup coordination completed", "xtID", xtID.Hex())
	return nil
}

func (mp *MailboxProcessor) sendCIRCMessage(ctx context.Context, msg *CrossRollupMessage, xtID *sptypes.XtID) error {
	destChainStr := strconv.FormatUint(msg.DestChainID, 10)
	client, exists := mp.sequencerClients[destChainStr]
	if !exists {
		return fmt.Errorf("no client for destination chain %d", msg.DestChainID)
	}

	circMsg := &sptypes.CIRCMessage{
		SourceChain:      new(big.Int).SetUint64(msg.SourceChainID).Bytes(),
		DestinationChain: new(big.Int).SetUint64(msg.DestChainID).Bytes(),
		Source:           [][]byte{msg.Sender.Bytes()},
		Receiver:         [][]byte{msg.Receiver.Bytes()},
		XtId:             xtID,
		Label:            string(msg.Label),
		Data:             [][]byte{msg.Data},
	}

	spMsg := &sptypes.Message{
		SenderId: strconv.FormatUint(mp.chainID, 10),
		Payload: &sptypes.Message_CircMessage{
			CircMessage: circMsg,
		},
	}

	log.Info("[SSV] Sending CIRC message",
		"from", msg.SourceChainID,
		"to", msg.DestChainID,
		"sessionId", msg.SessionID,
	)

	return client.Send(ctx, spMsg)
}

func (mp *MailboxProcessor) waitForCIRCMessage(ctx context.Context, xtID *sptypes.XtID, dep *CrossRollupDependency) error {
	sourceChainStr := strconv.FormatUint(dep.SourceChainID, 10)

	// Wait for CIRC message with timeout
	timeout := time.NewTimer(30 * time.Second)
	defer timeout.Stop()

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	log.Info("[SSV] Waiting for CIRC message",
		"from", dep.SourceChainID,
		"to", dep.DestChainID,
		"sessionId", dep.SessionID,
	)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timeout.C:
			return fmt.Errorf("timeout waiting for CIRC message from chain %s", sourceChainStr)
		case <-ticker.C:
			circMsg, err := mp.coordinator.ConsumeCIRCMessage(xtID, sourceChainStr)
			if err != nil {
				continue // Keep waiting
			}

			log.Info("[SSV] Received CIRC message",
				"from", sourceChainStr,
				"dataLen", len(circMsg.Data[0]),
			)

			// Create putInbox transaction
			return mp.createAndSubmitPutInboxTx(ctx, dep, circMsg.Data[0])
		}
	}
}

func (mp *MailboxProcessor) createAndSubmitPutInboxTx(ctx context.Context, dep *CrossRollupDependency, data []byte) error {
	parsedABI, err := abi.JSON(strings.NewReader(mailboxABI))
	if err != nil {
		return err
	}

	callData, err := parsedABI.Pack("putInbox",
		new(big.Int).SetUint64(dep.SourceChainID),
		new(big.Int).SetUint64(dep.DestChainID),
		dep.Receiver,
		dep.SessionID,
		data,
		dep.Label,
	)
	if err != nil {
		return err
	}

	// Create transaction to mailbox
	mailboxAddr := mp.mailboxAddresses[0] // Use first mailbox address

	log.Info("[SSV] Created putInbox transaction",
		"mailbox", mailboxAddr.Hex(),
		"sourceChain", dep.SourceChainID,
		"destChain", dep.DestChainID,
		"sessionId", dep.SessionID,
		"dataLen", len(callData),
	)

	// TODO: Submit to txpool - this should create an actual transaction and submit it
	// For now just log that it would be submitted
	return nil
}

func (mp *MailboxProcessor) isMailboxAddress(addr common.Address) bool {
	for _, mailboxAddr := range mp.mailboxAddresses {
		if addr == mailboxAddr {
			return true
		}
	}
	return false
}
