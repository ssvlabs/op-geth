package eth

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/ssv"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/eth/tracers"
	"github.com/ethereum/go-ethereum/eth/tracers/native"
	"github.com/ethereum/go-ethereum/internal/ethapi"
	rollupv1 "github.com/ethereum/go-ethereum/internal/rollup-shared-publisher/proto/rollup/v1"
	spconsensus "github.com/ethereum/go-ethereum/internal/rollup-shared-publisher/x/consensus"
	"github.com/ethereum/go-ethereum/internal/rollup-shared-publisher/x/superblock/sequencer"
	"github.com/ethereum/go-ethereum/internal/rollup-shared-publisher/x/transport"
	"github.com/ethereum/go-ethereum/log"

	"math/big"
	"strconv"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/rpc"
)

const mailboxABI = `[{"type":"constructor","inputs":[{"name":"_coordinator","type":"address","internalType":"address"}],"stateMutability":"nonpayable"},{"type":"function","name":"clear","inputs":[],"outputs":[],"stateMutability":"nonpayable"},{"type":"function","name":"coordinator","inputs":[],"outputs":[{"name":"","type":"address","internalType":"address"}],"stateMutability":"view"},{"type":"function","name":"getKey","inputs":[{"name":"chainSrc","type":"uint256","internalType":"uint256"},{"name":"chainDest","type":"uint256","internalType":"uint256"},{"name":"receiver","type":"address","internalType":"address"},{"name":"sessionId","type":"uint256","internalType":"uint256"},{"name":"label","type":"bytes","internalType":"bytes"}],"outputs":[{"name":"key","type":"bytes32","internalType":"bytes32"}],"stateMutability":"pure"},{"type":"function","name":"inbox","inputs":[{"name":"key","type":"bytes32","internalType":"bytes32"}],"outputs":[{"name":"message","type":"bytes","internalType":"bytes"}],"stateMutability":"view"},{"type":"function","name":"keyListInbox","inputs":[{"name":"","type":"uint256","internalType":"uint256"}],"outputs":[{"name":"","type":"bytes32","internalType":"bytes32"}],"stateMutability":"view"},{"type":"function","name":"keyListOutbox","inputs":[{"name":"","type":"uint256","internalType":"uint256"}],"outputs":[{"name":"","type":"bytes32","internalType":"bytes32"}],"stateMutability":"view"},{"type":"function","name":"outbox","inputs":[{"name":"key","type":"bytes32","internalType":"bytes32"}],"outputs":[{"name":"message","type":"bytes","internalType":"bytes"}],"stateMutability":"view"},{"type":"function","name":"putInbox","inputs":[{"name":"chainSrc","type":"uint256","internalType":"uint256"},{"name":"chainDest","type":"uint256","internalType":"uint256"},{"name":"receiver","type":"address","internalType":"address"},{"name":"sessionId","type":"uint256","internalType":"uint256"},{"name":"data","type":"bytes","internalType":"bytes"},{"name":"label","type":"bytes","internalType":"bytes"}],"outputs":[],"stateMutability":"nonpayable"},{"type":"function","name":"read","inputs":[{"name":"chainSrc","type":"uint256","internalType":"uint256"},{"name":"chainDest","type":"uint256","internalType":"uint256"},{"name":"receiver","type":"address","internalType":"address"},{"name":"sessionId","type":"uint256","internalType":"uint256"},{"name":"label","type":"bytes","internalType":"bytes"}],"outputs":[{"name":"message","type":"bytes","internalType":"bytes"}],"stateMutability":"view"},{"type":"function","name":"write","inputs":[{"name":"chainSrc","type":"uint256","internalType":"uint256"},{"name":"chainDest","type":"uint256","internalType":"uint256"},{"name":"receiver","type":"address","internalType":"address"},{"name":"sessionId","type":"uint256","internalType":"uint256"},{"name":"data","type":"bytes","internalType":"bytes"},{"name":"label","type":"bytes","internalType":"bytes"}],"outputs":[],"stateMutability":"nonpayable"},{"type":"error","name":"InvalidCoordinator","inputs":[]}]`

type MailboxCall struct {
	ChainSrc  *big.Int
	ChainDest *big.Int
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
	Receiver      common.Address
	SessionID     *big.Int
	Label         []byte
	RequiredData  bool
	IsInboxRead   bool
	Data          []byte // Fulfilled by CIRCMessage
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
	// TODO: consider naming Revert and invert bool checks
	Success          bool // success when not revert()'ed
	Dependencies     []CrossRollupDependency
	OutboundMessages []CrossRollupMessage
	Tx               *types.Transaction
}

func (s SimulationState) RequiresCoordination() bool {
	return len(s.Dependencies) > 0 || len(s.OutboundMessages) > 0
}

type MailboxProcessor struct {
	chainID              uint64
	mailboxAddresses     []common.Address
	sequencerClients     map[string]transport.Client
	sequencerCoordinator sequencer.Coordinator
	backend              interface{}
	sequencerKey         *ecdsa.PrivateKey
	sequencerAddr        common.Address
}

func NewMailboxProcessor(chainID uint64, mailboxAddrs []common.Address, sequencerClients map[string]transport.Client, coordinator sequencer.Coordinator, sequencerKey *ecdsa.PrivateKey, sequencerAddr common.Address, backend *EthAPIBackend) *MailboxProcessor {
	return &MailboxProcessor{
		chainID:              chainID,
		mailboxAddresses:     mailboxAddrs,
		sequencerClients:     sequencerClients,
		sequencerCoordinator: coordinator,
		backend:              backend,
		sequencerKey:         sequencerKey,
		sequencerAddr:        sequencerAddr,
	}
}

func (mp *MailboxProcessor) AnalyzeTransaction(traceResult *ssv.SSVTraceResult, sentOutboundMsgs []CrossRollupMessage, fullFilledDeps []CrossRollupDependency, tx *types.Transaction) (*SimulationState, error) {
	txHashHex := tx.Hash().Hex()
	simState, err := mp.analyzeTransaction(traceResult, sentOutboundMsgs, fullFilledDeps, txHashHex)
	if err != nil {
		return nil, fmt.Errorf("failed to analyze transaction: %w", err)
	}

	simState.Tx = tx

	if !simState.RequiresCoordination() {
		log.Info("[SSV] Transaction requires no cross-rollup coordination", "txHash", txHashHex)
		return simState, nil
	}

	log.Info("[SSV] Transaction requires cross-rollup coordination",
		"txHash", txHashHex,
		"dependencies", len(simState.Dependencies),
		"outbound", len(simState.OutboundMessages))

	return simState, nil
}

func parseCallType(call *MailboxCall) string {
	if call.IsRead {
		return "read"
	}
	if call.IsWrite {
		return "write"
	}

	return "unknown"
}

func (mp *MailboxProcessor) analyzeTransaction(traceResult *ssv.SSVTraceResult, sentOutboundMsgs []CrossRollupMessage, fullfilledDeps []CrossRollupDependency, txHashHex string) (*SimulationState, error) {
	simState := &SimulationState{
		Success:          traceResult.ExecutionResult.Err == nil,
		Dependencies:     make([]CrossRollupDependency, 0),
		OutboundMessages: make([]CrossRollupMessage, 0),
	}

	log.Info("[SSV] Analyzing transaction trace",
		"txHash", txHashHex,
		"success", simState.Success,
		"operations", len(traceResult.Operations))

	if traceResult.ExecutionResult.Err != nil {
		log.Warn("[SSV] Cross-chain transaction reverted during simulation",
			"txHash", txHashHex,
			"error", traceResult.ExecutionResult.Err,
			"revert", traceResult.ExecutionResult.Revert(),
			"continuing_analysis", true)
	}

	for i, op := range traceResult.Operations {
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

			log.Info(fmt.Sprintf("[SSV] Parsed mailbox %s call", parseCallType(call)),
				"chainSrc", call.ChainSrc,
				"chainDest", call.ChainDest,
				"receiver", call.Receiver.Hex(),
				"sessionId", call.SessionId)

			// mailbox.read(...)
			if call.IsRead {
				// If we're reading (chainDest  == chainID) this is a dependency
				if awaitRead(call, mp.chainID) {
					dep := CrossRollupDependency{
						SourceChainID: call.ChainSrc.Uint64(),
						DestChainID:   call.ChainDest.Uint64(),
						Receiver:      call.Receiver,
						SessionID:     call.SessionId,
						Label:         call.Label,
						RequiredData:  true,
						IsInboxRead:   true,
					}

					if !containsDependency(fullfilledDeps, dep) {
						simState.Dependencies = append(simState.Dependencies, dep)

						log.Info("[SSV] Detected new mailbox read call",
							"chainSrc", dep.SourceChainID,
							"chainDest", dep.DestChainID,
							"receiver", dep.Receiver.Hex(),
							"sessionId", dep.SessionID)
					} else {
						log.Info("[SSV] Ignore mailbox read call: already fulfilled",
							"chainSrc", call.ChainSrc.Uint64(),
							"chainDest", call.ChainDest.Uint64(),
							"localChain", mp.chainID)
					}
				} else {
					log.Info("[SSV] Ignore mailbox read call: chainDest is another chain",
						"chainSrc", call.ChainSrc.Uint64(),
						"chainDest", call.ChainDest.Uint64(),
						"localChain", mp.chainID)
				}
			}

			// mailbox.write(...)
			if call.IsWrite {
				// If we're writing (chainSrc == chainID) this is an outbound message
				if mustWrite(call, mp.chainID) {
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

					if !alreadySent(sentOutboundMsgs, msg) {
						simState.OutboundMessages = append(simState.OutboundMessages, msg)

						log.Info("[SSV] Detected new mailbox write call",
							"chainSrc", msg.SourceChainID,
							"chainDest", msg.DestChainID,
							"sender", msg.Sender.Hex(),
							"receiver", msg.Receiver.Hex(),
							"sessionId", msg.SessionID,
							"dataLen", len(msg.Data))
					} else {
						log.Info("[SSV] Ignore mailbox write call: already sent",
							"chainSrc", call.ChainSrc.Uint64(),
							"chainDest", call.ChainDest.Uint64(),
							"localChain", mp.chainID)
					}
				} else {
					log.Info("[SSV] Ignore mailbox write call: chainSrc is another chain",
						"chainSrc", call.ChainSrc.Uint64(),
						"chainDest", call.ChainDest.Uint64(),
						"localChain", mp.chainID)
				}
			}
		} else if op.Type != vm.CALL && op.Type != vm.STATICCALL {
			log.Debug("[SSV] Ignoring non-CALL/STATICCALL operation to mailbox", "type", op.Type.String(), "address", op.Address.Hex())
		}
	}

	log.Info("[SSV] Transaction analysis complete",
		"txHash", txHashHex,
		"requiresCoordination", simState.RequiresCoordination(),
		"dependencies", len(simState.Dependencies),
		"outboundMessages", len(simState.OutboundMessages))

	// Log detailed dependency information
	for i, dep := range simState.Dependencies {
		log.Info("[SSV] Dependency details",
			"index", i,
			"chainSrc", dep.SourceChainID,
			"chainDest", dep.DestChainID,
			"sessionId", dep.SessionID,
			"label", string(dep.Label))
	}

	// Log detailed outbound message information
	for i, msg := range simState.OutboundMessages {
		log.Info("[SSV] Outbound message details",
			"index", i,
			"chainSrc", msg.SourceChainID,
			"chainDest", msg.DestChainID,
			"sessionId", msg.SessionID,
			"dataLen", len(msg.Data),
			"label", string(msg.Label))
	}

	return simState, nil
}

func alreadySent(msgs []CrossRollupMessage, msg CrossRollupMessage) bool {
	for _, m := range msgs {
		if m.SourceChainID == msg.SourceChainID &&
			m.DestChainID == msg.DestChainID &&
			m.Sender == msg.Sender &&
			m.Receiver == msg.Receiver &&
			m.SessionID.Cmp(msg.SessionID) == 0 &&
			bytes.Equal(m.Data, msg.Data) &&
			bytes.Equal(m.Label, msg.Label) &&
			m.MessageType == msg.MessageType &&
			m.IsOutboxWrite == msg.IsOutboxWrite {
			return true
		}
	}

	return false
}

func containsDependency(deps []CrossRollupDependency, dep CrossRollupDependency) bool {
	for _, d := range deps {
		if d.SourceChainID == dep.SourceChainID &&
			d.DestChainID == dep.DestChainID &&
			d.Receiver == dep.Receiver &&
			d.SessionID.Cmp(dep.SessionID) == 0 &&
			bytes.Equal(d.Label, dep.Label) &&
			d.RequiredData == dep.RequiredData &&
			d.IsInboxRead == dep.IsInboxRead {
			return true
		}
	}

	return false
}

// Check whether our sequencer must write
func mustWrite(call *MailboxCall, chainID uint64) bool {
	return call.ChainSrc.Uint64() == chainID && call.ChainDest.Uint64() != chainID
}

// Checker whether our sequencer should wait for other sequencer "mustWrite"
func awaitRead(call *MailboxCall, chainID uint64) bool {
	return call.ChainSrc.Uint64() != chainID && call.ChainDest.Uint64() == chainID
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
		Receiver:  values[2].(common.Address),
		SessionId: values[3].(*big.Int),
		Label:     values[4].([]byte),
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

func (mp *MailboxProcessor) handleCrossRollupCoordination(ctx context.Context, simState *SimulationState, xtID *rollupv1.XtID) ([]CrossRollupMessage, []CrossRollupDependency, error) {
	sentMsgs := make([]CrossRollupMessage, 0)
	// Send outbound CIRC messages
	for _, outMsg := range simState.OutboundMessages {
		log.Info("[SSV] Send CIRC message", "xtID", xtID.Hex(), "srcChain", outMsg.SourceChainID, "destChain", outMsg.DestChainID, "sessionId", outMsg.SessionID)
		if err := mp.sendCIRCMessage(ctx, &outMsg, xtID); err != nil {
			return nil, nil, fmt.Errorf("failed to send CIRC message: %w", err)
		}

		sentMsgs = append(sentMsgs, outMsg)
	}

	circDeps := make([]CrossRollupDependency, 0)
	// Wait for required CIRC messages and create putInbox transactions
	for _, dep := range simState.Dependencies {
		log.Info("[SSV] Await for CIRC message", "srcChain", dep.SourceChainID, "destChain", dep.DestChainID, "sessionId", dep.SessionID)

		sourceKey := spconsensus.ChainKeyUint64(dep.SourceChainID)
		circMsg, err := mp.waitForCIRCMessage(ctx, xtID, sourceKey)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to wait for CIRC message: %w", err)
		}

		// Populate dependency with authoritative fields from the CIRC message.
		// The Mailbox key uses (chainSrc, chainId, sender, receiver, sessionId, label),
		// where 'sender' in the outbox is msg.sender of the contract that performed write(...).
		// Ensure we mirror exactly what the source chain wrote so putInbox matches read(...).
		if len(circMsg.Receiver) > 0 {
			dep.Receiver = common.BytesToAddress(circMsg.Receiver[0])
		}
		dep.Data = circMsg.Data[0]
		circDeps = append(circDeps, dep)
	}

	log.Info("[SSV] Cross-rollup coordination completed", "xtID", xtID.Hex(), "sent", len(sentMsgs), "received", len(circDeps))
	return sentMsgs, circDeps, nil
}

func (mp *MailboxProcessor) sendCIRCMessage(ctx context.Context, msg *CrossRollupMessage, xtID *rollupv1.XtID) error {
	// Build CIRC payload
	circMsg := &rollupv1.CIRCMessage{
		SourceChain:      new(big.Int).SetUint64(msg.SourceChainID).Bytes(),
		DestinationChain: new(big.Int).SetUint64(msg.DestChainID).Bytes(),
		Source:           [][]byte{msg.Sender.Bytes()},
		Receiver:         [][]byte{msg.Receiver.Bytes()},
		XtId:             xtID,
		Label:            string(msg.Label),
		Data:             [][]byte{msg.Data},
	}

	spMsg := &rollupv1.Message{
		SenderId: strconv.FormatUint(mp.chainID, 10),
		Payload: &rollupv1.Message_CircMessage{
			CircMessage: circMsg,
		},
	}

	backend, ok := mp.backend.(*EthAPIBackend)
	if !ok {
		return fmt.Errorf("backend not available")
	}

	destChainID := spconsensus.ChainKeyUint64(msg.DestChainID)
	sequencerClient := backend.sequencerClients[destChainID]
	if sequencerClient == nil {
		return fmt.Errorf("no client for destination chain %s", destChainID)
	}
	if err := sequencerClient.Send(ctx, spMsg); err != nil {
		return err
	}
	log.Info("[SSV] CIRC message sent to peer", "xtID", xtID.Hex(), "destChainID", destChainID)
	return nil
}

func (mp *MailboxProcessor) waitForCIRCMessage(ctx context.Context, xtID *rollupv1.XtID, sourceChainID string) (*rollupv1.CIRCMessage, error) {
	// Wait for CIRC message with timeout
	timeout := time.NewTimer(2 * time.Minute)
	defer timeout.Stop()

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	ticks := 0

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timeout.C:
			return nil, fmt.Errorf("timeout waiting for CIRC message from chain %s", sourceChainID)
		case <-ticker.C:
			backend := mp.backend.(*EthAPIBackend)
			circMsg, err := backend.coordinator.Consensus().ConsumeCIRCMessage(xtID, sourceChainID)
			if err != nil {
				// Periodic info to confirm we're still waiting
				ticks++
				if ticks%20 == 0 { // ~2s interval
					log.Info("[SSV] Still waiting for CIRC message", "xtID", xtID.Hex(), "from", sourceChainID, "err", err.Error())
				}
				continue // Keep waiting
			}

			log.Info("[SSV] Consumed CIRC message",
				"from", sourceChainID,
				"dataLen", len(circMsg.Data[0]),
			)

			return circMsg, nil
		}
	}
}

func (mp *MailboxProcessor) getCoordinatorAddress(ctx context.Context, addr common.Address) (common.Address, error) {
	parsedABI, err := abi.JSON(strings.NewReader(mailboxABI))
	if err != nil {
		return common.Address{}, err
	}

	// The Mailbox ABI exposes the coordinator under the "coordinator" view
	callData, err := parsedABI.Pack("coordinator")
	if err != nil {
		return common.Address{}, err
	}

	// Build a call transaction
	data := hexutil.Bytes(callData)
	args := ethapi.TransactionArgs{
		To:   &addr,
		Data: &data,
	}
	latest := rpc.BlockNumberOrHashWithNumber(rpc.LatestBlockNumber)
	res, err := ethapi.DoCall(ctx, mp.backend.(*EthAPIBackend), args, latest, nil, nil, mp.backend.(*EthAPIBackend).RPCEVMTimeout(), mp.backend.(*EthAPIBackend).RPCGasCap())
	if err != nil {
		return common.Address{}, err
	}
	if len(res.Return()) != 32 && len(res.Return()) != 20 {
		return common.Address{}, fmt.Errorf("unexpected return size: %d", len(res.Return()))
	}
	// The ABI-encoded return for address is 32 bytes right-padded, extract last 20
	out := res.Return()
	if len(out) == 32 {
		out = out[12:]
	}
	return common.BytesToAddress(out), nil
}

func (mp *MailboxProcessor) createPutInboxTx(dep CrossRollupDependency, nonce uint64) (*types.Transaction, error) {
	parsedABI, err := abi.JSON(strings.NewReader(mailboxABI))
	if err != nil {
		return nil, err
	}

	callData, err := parsedABI.Pack("putInbox",
		new(big.Int).SetUint64(dep.SourceChainID),
		new(big.Int).SetUint64(dep.DestChainID),
		dep.Receiver,
		dep.SessionID,
		dep.Data,
		dep.Label,
	)
	if err != nil {
		return nil, err
	}

	var mailboxAddr common.Address
	switch mp.chainID {
	case native.RollupAChainID:
		mailboxAddr = mp.mailboxAddresses[0]
	case native.RollupBChainID:
		mailboxAddr = mp.mailboxAddresses[1]
	default:
		return nil, fmt.Errorf("unable to select mailbox addr. Unsupported \"%d\"chain id", mp.chainID)
	}

	log.Info("[SSV] Created putInbox transaction",
		"nonce", nonce,
		"mailbox", mailboxAddr.Hex(),
		"sourceChain", dep.SourceChainID,
		"destChain", dep.DestChainID,
		"sessionId", dep.SessionID,
		"data", hex.EncodeToString(dep.Data),
		"dataLen", len(callData),
		"receiver", dep.Receiver,
		"label", dep.Label,
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

	// TODO: unmarshalling JSON might be cpu intensive, remove later
	mp.traceTransaction(callData, mailboxAddr)

	tx := types.NewTx(txData)
	signedTx, err := types.SignTx(tx, types.NewLondonSigner(new(big.Int).SetUint64(mp.chainID)), mp.sequencerKey)
	if err != nil {
		return nil, fmt.Errorf("failed to sign tx %v", err)
	}

	return signedTx, nil
}

func (mp *MailboxProcessor) traceTransaction(callData []byte, mailboxAddr common.Address) {
	api := tracers.NewAPI(mp.backend.(tracers.Backend))
	res, err := api.TraceCall(context.Background(), ethapi.TransactionArgs{
		Data:  (*hexutil.Bytes)(&callData),
		From:  &mp.sequencerAddr,
		To:    &mailboxAddr,
		Value: (*hexutil.Big)(big.NewInt(0))},
		rpc.BlockNumberOrHashWithNumber(rpc.LatestBlockNumber),
		nil,
	)
	if err != nil {
		log.Warn("putInbox tx tracing failed", "calldata", string(callData), "err", err)
		return
	}

	var traceResult struct {
		Failed      bool   `json:"failed"`
		ReturnValue string `json:"returnValue"`
	}

	if err := json.Unmarshal(res.(json.RawMessage), &traceResult); err != nil {
		log.Warn("unable to parse putInbox tx trace result", "error", err)
	} else {
		if traceResult.Failed {
			log.Warn("putInbox tx fails during trace", "calldata", hexutil.Encode(callData), "returnValue", traceResult.ReturnValue, "res", string(res.(json.RawMessage)))
		}
	}
}

func (mp *MailboxProcessor) isMailboxAddress(addr common.Address) bool {
	for _, mailboxAddr := range mp.mailboxAddresses {
		if addr == mailboxAddr {
			return true
		}
	}
	return false
}

// reSimulateForACKMessages re-simulates the transaction after putInbox creation
func (mp *MailboxProcessor) reSimulateForACKMessages(ctx context.Context, tx *types.Transaction, xtID *rollupv1.XtID, alreadySentMsgs []CrossRollupMessage) ([]CrossRollupMessage, error) {
	backend, ok := mp.backend.(*EthAPIBackend)
	if !ok {
		return nil, fmt.Errorf("backend not available for re-simulation")
	}

	log.Debug("[SSV] Re-simulating transaction for ACK detection", "txHash", tx.Hash().Hex(), "xtID", xtID.Hex())

	// Re-simulate the transaction against pending state (which now includes putInbox transactions)
	blockNrOrHash := rpc.BlockNumberOrHashWithNumber(rpc.PendingBlockNumber)
	traceResult, err := backend.SimulateTransaction(ctx, tx, blockNrOrHash)
	if err != nil {
		return nil, fmt.Errorf("failed to re-simulate transaction: %w", err)
	}

	// Analyze the re-simulation result for new outbound messages
	emptyDeps := make([]CrossRollupDependency, 0) // No dependencies needed for re-simulation
	simState, err := mp.analyzeTransaction(traceResult, alreadySentMsgs, emptyDeps, tx.Hash().Hex())
	if err != nil {
		return nil, fmt.Errorf("failed to analyze re-simulation: %w", err)
	}

	newOutboundMsgs := make([]CrossRollupMessage, 0)

	// Send any new outbound messages detected in re-simulation
	for _, outMsg := range simState.OutboundMessages {
		log.Info("[SSV] Detected new ACK message in re-simulation",
			"xtID", xtID.Hex(),
			"srcChain", outMsg.SourceChainID,
			"destChain", outMsg.DestChainID,
			"sessionId", outMsg.SessionID,
			"label", string(outMsg.Label))

		if err := mp.sendCIRCMessage(ctx, &outMsg, xtID); err != nil {
			log.Error("[SSV] Failed to send ACK CIRC message", "error", err, "xtID", xtID.Hex())
			return nil, fmt.Errorf("failed to send ACK CIRC message: %w", err)
		}

		newOutboundMsgs = append(newOutboundMsgs, outMsg)
	}

	if len(newOutboundMsgs) > 0 {
		log.Info("[SSV] Successfully sent ACK CIRC messages", "xtID", xtID.Hex(), "count", len(newOutboundMsgs))
	}

	return newOutboundMsgs, nil
}
