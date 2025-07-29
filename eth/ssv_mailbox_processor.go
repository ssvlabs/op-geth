package eth

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"encoding/hex"
	"fmt"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/ssv"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	network "github.com/ethereum/go-ethereum/internal/publisherapi/spnetwork"
	spconsensus "github.com/ethereum/go-ethereum/internal/sp/consensus"
	sptypes "github.com/ethereum/go-ethereum/internal/sp/proto"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/rpc"
	"math/big"
	"strconv"
	"strings"
	"time"
)

const mailboxABI = `[{"type":"constructor","inputs":[{"name":"_coordinator","type":"address","internalType":"address"}],"stateMutability":"nonpayable"},{"type":"function","name":"clear","inputs":[],"outputs":[],"stateMutability":"nonpayable"},{"type":"function","name":"coordinator","inputs":[],"outputs":[{"name":"","type":"address","internalType":"address"}],"stateMutability":"view"},{"type":"function","name":"getKey","inputs":[{"name":"chainSrc","type":"uint256","internalType":"uint256"},{"name":"chainDest","type":"uint256","internalType":"uint256"},{"name":"sender","type":"address","internalType":"address"},{"name":"receiver","type":"address","internalType":"address"},{"name":"sessionId","type":"uint256","internalType":"uint256"},{"name":"label","type":"bytes","internalType":"bytes"}],"outputs":[{"name":"key","type":"bytes32","internalType":"bytes32"}],"stateMutability":"pure"},{"type":"function","name":"inbox","inputs":[{"name":"key","type":"bytes32","internalType":"bytes32"}],"outputs":[{"name":"message","type":"bytes","internalType":"bytes"}],"stateMutability":"view"},{"type":"function","name":"keyListInbox","inputs":[{"name":"","type":"uint256","internalType":"uint256"}],"outputs":[{"name":"","type":"bytes32","internalType":"bytes32"}],"stateMutability":"view"},{"type":"function","name":"keyListOutbox","inputs":[{"name":"","type":"uint256","internalType":"uint256"}],"outputs":[{"name":"","type":"bytes32","internalType":"bytes32"}],"stateMutability":"view"},{"type":"function","name":"outbox","inputs":[{"name":"key","type":"bytes32","internalType":"bytes32"}],"outputs":[{"name":"message","type":"bytes","internalType":"bytes"}],"stateMutability":"view"},{"type":"function","name":"putInbox","inputs":[{"name":"chainSrc","type":"uint256","internalType":"uint256"},{"name":"chainDest","type":"uint256","internalType":"uint256"},{"name":"receiver","type":"address","internalType":"address"},{"name":"sessionId","type":"uint256","internalType":"uint256"},{"name":"data","type":"bytes","internalType":"bytes"},{"name":"label","type":"bytes","internalType":"bytes"}],"outputs":[],"stateMutability":"nonpayable"},{"type":"function","name":"read","inputs":[{"name":"chainSrc","type":"uint256","internalType":"uint256"},{"name":"chainDest","type":"uint256","internalType":"uint256"},{"name":"sender","type":"address","internalType":"address"},{"name":"receiver","type":"address","internalType":"address"},{"name":"sessionId","type":"uint256","internalType":"uint256"},{"name":"label","type":"bytes","internalType":"bytes"}],"outputs":[{"name":"message","type":"bytes","internalType":"bytes"}],"stateMutability":"view"},{"type":"function","name":"write","inputs":[{"name":"chainSrc","type":"uint256","internalType":"uint256"},{"name":"chainDest","type":"uint256","internalType":"uint256"},{"name":"receiver","type":"address","internalType":"address"},{"name":"sessionId","type":"uint256","internalType":"uint256"},{"name":"data","type":"bytes","internalType":"bytes"},{"name":"label","type":"bytes","internalType":"bytes"}],"outputs":[],"stateMutability":"nonpayable"},{"type":"error","name":"InvalidCoordinator","inputs":[]}]`

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
	sequencerKey     *ecdsa.PrivateKey
	sequencerAddr    common.Address
}

func NewMailboxProcessor(chainID uint64, mailboxAddrs []common.Address, sequencerClients map[string]network.Client, coordinator *spconsensus.Coordinator, sequencerKey *ecdsa.PrivateKey, sequencerAddr common.Address, backend *EthAPIBackend) *MailboxProcessor {
	return &MailboxProcessor{
		chainID:          chainID,
		mailboxAddresses: mailboxAddrs,
		sequencerClients: sequencerClients,
		coordinator:      coordinator,
		backend:          backend,
		sequencerKey:     sequencerKey,
		sequencerAddr:    sequencerAddr,
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

	log.Info("[SSV] Analyzing transaction trace",
		"txHash", tx.Hash().Hex(),
		"success", simState.OriginalSuccess,
		"operations", len(traceResult.Operations))

	for i, op := range traceResult.Operations {
		// Only analyze calls to mailbox contracts
		if !mp.isMailboxAddress(op.Address) {
			continue
		}

		log.Debug("[SSV] Found mailbox operation",
			"index", i,
			"type", op.Type.String(),
			"address", op.Address.Hex(),
			"from", op.From.Hex(),
			"callDataLen", len(op.CallData))

		// Handle both CALL (write) and STATICCALL (read) operations
		if (op.Type == vm.CALL || op.Type == vm.STATICCALL) && len(op.CallData) >= 4 {
			call, err := mp.parseMailboxCall(op.CallData)
			if err != nil {
				log.Debug("[SSV] Failed to parse mailbox call", "error", err)
				continue
			}

			log.Info("[SSV] Parsed mailbox call",
				"isRead", call.IsRead,
				"isWrite", call.IsWrite,
				"chainSrc", call.ChainSrc,
				"chainDest", call.ChainDest,
				"sender", call.Sender.Hex(),
				"receiver", call.Receiver.Hex(),
				"sessionId", call.SessionId)

			// Check for cross-rollup read dependency
			if call.IsRead {
				log.Info("[SSV] Processing read operation",
					"chainSrc", call.ChainSrc.Uint64(),
					"chainDest", call.ChainDest.Uint64(),
					"localChainID", mp.chainID,
					"shouldCreateDep", call.ChainDest.Uint64() != mp.chainID)

				// If we're reading from a different source chain, this is a dependency
				if call.ChainDest.Uint64() != mp.chainID {
					dep := CrossRollupDependency{
						SourceChainID: call.ChainDest.Uint64(),
						DestChainID:   mp.chainID,
						Sender:        call.Sender,
						Receiver:      call.Receiver,
						SessionID:     call.SessionId,
						Label:         call.Label,
						RequiredData:  true,
						IsInboxRead:   true,
					}

					simState.Dependencies = append(simState.Dependencies, dep)
					simState.RequiresCoordination = true

					log.Info("[SSV] Detected cross-rollup read dependency",
						"sourceChain", dep.SourceChainID,
						"destChain", dep.DestChainID,
						"sender", dep.Sender.Hex(),
						"receiver", dep.Receiver.Hex(),
						"sessionId", dep.SessionID)
				} else {
					log.Debug("[SSV] Local chain read, no dependency needed",
						"chainSrc", call.ChainSrc.Uint64(),
						"localChain", mp.chainID)
				}
			}

			// Check for cross-rollup write (outbound message)
			if call.IsWrite {
				// If we're writing to a different destination chain, this is an outbound message
				if call.ChainDest.Uint64() != mp.chainID {
					msg := CrossRollupMessage{
						SourceChainID: call.ChainSrc.Uint64(),
						DestChainID:   call.ChainDest.Uint64(),
						Sender:        op.From,
						Receiver:      call.Receiver,
						SessionID:     call.SessionId,
						Data:          call.Data,
						Label:         call.Label,
						MessageType:   "mailbox_write",
						IsOutboxWrite: true,
					}

					simState.OutboundMessages = append(simState.OutboundMessages, msg)
					simState.RequiresCoordination = true

					log.Info("[SSV] Detected cross-rollup write (outbound message)",
						"sourceChain", msg.SourceChainID,
						"destChain", msg.DestChainID,
						"sender", msg.Sender.Hex(),
						"receiver", msg.Receiver.Hex(),
						"sessionId", msg.SessionID,
						"dataLen", len(msg.Data))
				} else {
					log.Debug("[SSV] Local chain write, no cross-rollup message needed",
						"chainDest", call.ChainDest.Uint64(),
						"localChain", mp.chainID)
				}
			}
		} else if op.Type != vm.CALL && op.Type != vm.STATICCALL {
			log.Debug("[SSV] Ignoring non-CALL/STATICCALL operation to mailbox",
				"type", op.Type.String(),
				"address", op.Address.Hex())
		}
	}

	log.Info("[SSV] Transaction analysis complete",
		"txHash", tx.Hash().Hex(),
		"requiresCoordination", simState.RequiresCoordination,
		"dependencies", len(simState.Dependencies),
		"outboundMessages", len(simState.OutboundMessages))

	// Log detailed dependency information
	for i, dep := range simState.Dependencies {
		log.Info("[SSV] Dependency details",
			"index", i,
			"sourceChain", dep.SourceChainID,
			"destChain", dep.DestChainID,
			"sessionId", dep.SessionID,
			"label", string(dep.Label))
	}

	// Log detailed outbound message information
	for i, msg := range simState.OutboundMessages {
		log.Info("[SSV] Outbound message details",
			"index", i,
			"sourceChain", msg.SourceChainID,
			"destChain", msg.DestChainID,
			"sessionId", msg.SessionID,
			"dataLen", len(msg.Data),
			"label", string(msg.Label))
	}

	return simState, nil
}

func (mp *MailboxProcessor) parseMailboxCall(callData []byte) (*MailboxCall, error) {
	if len(callData) < 4 {
		return nil, fmt.Errorf("invalid call data length")
	}

	methodSig := callData[:4]

	// Parse using method signatures directly
	parsedABI, err := abi.JSON(strings.NewReader(mailboxABI))
	if err != nil {
		return nil, err
	}

	// Check method by comparing signatures
	if bytes.Equal(methodSig, parsedABI.Methods["read"].ID) {
		call, err := mp.parseReadCall(callData[4:])
		if err != nil {
			return nil, err
		}
		call.IsRead = true
		return call, nil
	}

	if bytes.Equal(methodSig, parsedABI.Methods["write"].ID) {
		call, err := mp.parseWriteCall(callData[4:])
		if err != nil {
			return nil, err
		}
		call.IsWrite = true
		return call, nil
	}

	return nil, fmt.Errorf("unknown mailbox method")
}

func (mp *MailboxProcessor) parseReadCall(data []byte) (*MailboxCall, error) {
	parsedABI, _ := abi.JSON(strings.NewReader(mailboxABI))
	values, err := parsedABI.Methods["read"].Inputs.Unpack(data)
	if err != nil {
		return nil, err
	}

	return &MailboxCall{
		ChainSrc:  values[0].(*big.Int),
		ChainDest: values[1].(*big.Int),
		Sender:    values[2].(common.Address),
		Receiver:  values[3].(common.Address),
		SessionId: values[4].(*big.Int),
		Label:     values[5].([]byte),
	}, nil
}

func (mp *MailboxProcessor) parseWriteCall(data []byte) (*MailboxCall, error) {
	parsedABI, _ := abi.JSON(strings.NewReader(mailboxABI))
	values, err := parsedABI.Methods["write"].Inputs.Unpack(data)
	if err != nil {
		return nil, err
	}

	return &MailboxCall{
		ChainSrc:  values[0].(*big.Int),
		ChainDest: values[1].(*big.Int),
		Receiver:  values[2].(common.Address),
		SessionId: values[3].(*big.Int),
		Data:      values[4].([]byte),
		Label:     values[5].([]byte),
	}, nil
}

func (mp *MailboxProcessor) handleCrossRollupCoordination(ctx context.Context, simState *SimulationState, xtID *sptypes.XtID, startNonce uint64) error {
	log.Info("[SSV] Starting cross-rollup coordination", "xtID", xtID.Hex())

	// Send outbound CIRC messages
	for _, outMsg := range simState.OutboundMessages {
		if err := mp.sendCIRCMessage(ctx, &outMsg, xtID); err != nil {
			return fmt.Errorf("failed to send CIRC message: %w", err)
		}
	}

	nonce := startNonce

	// Wait for required CIRC messages and create putInbox transactions
	for _, dep := range simState.Dependencies {
		log.Info("[SSV] Await for CIRC message",
			"from", dep.SourceChainID,
			"to", dep.DestChainID,
			"sessionId", dep.SessionID,
		)

		circMsg, err := mp.waitForCIRCMessage(ctx, xtID, hex.EncodeToString(new(big.Int).SetUint64(dep.SourceChainID).Bytes()))
		if err != nil {
			return fmt.Errorf("failed to wait for CIRC message: %w", err)
		}

		err = mp.createAndSubmitPutInboxTx(ctx, dep, circMsg.Data[0], nonce)
		if err != nil {
			return fmt.Errorf("failed to createAndSubmitPutInboxTx: %v", err)
		}

		nonce++
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

	log.Info("[SSV] Attempting to send CIRC",
		"destChainStr", destChainStr,
		"availableClients", len(mp.sequencerClients),
		"clientExists", exists)

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

func (mp *MailboxProcessor) waitForCIRCMessage(ctx context.Context, xtID *sptypes.XtID, sourceChainID string) (*sptypes.CIRCMessage, error) {
	// Wait for CIRC message with timeout
	timeout := time.NewTimer(2 * time.Minute)
	defer timeout.Stop()

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timeout.C:
			return nil, fmt.Errorf("timeout waiting for CIRC message from chain %s", sourceChainID)
		case <-ticker.C:
			circMsg, err := mp.coordinator.ConsumeCIRCMessage(xtID, sourceChainID)
			if err != nil {
				continue // Keep waiting
			}

			log.Info("[SSV] Received CIRC message",
				"from", sourceChainID,
				"dataLen", len(circMsg.Data[0]),
			)

			return circMsg, nil
		}
	}
}

func (mp *MailboxProcessor) createAndSubmitPutInboxTx(ctx context.Context, dep CrossRollupDependency, data []byte, nonce uint64) error {
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
		"nonce", nonce,
		"mailbox", mailboxAddr.Hex(),
		"sourceChain", dep.SourceChainID,
		"destChain", dep.DestChainID,
		"sessionId", dep.SessionID,
		"dataLen", len(callData),
	)

	txData := &types.DynamicFeeTx{
		ChainID:    new(big.Int).SetUint64(mp.chainID),
		Nonce:      nonce,
		GasTipCap:  big.NewInt(1000000000),
		GasFeeCap:  big.NewInt(20000000000),
		Gas:        300000,
		To:         &mailboxAddr,
		Value:      big.NewInt(0),
		Data:       callData,
		AccessList: nil,
	}

	tx := types.NewTx(txData)
	signedTx, err := types.SignTx(tx, types.NewLondonSigner(new(big.Int).SetUint64(mp.chainID)), mp.sequencerKey)
	if err != nil {
		return fmt.Errorf("failed to sign tx %v", err)
	}

	type submitTx interface {
		SubmitSequencerTransaction(ctx context.Context, tx *types.Transaction, isPutInbox bool) error
	}

	stb, ok := mp.backend.(submitTx)
	if !ok {
		return fmt.Errorf("backend does not implement required SubmitSequencerTransaction method")
	}

	return stb.SubmitSequencerTransaction(ctx, signedTx, true)
}

func (mp *MailboxProcessor) isMailboxAddress(addr common.Address) bool {
	for _, mailboxAddr := range mp.mailboxAddresses {
		if addr == mailboxAddr {
			return true
		}
	}
	return false
}
