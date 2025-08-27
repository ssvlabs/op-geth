package contracts

import (
	"context"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ssvlabs/rollup-shared-publisher/x/superblock/store"
)

// Binding defines how to encode a publish-superblock call to a specific contract.
type Binding interface {
	// Address returns the L1 contract address for publishing.
	Address() common.Address

	// BuildPublishCalldata encodes the calldata to publish the given superblock.
	BuildPublishCalldata(ctx context.Context, superblock *store.Superblock) ([]byte, error)
}
