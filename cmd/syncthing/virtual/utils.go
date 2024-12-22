package virtual

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/syncthing/syncthing/internal/gen/bep"
	"github.com/syncthing/syncthing/lib/blockstorage"
	"github.com/syncthing/syncthing/lib/db"
	"github.com/syncthing/syncthing/lib/logger"
	"github.com/syncthing/syncthing/lib/protocol"
	"github.com/syncthing/syncthing/lib/utils"
	"gocloud.dev/blob"
	"google.golang.org/protobuf/proto"
)

type OfflineStorageAccess struct {
	BlockStorage *blockstorage.GoCloudUrlStorage
	DeviceID     string
	FolderID     string
	FSetRO       *OfflineDbFileSetRead
}

func NewOfflineDataAccess(URL string, DeviceID string, FolderID string) (*OfflineStorageAccess, error) {
	BlockStorage := blockstorage.NewGoCloudUrlStorageFromConfigStr(context.Background(), URL, "")

	devices, err := listDeviceIDs(BlockStorage)
	if err != nil {
		return nil, err
	}

	println("known devices:")
	for _, device := range devices {
		println(device)
	}

	if len(devices) == 0 {
		return nil, errors.New("this URL doesn't list any syncthing devices. Abort")
	}

	if DeviceID == "" {
		// by default, take the only device
		if len(devices) > 1 {
			return nil, errors.New("this URL lists multiple syncthing devices. You need to specify which one. Abort")
		}
		DeviceID = devices[0]
	}

	folders, err := listFolderIDs(BlockStorage, DeviceID)
	if err != nil {
		return nil, err
	}

	println("known folders for device " + DeviceID + ":")
	for _, folder := range folders {
		println(folder)
	}

	if len(folders) == 0 {
		return nil, errors.New("this URL doesn't list any syncthing folders for specified device. Abort")
	}

	if FolderID == "" {
		// by default, take the only folder
		if len(folders) > 1 {
			return nil, errors.New("this URL lists multiple syncthing folders for specified device. You need to specify which one. Abort")
		}
		FolderID = folders[0]
	}

	metaPrefix := blockstorage.LOCAL_HAVE_FI_META_PREFIX + "/" +
		DeviceID + "/" +
		FolderID + "/"

	fsetRO := NewOfflineDbFileSetRead(metaPrefix, BlockStorage)

	return &OfflineStorageAccess{
		BlockStorage: BlockStorage,
		DeviceID:     DeviceID,
		FolderID:     FolderID,
		FSetRO:       fsetRO,
	}, nil
}

func listDeviceIDs(storage *blockstorage.GoCloudUrlStorage) ([]string, error) {
	prefix := blockstorage.MetaDataSubFolder + "/" +
		blockstorage.LOCAL_HAVE_FI_META_PREFIX + "/"
	return listSubdirs(storage, prefix, "/")
}

func listFolderIDs(storage *blockstorage.GoCloudUrlStorage, deviceID string) ([]string, error) {
	prefix := blockstorage.MetaDataSubFolder + "/" +
		blockstorage.LOCAL_HAVE_FI_META_PREFIX + "/" +
		deviceID + "/"
	return listSubdirs(storage, prefix, "/")
}

func iterateSubdirs(storage *blockstorage.GoCloudUrlStorage, prefix string, delimiter string, fn func(e *blob.ListObject)) error {
	bucket := storage.RawAccess()
	listCtx, listCtxCancel := context.WithCancel(context.Background())
	defer listCtxCancel()
	opts := &blob.ListOptions{Prefix: prefix, Delimiter: delimiter}
	token := blob.FirstPageToken
	for {
		var page []*blob.ListObject
		var err error
		page, token, err = bucket.ListPage(listCtx, token, 100, opts)
		if err != nil {
			return err
		}

		if len(page) == 0 {
			return nil
		}

		for _, e := range page {
			fn(e)
		}
	}
}

func listSubdirs(storage *blockstorage.GoCloudUrlStorage, prefix string, delimiter string) ([]string, error) {
	names := make([]string, 0)
	iterateSubdirs(storage, prefix, delimiter, func(e *blob.ListObject) {
		name, _ := strings.CutPrefix(e.Key, prefix)
		if delimiter != "" {
			name, _ = strings.CutSuffix(name, delimiter)
		}
		names = append(names, name)
	})
	return names, nil
}

type Caches struct {
	fileCache *utils.Protected[map[string]*protocol.FileInfo]
	dirCache  *utils.Protected[map[string]*utils.Protected[[]*protocol.FileInfo]]
}

type OfflineDbSnapshotI struct {
	metaPrefix   string
	blockStorage *blockstorage.GoCloudUrlStorage
	caches       *Caches
}

// GetGlobal implements db.DbSnapshotI.
func (o *OfflineDbSnapshotI) GetGlobal(file string) (protocol.FileInfo, bool) {
	var fi *protocol.FileInfo = nil
	var ok bool = false
	func() {
		cache := o.caches.fileCache.Lock()
		defer o.caches.fileCache.Unlock()
		fi, ok = (*cache)[file]
	}()

	logger.DefaultLogger.Debugf("GetGlobal(%v): cache-ok:%v, data len:%v", file, ok, fi)
	if ok {
		if fi == nil {
			return protocol.FileInfo{}, false
		}
		return *fi, true
	}

	fullUrl := o.metaPrefix + file
	data, err := o.blockStorage.GetMeta(fullUrl)
	logger.DefaultLogger.Debugf("GetGlobal(%v): %v, ok:%v, data len:%v", file, fullUrl, ok, len(data))
	if err != nil {
		func() {
			cache := o.caches.fileCache.Lock()
			defer o.caches.fileCache.Unlock()
			(*cache)[file] = nil
		}()
		return protocol.FileInfo{}, false
	}

	wireFi := &bep.FileInfo{}
	err = proto.Unmarshal(data, wireFi)
	fiCpy := protocol.FileInfoFromWire(wireFi)
	fi = &fiCpy
	logger.DefaultLogger.Debugf("GetGlobal(%v): unmarshal-err: %+v. fi: %+v", file, err, fi)
	if err != nil {
		return protocol.FileInfo{}, false
	}

	func() {
		cache := o.caches.fileCache.Lock()
		defer o.caches.fileCache.Unlock()
		(*cache)[file] = fi
	}()

	return *fi, true
}

// GetGlobalTruncated implements db.DbSnapshotI.
func (o *OfflineDbSnapshotI) GetGlobalTruncated(file string) (protocol.FileInfo, bool) {
	return o.GetGlobal(file)
}

// Release implements db.DbSnapshotI.
func (o *OfflineDbSnapshotI) Release() {
	// ignore
}

// WithPrefixedGlobalTruncated implements db.DbSnapshotI.
func (o *OfflineDbSnapshotI) WithPrefixedGlobalTruncated(prefix string, fn db.Iterator) {
	logger.DefaultLogger.Debugf("WithPrefixedGlobalTruncated(%v)", prefix)
	rootPrefix := blockstorage.MetaDataSubFolder + "/" + o.metaPrefix
	if (len(prefix) != 0) && !strings.HasSuffix(prefix, "/") {
		prefix = prefix + "/"
	}

	var pChilds *utils.Protected[[]*protocol.FileInfo] = nil
	var childs *[]*protocol.FileInfo = nil
	ok := false
	func() {
		cache := o.caches.dirCache.Lock()
		defer o.caches.dirCache.Unlock()
		pChilds, ok = (*cache)[prefix]
		if !ok {
			pChilds = utils.NewProtected([]*protocol.FileInfo{})
			(*cache)[prefix] = pChilds
			childs = pChilds.Lock() // prevent anybody else
		}
	}()

	func() {
		if ok {
			childs = pChilds.Lock() // here it doesn't matter
		}
		defer pChilds.Unlock()

		if !ok {
			ch := make(chan *protocol.FileInfo, 10)
			wg := sync.WaitGroup{}

			fullPrefix := rootPrefix + prefix
			iterateSubdirs(o.blockStorage, fullPrefix, "/", func(e *blob.ListObject) {
				wg.Add(1)
				go func() {
					defer wg.Done()
					name, _ := strings.CutPrefix(e.Key, rootPrefix)
					logger.DefaultLogger.Debugf("WithPrefixedGlobalTruncated(%v): %v", prefix, name)
					fi, ok := o.GetGlobal(name)
					logger.DefaultLogger.Debugf("WithPrefixedGlobalTruncated(%v): %v, ok:%v: %+v", prefix, ok, fi)
					if !ok {
						return
					}
					fi.Name, _ = strings.CutPrefix(fi.Name, prefix)
					ch <- &fi
				}()
			})

			go func() {
				wg.Wait()
				close(ch)
			}()

			for fi := range ch {
				*childs = append(*childs, fi)
			}
		}

		for _, child := range *childs {
			fn(*child)
		}
	}()
}

type OfflineDbFileSetRead struct {
	metaPrefix   string
	blockStorage *blockstorage.GoCloudUrlStorage
	caches       *Caches
}

func NewOfflineDbFileSetRead(
	metaPrefix string,
	blockStorage *blockstorage.GoCloudUrlStorage,
) *OfflineDbFileSetRead {
	return &OfflineDbFileSetRead{
		metaPrefix:   metaPrefix,
		blockStorage: blockStorage,
		caches: &Caches{
			fileCache: utils.NewProtected[map[string]*protocol.FileInfo](make(map[string]*protocol.FileInfo)),
			dirCache:  utils.NewProtected[map[string]*utils.Protected[[]*protocol.FileInfo]](make(map[string]*utils.Protected[[]*protocol.FileInfo])),
		},
	}
}

// SnapshotI implements model.DbFileSetReadI.
func (o *OfflineDbFileSetRead) SnapshotI() (db.DbSnapshotI, error) {
	return &OfflineDbSnapshotI{o.metaPrefix, o.blockStorage, o.caches}, nil
}
