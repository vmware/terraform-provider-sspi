// © Broadcom. All Rights Reserved.
// The term "Broadcom" refers to Broadcom Inc. and/or its subsidiaries.

// Package backuprestorelcm holds the single mutex shared by sspi_backup and
// sspi_restore.
package backuprestorelcm

import "sync"

// Mu serialises SSPI appliance backup and restore actions against each
// other within a single Terraform apply: the appliance has exactly one
// backup/restore execution slot, and a restore rewrites the very state a
// concurrent backup would be trying to capture, so the two must never run
// at the same time. This is deliberately a package of its own (rather than
// living in resource_backup or resource_restore) so both resource packages
// can share exactly one lock instance without importing one another —
// two independent mutexes here would defeat the whole point, since each
// would only serialise a resource against itself, not against the other.
// It is deliberately a separate lock from platformLcmMu
// (internal/provider/resource_platform) and upgradeLcmMu
// (internal/provider/resource_upgrade): backup/restore, appliance
// self-upgrade, and platform/cluster LCM are distinct backend workflow
// slots, so serialising all of them together would only add unnecessary
// contention with no correctness benefit.
var Mu sync.Mutex
