package tripstate

// RiderRecoverableSQL uses trip alias t and settlement alias s. Recovery and
// creation share this invariant: unfinished transport or unresolved settlement.
const RiderRecoverableSQL = `(t.status IN ('pending','assigned','accepted','arrived','started') OR (t.status='completed' AND s.status IN ('pending','disputed')))`
