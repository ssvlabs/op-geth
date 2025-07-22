package ssv

import (
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/vm"
)


//go:generate go run github.com/fjl/gencodec -type SSVOperation -field-override ssvOperationMarshaling -out gen_ssv_json.go

type SSVOperation struct {
	Type         vm.OpCode      `json:"-"`       // Operation type (CALL, SLOAD, SSTORE, etc.)
	Address      common.Address `json:"address"` // Contract address
	CallData     []byte         `json:"callData,omitempty" rlp:"optional"`
	StorageKey   []byte         `json:"storageKey,omitempty" rlp:"optional"`
	StorageValue []byte         `json:"storageValue,omitempty" rlp:"optional"`
	From         common.Address `json:"from"` // Caller address
	Gas          uint64         `json:"gas"`  // Gas available
}

func (op SSVOperation) TypeString() string {
	return op.Type.String()
}

type SSVTraceResult struct {
	Operations      []SSVOperation        `json:"operations"`
	ExecutionResult *core.ExecutionResult `json:"-"`
}


// ssvOperationMarshaling provides field overrides for JSON marshaling
type ssvOperationMarshaling struct {
	TypeString   string `json:"type"`
	CallData     hexutil.Bytes
	StorageKey   hexutil.Bytes
	StorageValue hexutil.Bytes
	Gas          hexutil.Uint64
}