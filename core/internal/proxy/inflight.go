package proxy

import "sync"

// CheckDedup is a process-wide registry of proxy IDs that are currently being
// network-checked. Every bulk check path acquires an id before its network
// check and releases it when done; a second job that finds the id busy skips
// it (no duplicate network check, no result written) instead of racing on the
// same proxy.
type CheckDedup struct {
	owners sync.Map // proxyID (int) -> jobID (string)
}

// globalCheckDedup is shared by every check path in the process.
var globalCheckDedup = &CheckDedup{}

// InFlightCheckDedup returns the process-wide registry.
func InFlightCheckDedup() *CheckDedup {
	return globalCheckDedup
}

// TryAcquire marks proxyID as being checked by jobID. It reports whether the
// acquire succeeded; when the id is already owned by another in-flight check
// it returns false with the current owner's job id.
func (d *CheckDedup) TryAcquire(proxyID int, jobID string) (bool, string) {
	if prev, loaded := d.owners.LoadOrStore(proxyID, jobID); loaded {
		return false, prev.(string)
	}
	return true, ""
}

// Release clears the owner for proxyID. It is a no-op when the id is no
// longer owned by jobID, so a stale release can never free a slot that a
// newer check already took over.
func (d *CheckDedup) Release(proxyID int, jobID string) {
	d.owners.CompareAndDelete(proxyID, jobID)
}
