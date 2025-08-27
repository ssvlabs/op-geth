package tx

import "time"

// Transaction captures basic tx submission info
type Transaction struct {
	Hash      []byte    `json:"hash"`
	Nonce     uint64    `json:"nonce"`
	GasPrice  uint64    `json:"gas_price"`
	GasLimit  uint64    `json:"gas_limit"`
	Data      []byte    `json:"data"`
	Timestamp time.Time `json:"timestamp"`
}

// TransactionStatus is a normalized receipt status for the publish tx
type TransactionStatus struct {
	Hash              []byte           `json:"hash"`
	Status            TransactionState `json:"status"`
	BlockNumber       uint64           `json:"block_number,omitempty"`
	BlockHash         []byte           `json:"block_hash,omitempty"`
	GasUsed           uint64           `json:"gas_used,omitempty"`
	ConfirmationCount int              `json:"confirmation_count"`
	Error             string           `json:"error,omitempty"`
}

type TransactionState string

const (
	TransactionStatePending   TransactionState = "pending"
	TransactionStateIncluded  TransactionState = "included"
	TransactionStateConfirmed TransactionState = "confirmed"
	TransactionStateFinalized TransactionState = "finalized"
	TransactionStateFailed    TransactionState = "failed"
)
