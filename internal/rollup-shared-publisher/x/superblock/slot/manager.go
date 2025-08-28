package slot

import (
	"context"
	"sync"
	"time"
)

type Manager struct {
	mu                  sync.RWMutex
	genesisTime         time.Time
	slotDuration        time.Duration
	sealCutoverFraction float64
}

func NewManager(genesisTime time.Time, slotDuration time.Duration, sealCutoverFraction float64) *Manager {
	return &Manager{
		genesisTime:         genesisTime,
		slotDuration:        slotDuration,
		sealCutoverFraction: sealCutoverFraction,
	}
}

func (m *Manager) GetCurrentSlot() uint64 {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if time.Now().Before(m.genesisTime) {
		return 0
	}

	elapsed := time.Since(m.genesisTime)
	slot := uint64(elapsed/m.slotDuration) + 1
	return slot
}

func (m *Manager) GetSlotStartTime(slot uint64) time.Time {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if slot == 0 {
		return m.genesisTime
	}

	duration := time.Duration(slot-1) * m.slotDuration
	return m.genesisTime.Add(duration)
}

func (m *Manager) GetSlotProgress() float64 {
	m.mu.RLock()
	defer m.mu.RUnlock()

	currentSlot := m.getCurrentSlotUnlocked()
	slotStart := m.getSlotStartTimeUnlocked(currentSlot)

	elapsed := time.Since(slotStart)
	progress := float64(elapsed) / float64(m.slotDuration)

	if progress < 0 {
		return 0
	}
	if progress > 1 {
		return 1
	}
	return progress
}

func (m *Manager) IsSlotSealTime() bool {
	return m.GetSlotProgress() >= m.sealCutoverFraction
}

func (m *Manager) WaitForNextSlot(ctx context.Context) error {
	currentSlot := m.GetCurrentSlot()
	nextSlotStart := m.GetSlotStartTime(currentSlot + 1)

	timer := time.NewTimer(time.Until(nextSlotStart))
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (m *Manager) SetGenesisTime(genesis time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.genesisTime = genesis
}

func (m *Manager) GetSealTime(slot uint64) time.Time {
	m.mu.RLock()
	defer m.mu.RUnlock()

	slotStart := m.getSlotStartTimeUnlocked(slot)
	sealOffset := time.Duration(float64(m.slotDuration) * m.sealCutoverFraction)
	return slotStart.Add(sealOffset)
}

func (m *Manager) GetSlotEndTime(slot uint64) time.Time {
	m.mu.RLock()
	defer m.mu.RUnlock()

	slotStart := m.getSlotStartTimeUnlocked(slot)
	return slotStart.Add(m.slotDuration)
}

func (m *Manager) TimeUntilSeal() time.Duration {
	currentSlot := m.GetCurrentSlot()
	sealTime := m.GetSealTime(currentSlot)
	return time.Until(sealTime)
}

func (m *Manager) TimeUntilSlotEnd() time.Duration {
	currentSlot := m.GetCurrentSlot()
	endTime := m.GetSlotEndTime(currentSlot)
	return time.Until(endTime)
}

func (m *Manager) getCurrentSlotUnlocked() uint64 {
	if time.Now().Before(m.genesisTime) {
		return 0
	}

	elapsed := time.Since(m.genesisTime)
	slot := uint64(elapsed/m.slotDuration) + 1
	return slot
}

func (m *Manager) getSlotStartTimeUnlocked(slot uint64) time.Time {
	if slot == 0 {
		return m.genesisTime
	}

	duration := time.Duration(slot-1) * m.slotDuration
	return m.genesisTime.Add(duration)
}
