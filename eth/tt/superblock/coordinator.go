package superblock

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog"
	pb "github.com/ssvlabs/rollup-shared-publisher/proto/rollup/v1"
	"github.com/ssvlabs/rollup-shared-publisher/x/consensus"
	"github.com/ssvlabs/rollup-shared-publisher/x/superblock/l1"
	l1events "github.com/ssvlabs/rollup-shared-publisher/x/superblock/l1/events"
	"github.com/ssvlabs/rollup-shared-publisher/x/superblock/l1/tx"
	"github.com/ssvlabs/rollup-shared-publisher/x/superblock/queue"
	"github.com/ssvlabs/rollup-shared-publisher/x/superblock/registry"
	"github.com/ssvlabs/rollup-shared-publisher/x/superblock/slot"
	"github.com/ssvlabs/rollup-shared-publisher/x/superblock/store"
	"github.com/ssvlabs/rollup-shared-publisher/x/superblock/wal"
	"github.com/ssvlabs/rollup-shared-publisher/x/transport"
)

// Coordinator orchestrates the Superblock Construction Protocol (SBCP)
// by managing slot-based execution, cross-chain transactions, and L2 block assembly
type Coordinator struct {
	mu      sync.RWMutex
	config  Config
	log     zerolog.Logger
	metrics prometheus.Registerer

	slotManager     SlotManager
	stateMachine    *slot.StateMachine
	registryService registry.Service
	l2BlockStore    store.L2BlockStore
	superblockStore store.SuperblockStore
	xtQueue         queue.XTRequestQueue
	l1Publisher     l1.Publisher
	walManager      wal.Manager

	consensusCoord consensus.Coordinator
	transport      transport.Server

	running  bool
	stopCh   chan struct{}
	workerWg sync.WaitGroup

	currentExecution *SlotExecution
	stats            map[string]interface{}

	l1TrackMu sync.Mutex
	l1Tracked map[uint64][]byte // superblock number -> tx hash
}

func NewCoordinator(
	config Config,
	log zerolog.Logger,
	metrics prometheus.Registerer,
	registryService registry.Service,
	l2BlockStore store.L2BlockStore,
	superblockStore store.SuperblockStore,
	xtQueue queue.XTRequestQueue,
	l1Publisher l1.Publisher,
	walManager wal.Manager,
	consensusCoord consensus.Coordinator,
	transport transport.Server,
) *Coordinator {
	slotManagerImpl := slot.NewManager(
		config.Slot.GenesisTime,
		config.Slot.Duration,
		config.Slot.SealCutover,
	)

	stateMachine := slot.NewStateMachine(slotManagerImpl, log)

	c := &Coordinator{
		config:          config,
		log:             log.With().Str("component", "coordinator").Logger(),
		metrics:         metrics,
		slotManager:     slotManagerImpl,
		stateMachine:    stateMachine,
		registryService: registryService,
		l2BlockStore:    l2BlockStore,
		superblockStore: superblockStore,
		xtQueue:         xtQueue,
		l1Publisher:     l1Publisher,
		walManager:      walManager,
		consensusCoord:  consensusCoord,
		transport:       transport,
		stopCh:          make(chan struct{}),
		stats:           make(map[string]interface{}),
		l1Tracked:       make(map[uint64][]byte),
	}

	c.setupStateCallbacks()
	c.setupConsensusCallbacks()
	return c
}

func (c *Coordinator) Start(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.running {
		return fmt.Errorf("coordinator already running")
	}

	c.log.Info().Msg("Starting superblock coordinator")

	if c.walManager != nil {
		if err := c.recoverFromWAL(ctx); err != nil {
			return fmt.Errorf("WAL recovery failed: %w", err)
		}
	}

	c.running = true

	c.workerWg.Add(5)
	go c.slotExecutionLoop(ctx)
	go c.queueProcessor(ctx)
	go c.metricsUpdater(ctx)
	go c.l1EventWatcher(ctx)
	go c.l1ReceiptPoller(ctx)

	return nil
}

func (c *Coordinator) Stop(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.running {
		return nil
	}

	c.log.Info().Msg("Stopping superblock coordinator")
	c.running = false

	close(c.stopCh)
	c.workerWg.Wait()

	if c.walManager != nil {
		return c.walManager.Close()
	}

	return nil
}

func (c *Coordinator) GetCurrentSlot() uint64 {
	return c.slotManager.GetCurrentSlot()
}

func (c *Coordinator) GetSlotState() SlotState {
	state := c.stateMachine.GetCurrentState()
	switch state {
	case slot.StateStarting:
		return SlotStateStarting
	case slot.StateFree:
		return SlotStateFree
	case slot.StateLocked:
		return SlotStateLocked
	case slot.StateSealing:
		return SlotStateSealing
	default:
		return SlotStateStarting
	}
}

// Logger exposes the coordinator's logger for external packages (e.g., handlers).
func (c *Coordinator) Logger() *zerolog.Logger { return &c.log }

// Consensus returns the underlying consensus coordinator.
func (c *Coordinator) Consensus() consensus.Coordinator { return c.consensusCoord }

// HandleL2Block is an exported wrapper to process incoming L2 block messages.
func (c *Coordinator) HandleL2Block(ctx context.Context, from string, l2Block *pb.L2Block) error {
	return c.handleL2Block(ctx, from, l2Block)
}

// StateMachine exposes the slot state machine for external observers.
func (c *Coordinator) StateMachine() *slot.StateMachine { return c.stateMachine }

// Transport exposes the transport server for broadcasting messages from adapters.
func (c *Coordinator) Transport() transport.Server { return c.transport }

// StartSCPForAdapter allows adapter layer to initiate SCP with a constructed request.
func (c *Coordinator) StartSCPForAdapter(ctx context.Context, xtReq *pb.XTRequest, xtID []byte, from string) error {
	queuedRequest := &queue.QueuedXTRequest{
		Request:     xtReq,
		XtID:        xtID,
		Priority:    time.Now().Unix(),
		SubmittedAt: time.Now(),
		ExpiresAt:   time.Now().Add(c.config.Queue.RequestExpiration),
		Attempts:    0,
		From:        from,
	}
	return c.startSCP(ctx, queuedRequest)
}

func (c *Coordinator) GetActiveTransactions() []*pb.XtID {
	instances := c.stateMachine.GetSCPInstances()
	var activeXTs []*pb.XtID

	for _, instance := range instances {
		if instance.Decision == nil {
			activeXTs = append(activeXTs, &pb.XtID{
				Hash: instance.XtID,
			})
		}
	}

	return activeXTs
}

func (c *Coordinator) SubmitXTRequest(ctx context.Context, from string, request *pb.XTRequest) error {
	queuedRequest := &queue.QueuedXTRequest{
		Request:     request,
		XtID:        c.calculateXtID(request),
		Priority:    time.Now().Unix(),
		SubmittedAt: time.Now(),
		ExpiresAt:   time.Now().Add(c.config.Queue.RequestExpiration),
		Attempts:    0,
		From:        from,
	}

	return c.xtQueue.Enqueue(ctx, queuedRequest)
}

func (c *Coordinator) GetStats() map[string]interface{} {
	c.mu.RLock()
	defer c.mu.RUnlock()

	result := make(map[string]interface{})
	for k, v := range c.stats {
		result[k] = v
	}

	result["current_slot"] = c.GetCurrentSlot()
	result["slot_state"] = c.GetSlotState().String()
	result["running"] = c.running

	return result
}

func (c *Coordinator) slotExecutionLoop(ctx context.Context) {
	defer c.workerWg.Done()

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-c.stopCh:
			return
		case <-ticker.C:
			if err := c.processSlotTick(ctx); err != nil {
				c.log.Error().Err(err).Msg("Error processing slot tick")
			}
		}
	}
}

func (c *Coordinator) processSlotTick(ctx context.Context) error {
	currentSlot := c.slotManager.GetCurrentSlot()
	currentState := c.stateMachine.GetCurrentState()

	switch currentState {
	case slot.StateStarting:
		return c.handleStartingState(ctx, currentSlot)
	case slot.StateFree:
		return c.handleFreeState(ctx, currentSlot)
	case slot.StateLocked:
		return c.handleLockedState(ctx, currentSlot)
	case slot.StateSealing:
		return c.handleSealingState(ctx, currentSlot)
	}

	return nil
}

func (c *Coordinator) handleStartingState(ctx context.Context, currentSlot uint64) error {
	if currentSlot <= c.stateMachine.GetCurrentSlot() {
		return nil
	}

	activeRollups, err := c.registryService.GetActiveRollups(ctx)
	if err != nil {
		return fmt.Errorf("failed to get active rollups: %w", err)
	}

	lastSuperblock, err := c.superblockStore.GetLatestSuperblock(ctx)
	var nextNumber uint64 = 1
	var lastHash = make([]byte, 32)

	if err == nil && lastSuperblock != nil {
		nextNumber = lastSuperblock.Number + 1
		lastHash = lastSuperblock.Hash
	}

	if err := c.stateMachine.BeginSlot(currentSlot, nextNumber, lastHash, activeRollups); err != nil {
		return fmt.Errorf("failed to begin slot: %w", err)
	}

	// Initialize current execution context
	c.currentExecution = &SlotExecution{
		Slot:                 currentSlot,
		State:                SlotStateFree,
		StartTime:            time.Now(),
		NextSuperblockNumber: nextNumber,
		LastSuperblockHash:   lastHash,
		ActiveRollups:        activeRollups,
		ReceivedL2Blocks:     make(map[string]*pb.L2Block),
		SCPInstances:         make(map[string]*slot.SCPInstance),
		L2BlockRequests:      make(map[string]*pb.L2BlockRequest),
		AttemptedRequests:    make(map[string]*queue.QueuedXTRequest),
	}

	c.sendStartSlotMessages(ctx, currentSlot, nextNumber, lastHash, activeRollups)

	return nil
}

func (c *Coordinator) handleFreeState(ctx context.Context, currentSlot uint64) error {
	if c.slotManager.IsSlotSealTime() {
		return c.requestSeal(ctx, currentSlot)
	}

	queuedRequest, err := c.xtQueue.Peek(ctx)
	if err != nil {
		return err
	}
	if queuedRequest == nil {
		return nil
	}

	if queuedRequest.ExpiresAt.Before(time.Now()) {
		c.xtQueue.Dequeue(ctx)
		c.log.Info().Str("xt_id", fmt.Sprintf("%x", queuedRequest.XtID)).Msg("Expired XT request removed")
		return nil
	}

	return c.startSCP(ctx, queuedRequest)
}

func (c *Coordinator) handleLockedState(ctx context.Context, currentSlot uint64) error {
	if c.slotManager.IsSlotSealTime() {
		return c.requestSeal(ctx, currentSlot)
	}

	return nil
}

func (c *Coordinator) handleSealingState(ctx context.Context, currentSlot uint64) error {
	if c.stateMachine.CheckAllL2BlocksReceived() {
		return c.buildSuperblock(ctx, currentSlot)
	}

	// Check for slot timeout
	stateMachineSlot := c.stateMachine.GetCurrentSlot()
	managerCurrentSlot := c.slotManager.GetCurrentSlot()

	if managerCurrentSlot > stateMachineSlot {
		return c.handleSlotTimeout(ctx, stateMachineSlot)
	}

	return nil
}

func (c *Coordinator) startSCP(ctx context.Context, queuedRequest *queue.QueuedXTRequest) error {
	c.xtQueue.Dequeue(ctx)

	instances := c.stateMachine.GetSCPInstances()
	sequenceNumber := uint64(len(instances))

	participatingChains := c.extractParticipatingChains(queuedRequest.Request)

	scpInstance := &slot.SCPInstance{
		XtID:                queuedRequest.XtID,
		Slot:                c.stateMachine.GetCurrentSlot(),
		SequenceNumber:      sequenceNumber,
		Request:             queuedRequest.Request,
		ParticipatingChains: participatingChains,
		Votes:               make(map[string]bool),
		StartTime:           time.Now(),
	}

	if err := c.stateMachine.StartSCP(scpInstance); err != nil {
		return fmt.Errorf("failed to start SCP: %w", err)
	}

	// Track attempted request for potential requeue on failure path
	c.currentExecution.AttemptedRequests[fmt.Sprintf("%x", queuedRequest.XtID)] = queuedRequest

	startSCMsg := &pb.Message{
		SenderId: "shared-publisher",
		Payload: &pb.Message_StartSc{
			StartSc: &pb.StartSC{
				Slot:             c.stateMachine.GetCurrentSlot(),
				XtSequenceNumber: sequenceNumber,
				XtRequest:        queuedRequest.Request,
				XtId:             queuedRequest.XtID,
			},
		},
	}

	if err := c.transport.Broadcast(ctx, startSCMsg, ""); err != nil {
		c.log.Error().Err(err).Msg("Failed to broadcast StartSC message")
	}

	c.sendStartSCMessages(scpInstance)

	c.log.Info().
		Str("xt_id", fmt.Sprintf("%x", scpInstance.XtID)).
		Uint64("sequence", sequenceNumber).
		Int("participating_chains", len(participatingChains)).
		Msg("Started SCP instance")

	return nil
}

func (c *Coordinator) requestSeal(ctx context.Context, currentSlot uint64) error {
	// At seal cutover: abort any in-flight (undecided) xTs and broadcast Decided(false)
	if err := c.forceAbortUndecided(ctx); err != nil {
		return fmt.Errorf("failed to force abort undecided: %w", err)
	}

	includedXTs := c.stateMachine.GetIncludedXTs()

	if err := c.stateMachine.RequestSeal(includedXTs); err != nil {
		return fmt.Errorf("failed to request seal: %w", err)
	}

	c.sendRequestSealMessages(ctx, currentSlot, includedXTs)

	return nil
}

func (c *Coordinator) buildSuperblock(ctx context.Context, slotNumber uint64) error {
	l2Blocks := c.stateMachine.GetReceivedL2Blocks()
	includedXTs := c.stateMachine.GetIncludedXTs()

	if !c.validateL2Blocks(l2Blocks) {
		return c.failSlot(slotNumber, "invalid L2 blocks")
	}

	superblock := &store.Superblock{
		Number:      c.currentExecution.NextSuperblockNumber,
		Slot:        slotNumber,
		ParentHash:  c.currentExecution.LastSuperblockHash,
		Timestamp:   time.Now(),
		L2Blocks:    make([]*pb.L2Block, 0, len(l2Blocks)),
		IncludedXTs: includedXTs,
		Status:      store.SuperblockStatusPending,
	}

	for _, block := range l2Blocks {
		superblock.L2Blocks = append(superblock.L2Blocks, block)
	}

	superblock.MerkleRoot = c.calculateMerkleRoot(superblock.L2Blocks)
	superblock.Hash = c.calculateSuperblockHash(superblock)

	if err := c.superblockStore.StoreSuperblock(ctx, superblock); err != nil {
		return fmt.Errorf("failed to store superblock: %w", err)
	}

	if tx, err := c.l1Publisher.PublishSuperblock(ctx, superblock); err != nil {
		c.log.Error().Err(err).Msg("Failed to publish superblock")
		superblock.Status = store.SuperblockStatusPending
	} else {
		superblock.Status = store.SuperblockStatusSubmitted
		superblock.L1TransactionHash = tx.Hash
		// persist updated fields (tx hash + status)
		if err := c.superblockStore.StoreSuperblock(ctx, superblock); err != nil {
			c.log.Warn().Err(err).Uint64("number", superblock.Number).Msg("Failed to update stored superblock post-publish")
		}
		// Track for receipt polling
		c.l1TrackMu.Lock()
		c.l1Tracked[superblock.Number] = append([]byte{}, tx.Hash...)
		c.l1TrackMu.Unlock()
	}

	c.log.Info().
		Uint64("number", superblock.Number).
		Uint64("slot", slotNumber).
		Int("l2_blocks", len(superblock.L2Blocks)).
		Int("included_xts", len(includedXTs)).
		Msg("Built superblock")

	return c.stateMachine.TransitionTo(slot.StateStarting, "superblock built")
}

// l1EventWatcher listens for L1 OutputProposed events and updates status/rollback.
func (c *Coordinator) l1EventWatcher(ctx context.Context) {
	defer c.workerWg.Done()
	if c.l1Publisher == nil {
		return
	}
	ch, err := c.l1Publisher.WatchSuperblocks(ctx)
	if err != nil {
		c.log.Warn().Err(err).Msg("L1 watcher unavailable")
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.stopCh:
			return
		case ev, ok := <-ch:
			if !ok || ev == nil {
				return
			}
			// Update store for submitted or rollback
			sb, err := c.superblockStore.GetSuperblock(ctx, ev.SuperblockNumber)
			if err != nil {
				// Not ours or not stored; ignore
				continue
			}
			if ev.Type == l1events.SuperblockEventRolledBack {
				sb.Status = store.SuperblockStatusRolledBack
				// Remove from tracking
				c.l1TrackMu.Lock()
				delete(c.l1Tracked, ev.SuperblockNumber)
				c.l1TrackMu.Unlock()
			} else if sb.Status == store.SuperblockStatusPending {
				// Ensure at least Submitted
				sb.Status = store.SuperblockStatusSubmitted
			}
			_ = c.superblockStore.StoreSuperblock(ctx, sb)
		}
	}
}

// l1ReceiptPoller periodically checks transaction receipts for tracked superblocks
// and advances their status to Confirmed/Finalized.
func (c *Coordinator) l1ReceiptPoller(ctx context.Context) {
	defer c.workerWg.Done()
	if c.l1Publisher == nil {
		return
	}
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.stopCh:
			return
		case <-ticker.C:
			c.l1TrackMu.Lock()
			for number, txHash := range c.l1Tracked {
				c.updateStatusFromReceipt(ctx, number, txHash)
			}
			c.l1TrackMu.Unlock()
		}
	}
}

func (c *Coordinator) updateStatusFromReceipt(ctx context.Context, number uint64, txHash []byte) {
	status, err := c.l1Publisher.GetPublishStatus(ctx, txHash)
	if err != nil || status == nil {
		return
	}
	sb, err := c.superblockStore.GetSuperblock(ctx, number)
	if err != nil || sb == nil {
		return
	}
	switch status.Status {
	case tx.TransactionStateFinalized:
		sb.Status = store.SuperblockStatusFinalized
		delete(c.l1Tracked, number)
	case tx.TransactionStateConfirmed:
		sb.Status = store.SuperblockStatusConfirmed
	case tx.TransactionStateIncluded:
		if sb.Status == store.SuperblockStatusPending {
			sb.Status = store.SuperblockStatusSubmitted
		}
	case tx.TransactionStateFailed:
		sb.Status = store.SuperblockStatusPending
		delete(c.l1Tracked, number)
	case tx.TransactionStatePending:
		// no-op
	}
	_ = c.superblockStore.StoreSuperblock(ctx, sb)
}

func (c *Coordinator) setupStateCallbacks() {
	c.stateMachine.RegisterStateChangeCallback(slot.StateFree, c.onStateFree)
	c.stateMachine.RegisterStateChangeCallback(slot.StateLocked, c.onStateLocked)
	c.stateMachine.RegisterStateChangeCallback(slot.StateSealing, c.onStateSealing)
}

func (c *Coordinator) setupConsensusCallbacks() {
	c.consensusCoord.SetStartCallback(c.handleConsensusStart)
	c.consensusCoord.SetVoteCallback(c.handleConsensusVote)
	c.consensusCoord.SetDecisionCallback(c.handleConsensusDecision)
}

func (c *Coordinator) onStateFree(from, to slot.State, slot uint64) {
}

func (c *Coordinator) onStateLocked(from, to slot.State, slot uint64) {
}

func (c *Coordinator) onStateSealing(from, to slot.State, slot uint64) {
}

func (c *Coordinator) handleConsensusStart(ctx context.Context, from string, xtReq *pb.XTRequest) error {
	c.log.Info().
		Str("from", from).
		Str("xt_id", fmt.Sprintf("%x", c.calculateXtID(xtReq))).
		Msg("Consensus start callback")
	return nil
}

func (c *Coordinator) handleConsensusVote(ctx context.Context, xtID *pb.XtID, vote bool) error {
	c.log.Info().Str("xt_id", xtID.Hex()).Bool("vote", vote).Msg("Broadcasting vote to sequencers")

	voteMsg := &pb.Message{
		SenderId: "shared-publisher",
		Payload: &pb.Message_Vote{
			Vote: &pb.Vote{
				SenderChainId: []byte("shared-publisher"),
				XtId:          xtID,
				Vote:          vote,
			},
		},
	}

	return c.transport.Broadcast(ctx, voteMsg, "")
}

func (c *Coordinator) handleConsensusDecision(ctx context.Context, xtID *pb.XtID, decision bool) error {
	c.log.Info().Str("xt_id", xtID.Hex()).Bool("decision", decision).Msg("Broadcasting decision to sequencers")

	decidedMsg := &pb.Message{
		SenderId: "shared-publisher",
		Payload: &pb.Message_Decided{
			Decided: &pb.Decided{
				XtId:     xtID,
				Decision: decision,
			},
		},
	}

	err := c.transport.Broadcast(ctx, decidedMsg, "")
	if err != nil {
		return err
	}

	return c.stateMachine.ProcessSCPDecision(xtID.Hash, decision)
}

// forceAbortUndecided marks undecided SCP instances as decided=false and broadcasts Decided(false)
func (c *Coordinator) forceAbortUndecided(ctx context.Context) error {
	instances := c.stateMachine.GetSCPInstances()
	var errs []error
	for _, inst := range instances {
		if inst.Decision == nil {
			// Broadcast Decided(false)
			decidedMsg := &pb.Message{
				SenderId: "shared-publisher",
				Payload: &pb.Message_Decided{
					Decided: &pb.Decided{XtId: &pb.XtID{Hash: inst.XtID}, Decision: false},
				},
			}
			if err := c.transport.Broadcast(ctx, decidedMsg, ""); err != nil {
				c.log.Error().
					Err(err).
					Str("xt_id", fmt.Sprintf("%x", inst.XtID)).
					Msg("Failed to broadcast forced abort decision")
				errs = append(errs, fmt.Errorf("broadcast forced abort %x: %w", inst.XtID, err))
			}

			// Update state machine
			if err := c.stateMachine.ProcessSCPDecision(inst.XtID, false); err != nil {
				c.log.Error().
					Err(err).
					Str("xt_id", fmt.Sprintf("%x", inst.XtID)).
					Msg("Failed to process forced abort decision")
				errs = append(errs, fmt.Errorf("update state forced abort %x: %w", inst.XtID, err))
			}
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

func (c *Coordinator) queueProcessor(ctx context.Context) {
	defer c.workerWg.Done()

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-c.stopCh:
			return
		case <-ticker.C:
			if _, err := c.xtQueue.RemoveExpired(ctx); err != nil {
				c.log.Error().Err(err).Msg("Failed to remove expired requests")
			}
		}
	}
}

func (c *Coordinator) metricsUpdater(ctx context.Context) {
	defer c.workerWg.Done()

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-c.stopCh:
			return
		case <-ticker.C:
			c.updateMetrics(ctx)
		}
	}
}

func (c *Coordinator) updateMetrics(ctx context.Context) {
}

func (c *Coordinator) recoverFromWAL(ctx context.Context) error {
	return nil
}

func (c *Coordinator) sendStartSlotMessages(
	ctx context.Context,
	slot uint64,
	superblockNumber uint64,
	lastHash []byte,
	activeRollups [][]byte, //nolint:unparam // later
) {
	l2BlockRequests := c.stateMachine.GetL2BlockRequests()

	blockRequests := make([]*pb.L2BlockRequest, 0, len(l2BlockRequests))
	for _, req := range l2BlockRequests {
		blockRequests = append(blockRequests, req)
	}

	startSlotMsg := &pb.Message{
		SenderId: "shared-publisher",
		Payload: &pb.Message_StartSlot{
			StartSlot: &pb.StartSlot{
				Slot:                 slot,
				NextSuperblockNumber: superblockNumber,
				LastSuperblockHash:   lastHash,
				L2BlocksRequest:      blockRequests,
			},
		},
	}

	if err := c.transport.Broadcast(ctx, startSlotMsg, ""); err != nil {
		c.log.Error().Err(err).Msg("Failed to broadcast StartSlot message")
	}
}

func (c *Coordinator) sendStartSCMessages(instance *slot.SCPInstance) {
	if err := c.consensusCoord.StartTransaction("superblock-coordinator", instance.Request); err != nil {
		c.log.Error().Err(err).Str("xt_id", fmt.Sprintf("%x", instance.XtID)).Msg("Failed to start SCP transaction")
	}
}

func (c *Coordinator) sendRequestSealMessages(ctx context.Context, slot uint64, includedXTs [][]byte) {
	requestSealMsg := &pb.Message{
		SenderId: "shared-publisher",
		Payload: &pb.Message_RequestSeal{
			RequestSeal: &pb.RequestSeal{
				Slot:        slot,
				IncludedXts: includedXTs,
			},
		},
	}

	if err := c.transport.Broadcast(ctx, requestSealMsg, ""); err != nil {
		c.log.Error().Err(err).Msg("Failed to broadcast RequestSeal message")
	}
}

func (c *Coordinator) extractParticipatingChains(request *pb.XTRequest) [][]byte {
	chains := make([][]byte, 0, len(request.Transactions))
	for _, tx := range request.Transactions {
		chains = append(chains, tx.ChainId)
	}
	return chains
}

func (c *Coordinator) validateL2Blocks(blocks map[string]*pb.L2Block) bool {
	slot := c.stateMachine.GetCurrentSlot()
	reqs := c.stateMachine.GetL2BlockRequests()

	for chainIDStr, blk := range blocks {
		if blk.Slot != slot {
			c.log.Error().Uint64("expected_slot", slot).Uint64("got", blk.Slot).Msg("L2 block with wrong slot")
			return false
		}
		req, ok := reqs[chainIDStr]
		if !ok {
			c.log.Error().Str("chain", fmt.Sprintf("%x", blk.ChainId)).Msg("unexpected chain in L2 blocks")
			return false
		}
		if blk.BlockNumber != req.BlockNumber {
			c.log.Error().
				Uint64("expected", req.BlockNumber).
				Uint64("got", blk.BlockNumber).
				Msg("L2 block number mismatch")
			return false
		}
		if len(blk.ParentBlockHash) == 0 || len(req.ParentHash) == 0 ||
			string(blk.ParentBlockHash) != string(req.ParentHash) {
			c.log.Error().Msg("L2 parent hash does not match requested parent")
			return false
		}
	}

	return true
}

func (c *Coordinator) failSlot(slotNumber uint64, reason string) error {
	c.log.Error().Uint64("slot", slotNumber).Str("reason", reason).Msg("Slot failed")
	// Requeue attempted xTs for next slot
	if err := c.requeueAttemptedRequests(context.Background()); err != nil {
		c.log.Error().Err(err).Msg("Failed to requeue attempted requests")
	}
	return c.stateMachine.TransitionTo(slot.StateStarting, fmt.Sprintf("slot failed: %s", reason))
}

// requeueAttemptedRequests requeues all attempted xTs for the next slot
func (c *Coordinator) requeueAttemptedRequests(ctx context.Context) error {
	if c.currentExecution == nil || len(c.currentExecution.AttemptedRequests) == 0 {
		return nil
	}
	reqs := make([]*queue.QueuedXTRequest, 0, len(c.currentExecution.AttemptedRequests))
	for _, r := range c.currentExecution.AttemptedRequests {
		reqs = append(reqs, r)
	}
	// clear current map to avoid double requeue
	c.currentExecution.AttemptedRequests = make(map[string]*queue.QueuedXTRequest)
	return c.xtQueue.RequeueForSlot(ctx, reqs)
}

func (c *Coordinator) handleSlotTimeout(ctx context.Context, slotNumber uint64) error {
	c.log.Warn().Uint64("slot", slotNumber).Msg("Slot timeout")

	// attempt to build superblock with partial blocks
	return c.buildPartialSuperblock(ctx, slotNumber)
}

func (c *Coordinator) buildPartialSuperblock(ctx context.Context, slotNumber uint64) error {
	receivedL2Blocks := c.stateMachine.GetReceivedL2Blocks()
	scpInstances := c.stateMachine.GetSCPInstances()

	// check if we can build with partial blocks
	receivedChainIDs := make(map[string]bool)
	for chainID := range receivedL2Blocks {
		receivedChainIDs[chainID] = true
	}

	// Check if SCPs with decisions=1 have all their participating chains represented
	canBuildPartial := true
	for _, instance := range scpInstances {
		if instance.Decision != nil && *instance.Decision {
			// This SCP was included, check if all its chains sent blocks
			hasAtLeastOneChain := false
			for _, chainID := range instance.ParticipatingChains {
				if receivedChainIDs[string(chainID)] {
					hasAtLeastOneChain = true
					break
				}
			}
			if !hasAtLeastOneChain {
				canBuildPartial = false
				break
			}
		}
	}

	if canBuildPartial && c.validateL2Blocks(receivedL2Blocks) {
		c.log.Info().
			Uint64("slot", slotNumber).
			Int("received_blocks", len(receivedL2Blocks)).
			Int("total_rollups", len(c.stateMachine.GetActiveRollups())).
			Msg("Building partial superblock due to timeout")
		return c.buildSuperblock(ctx, slotNumber)
	} else {
		c.log.Warn().
			Uint64("slot", slotNumber).
			Int("received_blocks", len(receivedL2Blocks)).
			Bool("can_build_partial", canBuildPartial).
			Msg("Cannot build superblock, failing slot")
		return c.failSlot(slotNumber, "timeout with insufficient blocks")
	}
}

func (c *Coordinator) calculateXtID(request *pb.XTRequest) []byte {
	xtID, _ := request.XtID()
	return xtID.Hash
}

func (c *Coordinator) calculateMerkleRoot(blocks []*pb.L2Block) []byte {
	if len(blocks) == 0 {
		return make([]byte, 32)
	}
	// Deterministic leaf order: sort by chainID (lexicographic)
	sort.Slice(blocks, func(i, j int) bool { return string(blocks[i].ChainId) < string(blocks[j].ChainId) })

	// Compute leaf hashes = keccak256(chainID || blockHash || blockNumberBE)
	leaves := make([][]byte, len(blocks))
	for i, b := range blocks {
		buf := make([]byte, 0, len(b.ChainId)+len(b.BlockHash)+8)
		buf = append(buf, b.ChainId...)
		buf = append(buf, b.BlockHash...)
		num := make([]byte, 8)
		binary.BigEndian.PutUint64(num, b.BlockNumber)
		buf = append(buf, num...)
		h := crypto.Keccak256(buf)
		leaves[i] = h
	}

	// Build Merkle tree with keccak256(left||right), duplicate last when odd
	level := leaves
	for len(level) > 1 {
		var next [][]byte
		for i := 0; i < len(level); i += 2 {
			left := level[i]
			right := left
			if i+1 < len(level) {
				right = level[i+1]
			}
			combined := append(append([]byte{}, left...), right...)
			next = append(next, crypto.Keccak256(combined))
		}
		level = next
	}
	return level[0]
}

func (c *Coordinator) calculateSuperblockHash(superblock *store.Superblock) []byte {
	// Header fields: Number || Slot || ParentHash || MerkleRoot
	header := make([]byte, 0, 8+8+len(superblock.ParentHash)+len(superblock.MerkleRoot))
	nb := make([]byte, 8)
	sb := make([]byte, 8)
	binary.BigEndian.PutUint64(nb, superblock.Number)
	binary.BigEndian.PutUint64(sb, superblock.Slot)
	header = append(header, nb...)
	header = append(header, sb...)
	header = append(header, superblock.ParentHash...)
	header = append(header, superblock.MerkleRoot...)
	return crypto.Keccak256(header)
}

func (c *Coordinator) handleL2Block(ctx context.Context, from string, l2Block *pb.L2Block) error {
	c.log.Info().
		Str("from", from).
		Uint64("block_number", l2Block.BlockNumber).
		Str("chain_id", fmt.Sprintf("%x", l2Block.ChainId)).
		Msg("Received L2 block")

	if err := c.l2BlockStore.StoreL2Block(ctx, l2Block); err != nil {
		c.log.Error().Err(err).Msg("Failed to store L2 block")
		return err
	}

	return c.stateMachine.ReceiveL2Block(l2Block)
}
