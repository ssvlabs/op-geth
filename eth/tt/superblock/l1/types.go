package l1

import "time"

type BlockInfo struct {
	Number     uint64    `json:"number"`
	Hash       []byte    `json:"hash"`
	ParentHash []byte    `json:"parent_hash"`
	Timestamp  time.Time `json:"timestamp"`
	GasLimit   uint64    `json:"gas_limit"`
	GasUsed    uint64    `json:"gas_used"`
}
