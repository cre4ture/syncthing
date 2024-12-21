package db

import "github.com/syncthing/syncthing/lib/protocol"

type DbSnapshotI interface {
	Release()
	GetGlobal(file string) (protocol.FileInfo, bool)
	GetGlobalTruncated(file string) (protocol.FileInfo, bool)
	WithPrefixedGlobalTruncated(prefix string, fn Iterator)
}
