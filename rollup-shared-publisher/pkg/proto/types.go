package proto

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"google.golang.org/protobuf/proto"
)

func (xt *XTRequest) XtID() (*XtID, error) {
	data, err := proto.Marshal(xt)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal XTRequest: %w", err)
	}

	hash := sha256.Sum256(data)

	return &XtID{Hash: hash[:]}, nil
}

// Return list of chain ids participating in cross chain transaction
func (xt *XTRequest) ChainIDs() map[string]struct{} {
	chains := make(map[string]struct{})
	for _, tx := range xt.Transactions {
		if tx != nil && len(tx.ChainId) > 0 {
			chainID := hex.EncodeToString(tx.ChainId)
			chains[chainID] = struct{}{}
		}
	}
	return chains
}

func (id *XtID) Hex() string {
	if id == nil || id.Hash == nil {
		return ""
	}

	return hex.EncodeToString(id.Hash)
}
