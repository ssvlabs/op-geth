package sequencer

import (
	"crypto/sha256"
	"fmt"
	"sync"
	"time"

	"github.com/rs/zerolog"
	pb "github.com/ssvlabs/rollup-shared-publisher/proto/rollup/v1"
)

type DraftBlock struct {
	slot        uint64
	blockNumber uint64
	parentHash  []byte
	localTxs    [][]byte
	// xtID -> list of transaction data for this chain
	scpTxs       map[string][][]byte
	circMessages []*pb.CIRCMessage
	includedXTs  [][]byte
	timestamp    time.Time
}

type BlockBuilder struct {
	mu      sync.RWMutex
	chainID []byte
	log     zerolog.Logger

	// Current draft
	draft *DraftBlock
}

func NewBlockBuilder(chainID []byte, log zerolog.Logger) *BlockBuilder {
	return &BlockBuilder{
		chainID: chainID,
		log:     log.With().Str("component", "block_builder").Logger(),
	}
}

func (bb *BlockBuilder) StartSlot(slot uint64, request *pb.L2BlockRequest) error {
	bb.mu.Lock()
	defer bb.mu.Unlock()

	bb.log.Info().
		Uint64("slot", slot).
		Uint64("block_number", request.BlockNumber).
		Str("parent_hash", fmt.Sprintf("%x", request.ParentHash)).
		Msg("Starting block for slot")

	bb.draft = &DraftBlock{
		slot:         slot,
		blockNumber:  request.BlockNumber,
		parentHash:   request.ParentHash,
		localTxs:     make([][]byte, 0),
		scpTxs:       make(map[string][][]byte),
		circMessages: make([]*pb.CIRCMessage, 0),
		includedXTs:  make([][]byte, 0),
		timestamp:    time.Now(),
	}

	// Add top-of-block transactions
	bb.addTopOfBlockTxs()

	return nil
}

func (bb *BlockBuilder) AddLocalTransaction(tx []byte) error {
	bb.mu.Lock()
	defer bb.mu.Unlock()

	if bb.draft == nil {
		return fmt.Errorf("no active draft block")
	}

	bb.draft.localTxs = append(bb.draft.localTxs, tx)
	bb.log.Debug().Int("tx_size", len(tx)).Msg("Added local transaction")
	return nil
}

// AddSCPTransactions adds or removes SCP-related transaction(s) for a given xtID
// If decision is true, provided txs are appended; if false, any existing entries are removed.
func (bb *BlockBuilder) AddSCPTransactions(xtID string, txs [][]byte, decision bool) error {
	bb.mu.Lock()
	defer bb.mu.Unlock()

	if bb.draft == nil {
		return fmt.Errorf("no active draft block")
	}

	if decision {
		if len(txs) > 0 {
			bb.draft.scpTxs[xtID] = append(bb.draft.scpTxs[xtID], txs...)
		}
		bb.log.Info().Str("xt_id", xtID).Int("txs", len(txs)).Msg("Added SCP transactions (commit)")
	} else {
		delete(bb.draft.scpTxs, xtID)
		bb.log.Info().Str("xt_id", xtID).Msg("Removed SCP transaction (abort)")
	}

	return nil
}

func (bb *BlockBuilder) AddCIRCMessage(msg *pb.CIRCMessage) error {
	bb.mu.Lock()
	defer bb.mu.Unlock()

	if bb.draft == nil {
		return fmt.Errorf("no active draft block")
	}

	bb.draft.circMessages = append(bb.draft.circMessages, msg)
	bb.log.Debug().
		Str("xt_id", msg.XtId.Hex()).
		Str("label", msg.Label).
		Msg("Added CIRC message to draft")

	return nil
}

func (bb *BlockBuilder) SealBlock(includedXTs [][]byte) (*pb.L2Block, error) {
	bb.mu.Lock()
	defer bb.mu.Unlock()

	if bb.draft == nil {
		return nil, fmt.Errorf("no active draft block")
	}

	bb.draft.includedXTs = includedXTs

	// Build final transaction list
	allTxs := make([][]byte, 0)

	// 1. Top-of-block transactions (already added)
	// 2. CIRC population transactions
	circTxs := bb.buildCIRCPopulationTxs()
	allTxs = append(allTxs, circTxs...)

	// 3. SCP transactions (only included ones)
	for _, xtID := range includedXTs {
		xtIDStr := fmt.Sprintf("%x", xtID)
		if txs, exists := bb.draft.scpTxs[xtIDStr]; exists {
			allTxs = append(allTxs, txs...)
		}
	}

	// 4. Local transactions
	allTxs = append(allTxs, bb.draft.localTxs...)

	// Build L2 block
	blockData := bb.encodeBlockData(allTxs)
	blockHash := bb.calculateBlockHash(blockData)

	l2Block := &pb.L2Block{
		Slot:            bb.draft.slot,
		ChainId:         bb.chainID,
		BlockNumber:     bb.draft.blockNumber,
		BlockHash:       blockHash,
		ParentBlockHash: bb.draft.parentHash,
		IncludedXts:     includedXTs,
		Block:           blockData,
	}

	bb.log.Info().
		Uint64("slot", l2Block.Slot).
		Uint64("block_number", l2Block.BlockNumber).
		Int("total_txs", len(allTxs)).
		Int("included_xts", len(includedXTs)).
		Msg("Sealed L2 block")

	return l2Block, nil
}

func (bb *BlockBuilder) addTopOfBlockTxs() {
	// Add Mailbox.clean() and other system transactions
	cleanTx := bb.buildMailboxCleanTx()
	bb.draft.localTxs = append(bb.draft.localTxs, cleanTx)

	bb.log.Debug().Msg("Added top-of-block transactions")
}

func (bb *BlockBuilder) buildCIRCPopulationTxs() [][]byte {
	txs := make([][]byte, 0, len(bb.draft.circMessages))

	for _, msg := range bb.draft.circMessages {
		// Build transaction to populate mailbox with CIRC message
		populateTx := bb.buildMailboxPopulateTx(msg)
		txs = append(txs, populateTx)
	}

	bb.log.Debug().Int("circ_txs", len(txs)).Msg("Built CIRC population transactions")
	return txs
}

func (bb *BlockBuilder) buildMailboxCleanTx() []byte {
	// Mock implementation - build transaction to call Mailbox.clean()
	return []byte(fmt.Sprintf("MAILBOX_CLEAN_%d", time.Now().UnixNano()))
}

func (bb *BlockBuilder) buildMailboxPopulateTx(msg *pb.CIRCMessage) []byte {
	// Mock implementation - build transaction to populate mailbox
	return []byte(fmt.Sprintf("MAILBOX_POPULATE_%s_%s", msg.XtId.Hex(), msg.Label))
}

func (bb *BlockBuilder) encodeBlockData(txs [][]byte) []byte {
	// Mock implementation - encode transactions into block format
	data := make([]byte, 0)
	for i, tx := range txs {
		txHeader := fmt.Sprintf("TX_%d_LEN_%d:", i, len(tx))
		data = append(data, []byte(txHeader)...)
		data = append(data, tx...)
	}
	return data
}

func (bb *BlockBuilder) calculateBlockHash(blockData []byte) []byte {
	// Simple hash calculation
	hash := sha256.Sum256(blockData)
	return hash[:]
}

func (bb *BlockBuilder) GetDraftStats() map[string]interface{} {
	bb.mu.RLock()
	defer bb.mu.RUnlock()

	if bb.draft == nil {
		return map[string]interface{}{"active": false}
	}

	return map[string]interface{}{
		"active":        true,
		"slot":          bb.draft.slot,
		"block_number":  bb.draft.blockNumber,
		"local_txs":     len(bb.draft.localTxs),
		"scp_txs":       len(bb.draft.scpTxs),
		"circ_messages": len(bb.draft.circMessages),
		"age_seconds":   time.Since(bb.draft.timestamp).Seconds(),
	}
}

func (bb *BlockBuilder) Reset() {
	bb.mu.Lock()
	defer bb.mu.Unlock()

	bb.draft = nil
	bb.log.Debug().Msg("Block builder reset")
}
