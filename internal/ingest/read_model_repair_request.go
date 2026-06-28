package ingest

// RegisterSourceReadModelRepair registers a coalesced repair-request channel
// for the source supervisor that owns sourceID. The returned function
// unregisters only the same channel.
func (i *Ingester) RegisterSourceReadModelRepair(sourceID string, ch chan<- struct{}) func() {
	if sourceID == "" || ch == nil {
		return func() {}
	}
	i.readModelRepairMu.Lock()
	i.readModelRepairChans[sourceID] = ch
	i.readModelRepairMu.Unlock()
	return func() {
		i.readModelRepairMu.Lock()
		if i.readModelRepairChans[sourceID] == ch {
			delete(i.readModelRepairChans, sourceID)
		}
		i.readModelRepairMu.Unlock()
	}
}

// RequestSourceReadModelRepair wakes the source supervisor so durable
// read_model_state='repair_pending' debt is retried in the current daemon run.
// The send is coalesced; a full channel means a repair request is already
// pending.
func (i *Ingester) RequestSourceReadModelRepair(sourceID string) bool {
	if sourceID == "" {
		return false
	}
	i.readModelRepairMu.Lock()
	ch := i.readModelRepairChans[sourceID]
	i.readModelRepairMu.Unlock()
	if ch == nil {
		i.logger.Warn("read-model repair pending source has no supervisor channel", "source_id", sourceID)
		return false
	}
	select {
	case ch <- struct{}{}:
	default:
	}
	return true
}
