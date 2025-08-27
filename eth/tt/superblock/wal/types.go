package wal

import (
	"time"
)

type Entry struct {
	ID        uint64    `json:"id"`
	Slot      uint64    `json:"slot"`
	Type      EntryType `json:"type"`
	Data      []byte    `json:"data"`
	Timestamp time.Time `json:"timestamp"`
	Checksum  []byte    `json:"checksum"`
}

type EntryType string

const (
	EntrySlotStart   EntryType = "slot_start"
	EntrySCPStart    EntryType = "scp_start"
	EntrySCPDecision EntryType = "scp_decision"
	EntryL2Block     EntryType = "l2_block"
	EntrySuperblock  EntryType = "superblock"
	EntryRollback    EntryType = "rollback"
	EntryCheckpoint  EntryType = "checkpoint"
)
