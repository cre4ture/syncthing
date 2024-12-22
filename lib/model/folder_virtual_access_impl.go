// Copyright (C) 2024 The Syncthing Authors.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this file,
// You can obtain one at https://mozilla.org/MPL/2.0/.

package model

import (
	"bytes"
	"context"
	"path"
	"strings"
	"syscall"
	"time"

	ffs "github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
	"github.com/syncthing/syncthing/lib/config"
	"github.com/syncthing/syncthing/lib/db"
	"github.com/syncthing/syncthing/lib/logger"
	"github.com/syncthing/syncthing/lib/protocol"
	"github.com/syncthing/syncthing/lib/scanner"
	"github.com/syncthing/syncthing/lib/sync"
	"golang.org/x/exp/constraints"
	"golang.org/x/exp/maps"
)

type syncthingVirtualFolderFuseAdapter struct {
	folderType config.FolderType
	folderID   string
	modelID    protocol.ShortID
	fset       DbFileSetReadI
	fsetRW     DbFileSetWriteI
	dataAccess BlockDataAccessI

	// ino mapping
	ino_mu      sync.Mutex
	next_ino_nr uint64
	ino_mapping map[string]uint64

	// encrypted directories
	directories_mu sync.Mutex
	directories    map[string]*TreeEntry
}

var _ = (SyncthingVirtualFolderAccessI)((*syncthingVirtualFolderFuseAdapter)(nil))

type DbFileSetReadI interface {
	SnapshotI() (db.DbSnapshotI, error)
}

type DbFileSetWriteI interface {
	Update(fs []protocol.FileInfo)
	UpdateOneLocalFileInfoLocalChangeDetected(fi *protocol.FileInfo)
}

type BlockDataAccessI interface {
	GetBlockDataFromCacheOrDownloadI(
		file *protocol.FileInfo,
		block protocol.BlockInfo,
	) ([]byte, error, GetBlockDataResult)
	ReserveAndSetI(hash []byte, data []byte)
	RequestBackgroundDownloadI(filename string, size int64, modified time.Time)
}

func NewSyncthingVirtualFolderFuseAdapter(
	modelID protocol.ShortID,
	folderID string,
	folderType config.FolderType,
	fset DbFileSetReadI,
	fsetRW DbFileSetWriteI,
	dataAccess BlockDataAccessI,
) *syncthingVirtualFolderFuseAdapter {
	return &syncthingVirtualFolderFuseAdapter{
		folderType:     folderType,
		folderID:       folderID,
		modelID:        modelID,
		fset:           fset,
		fsetRW:         fsetRW,
		dataAccess:     dataAccess,
		ino_mu:         sync.NewMutex(),
		next_ino_nr:    1,
		ino_mapping:    make(map[string]uint64),
		directories_mu: sync.NewMutex(),
		directories:    make(map[string]*TreeEntry),
	}
}

func (r *syncthingVirtualFolderFuseAdapter) getInoOf(path string) uint64 {
	r.ino_mu.Lock()
	defer r.ino_mu.Unlock()
	ino, ok := r.ino_mapping[path]
	if !ok {
		ino = r.next_ino_nr
		r.next_ino_nr += 1
		r.ino_mapping[path] = ino
	}
	return ino
}

func (stf *syncthingVirtualFolderFuseAdapter) lookupFile(path string) (info *protocol.FileInfo, eno syscall.Errno) {
	snap, err := stf.fset.SnapshotI()
	if err != nil {
		//stf..log()
		return nil, syscall.EFAULT
	}
	defer snap.Release()

	fi, ok := snap.GetGlobalTruncated(path)
	if !ok {
		if stf.folderType.IsReceiveEncrypted() {
			stf.directories_mu.Lock()
			defer stf.directories_mu.Unlock()
			logger.DefaultLogger.Infof("ENC VIRT lookup %s - %+v", path, stf.directories)
			entry, exists := stf.directories[path]
			if exists {
				return &protocol.FileInfo{
					Name: entry.Name,
					Type: protocol.FileInfoTypeDirectory,
				}, 0
			}
		}

		return nil, syscall.ENOENT
	}

	return &fi, 0
}

func createNewVirtualFileInfo(creator protocol.ShortID, Permissions *uint32, name string) protocol.FileInfo {

	creationTime := time.Now()
	fi := protocol.FileInfo{}
	fi.Name = name
	// fi.Size =
	fi.ModifiedS = creationTime.Unix()
	fi.ModifiedBy = creator
	fi.Version = fi.Version.Update(creator)
	// fi.Sequence =
	fi.Blocks = append([]protocol.BlockInfo{}, protocol.BlockInfo{Size: 0})
	// fi.SymlinkTarget =
	// BlocksHash
	// Encrypted
	fi.Type = protocol.FileInfoTypeFile
	if Permissions != nil {
		fi.Permissions = *Permissions
	}
	fi.ModifiedNs = int32(creationTime.Nanosecond())
	// fi.RawBlockSize
	// fi.Platform
	// fi.LocalFlags
	// fi.VersionHash
	fi.InodeChangeNs = creationTime.UnixNano()
	// EncryptionTrailerSize
	// Deleted
	// RawInvalid
	fi.NoPermissions = Permissions == nil

	return fi
}

func (stf *syncthingVirtualFolderFuseAdapter) createFile(
	Permissions *uint32, name string,
) (info *protocol.FileInfo, eno syscall.Errno) {

	if stf.folderType.IsReceiveOnly() {
		return nil, syscall.EACCES
	}

	fi := createNewVirtualFileInfo(stf.modelID, Permissions, name)
	stf.fsetRW.UpdateOneLocalFileInfoLocalChangeDetected(&fi)

	snap, err := stf.fset.SnapshotI()
	if err != nil {
		return nil, syscall.EAGAIN
	}
	defer snap.Release()

	db_fi, ok := snap.GetGlobalTruncated(name)
	if !ok {
		return nil, syscall.ENOENT
	}

	return &db_fi, 0
}

func calculateHashForBlock(ctx context.Context, blockData []byte,
) (bi protocol.BlockInfo, err error) {
	blockInfos, err := scanner.Blocks(
		ctx, bytes.NewReader(blockData), len(blockData), int64(len(blockData)), nil, true)
	if err != nil {
		return protocol.BlockInfo{}, err // Context done
	}
	if len(blockInfos) != 1 {
		panic("internal error: output length not as expected!")
	}
	return blockInfos[0], nil
}

func (stf *syncthingVirtualFolderFuseAdapter) writeFile(
	ctx context.Context, name string, offset uint64, inputData []byte,
) syscall.Errno {

	if stf.folderType.IsReceiveOnly() {
		return syscall.EACCES
	}

	snap, err := stf.fset.SnapshotI()
	if err != nil {
		//stf..log()
		return syscall.EFAULT
	}
	defer snap.Release()

	fi, ok := snap.GetGlobal(name)
	if !ok {
		return syscall.ENOENT
	}

	if fi.RawBlockSize == 0 {
		fi.RawBlockSize = protocol.MinBlockSize
	}

	if fi.Blocks == nil {
		fi.Blocks = []protocol.BlockInfo{}
	}

	inputPos := 0
	blockIdx := int(offset / uint64(fi.RawBlockSize))
	writeStartInBlock := int(offset % uint64(fi.RawBlockSize))

	if blockIdx >= (len(fi.Blocks) + 1 /* appending one block is OK */) {
		return syscall.EINVAL
	}

	for {
		writeEndInBlock := clamp(writeStartInBlock+len(inputData), writeStartInBlock, int(fi.RawBlockSize))
		writeLenInBlock := writeEndInBlock - writeStartInBlock

		ok := false
		var blockData = []byte{}
		if blockIdx < len(fi.Blocks) {
			bi := fi.Blocks[blockIdx]
			blockData, err, _ = stf.dataAccess.GetBlockDataFromCacheOrDownloadI(&fi, bi)
			ok = (err == nil)
		}
		if !ok {
			// allocate new block: // TODO: what in case of temporary connection issue?
			blockData = make([]byte, writeEndInBlock)
		}

		inputPosNext := inputPos + writeLenInBlock
		copy(blockData[writeStartInBlock:writeEndInBlock], inputData[inputPos:inputPosNext])

		biNew, err := calculateHashForBlock(ctx, blockData)
		if err != nil {
			return syscall.ECONNABORTED
		}

		// offset needs to be corrected as not the full file was re-calculated
		biNew.Offset = int64(blockIdx) * int64(fi.RawBlockSize)
		if blockIdx < len(fi.Blocks) {
			fi.Blocks[blockIdx] = biNew
		} else {
			fi.Blocks = append(fi.Blocks, biNew)
		}
		writeEndInFile := biNew.Offset + int64(writeEndInBlock)
		if writeEndInFile > fi.Size {
			fi.Size = writeEndInFile
		}
		changeTime := time.Now()
		fi.ModifiedBy = stf.modelID
		fi.ModifiedS = changeTime.Unix()
		fi.ModifiedNs = int32(changeTime.Nanosecond())
		fi.InodeChangeNs = changeTime.UnixNano()
		fi.Version = fi.Version.Update(fi.ModifiedBy)

		stf.dataAccess.ReserveAndSetI(biNew.Hash, blockData)
		stf.fsetRW.UpdateOneLocalFileInfoLocalChangeDetected(&fi)

		blockIdx += 1
		inputPos = inputPosNext
		writeStartInBlock = 0

		if inputPosNext >= len(inputData) {
			return ffs.OK
		}
	}
}

func (stf *syncthingVirtualFolderFuseAdapter) deleteFile(ctx context.Context, path string) syscall.Errno {

	if stf.folderType.IsReceiveOnly() {
		return syscall.EACCES
	}

	snap, err := stf.fset.SnapshotI()
	if err != nil {
		//stf..log()
		return syscall.EFAULT
	}
	defer snap.Release()

	fi, ok := snap.GetGlobal(path)
	if !ok {
		return syscall.ENOENT
	}

	fi.ModifiedBy = stf.modelID
	fi.Deleted = true
	fi.Size = 0
	fi.Blocks = nil
	fi.Version = fi.Version.Update(stf.modelID)
	stf.fsetRW.UpdateOneLocalFileInfoLocalChangeDetected(&fi)

	return 0
}

func (stf *syncthingVirtualFolderFuseAdapter) createDir(ctx context.Context, path string) syscall.Errno {

	if stf.folderType.IsReceiveOnly() {
		return syscall.EACCES
	}

	_, eno := stf.lookupFile(path)
	if eno == 0 {
		return syscall.EEXIST
	}

	fi := createNewVirtualFileInfo(stf.modelID, nil, path)
	fi.Type = protocol.FileInfoTypeDirectory
	fi.Blocks = nil
	stf.fsetRW.UpdateOneLocalFileInfoLocalChangeDetected(&fi)
	return 0
}

func (stf *syncthingVirtualFolderFuseAdapter) deleteDir(ctx context.Context, path string) syscall.Errno {

	if stf.folderType.IsReceiveOnly() {
		return syscall.EACCES
	}

	snap, err := stf.fset.SnapshotI()
	if err != nil {
		//stf..log()
		return syscall.EFAULT
	}
	defer snap.Release()

	fi, ok := snap.GetGlobal(path)
	if !ok {
		return syscall.ENOENT
	}

	if fi.Type != protocol.FileInfoTypeDirectory {
		return syscall.ENOTDIR
	}

	fi.ModifiedBy = stf.modelID
	fi.Deleted = true
	fi.Size = 0
	fi.Blocks = []protocol.BlockInfo{}
	fi.Version = fi.Version.Update(stf.modelID)
	stf.fsetRW.UpdateOneLocalFileInfoLocalChangeDetected(&fi)

	return 0
}

func (stf *syncthingVirtualFolderFuseAdapter) renameFileOrDir(
	ctx context.Context, existingPath string, newPath string,
) syscall.Errno {

	if stf.folderType.IsReceiveOnly() {
		return syscall.EACCES
	}

	snap, err := stf.fset.SnapshotI()
	if err != nil {
		//stf..log()
		return syscall.EFAULT
	}
	defer snap.Release()

	fi, ok := snap.GetGlobal(existingPath)
	if !ok {
		return syscall.ENOENT
	}

	_, ok = snap.GetGlobalTruncated(newPath)
	if ok {
		return syscall.EEXIST
	}

	fi.ModifiedBy = stf.modelID
	fi.Name = newPath
	fi.Version = fi.Version.Update(stf.modelID)
	stf.fsetRW.UpdateOneLocalFileInfoLocalChangeDetected(&fi)

	return stf.deleteFile(ctx, existingPath)
}

func (stf *syncthingVirtualFolderFuseAdapter) renameExchangeFileOrDir(
	ctx context.Context, path1 string, path2 string,
) syscall.Errno {

	if stf.folderType.IsReceiveOnly() {
		return syscall.EACCES
	}

	snap, err := stf.fset.SnapshotI()
	if err != nil {
		//stf..log()
		return syscall.EFAULT
	}
	defer snap.Release()

	fi1, ok := snap.GetGlobal(path1)
	if !ok {
		return syscall.ENOENT
	}

	fi2, ok := snap.GetGlobal(path2)
	if !ok {
		return syscall.ENOENT
	}

	fi1.ModifiedBy = stf.modelID
	fi2.ModifiedBy = stf.modelID

	origFi1Name := fi1.Name
	fi1.Name = fi2.Name
	fi2.Name = origFi1Name

	origFi1Version := fi1.Version
	fi1.Version = fi2.Version.Update(stf.modelID)
	fi2.Version = origFi1Version.Update(stf.modelID)

	fiList := append([]protocol.FileInfo{}, fi1, fi2)

	stf.fsetRW.Update(fiList)

	return 0
}

func (stf *syncthingVirtualFolderFuseAdapter) createSymlink(
	ctx context.Context, path, target string,
) syscall.Errno {

	if stf.folderType.IsReceiveOnly() {
		return syscall.EACCES
	}

	_, eno := stf.lookupFile(path)
	if eno == 0 {
		return syscall.EEXIST
	}

	fi := createNewVirtualFileInfo(stf.modelID, nil, path)
	fi.Type = protocol.FileInfoTypeSymlink
	fi.Blocks = nil
	fi.SymlinkTarget = target

	stf.fsetRW.UpdateOneLocalFileInfoLocalChangeDetected(&fi)
	return 0
}

type VirtualFolderDirStream struct {
	root     *syncthingVirtualFolderFuseAdapter
	dirPath  string
	children []*TreeEntry
	i        int
}

func (s *VirtualFolderDirStream) HasNext() bool {
	return s.i < len(s.children)
}
func (s *VirtualFolderDirStream) Next() (fuse.DirEntry, syscall.Errno) {
	if !s.HasNext() {
		return fuse.DirEntry{}, syscall.ENOENT
	}

	child := s.children[s.i]
	s.i += 1

	mode := syscall.S_IFREG
	switch child.Type {
	case protocol.FileInfoTypeDirectory:
		mode = syscall.S_IFDIR
	case protocol.FileInfoTypeSymlink:
		mode = syscall.S_IFLNK
	case protocol.FileInfoTypeFile:
		fallthrough
	default:
		break
	}

	return fuse.DirEntry{
		Mode: uint32(mode),
		Name: child.Name,
		Ino:  s.root.getInoOf(path.Join(s.dirPath, child.Name)),
	}, 0
}
func (s *VirtualFolderDirStream) Close() {}

func (f *syncthingVirtualFolderFuseAdapter) readDir(path string) (stream ffs.DirStream, eno syscall.Errno) {

	var err error = nil
	children := []*TreeEntry{}
	levels := 0

	snap, err := f.fset.SnapshotI()
	if err != nil {
		return nil, syscall.EFAULT
	}
	defer snap.Release()

	if f.folderType.IsReceiveEncrypted() {

		if path != "" && !strings.HasSuffix(path, "/") {
			path = path + "/"
		}

		fileMap := make(map[string]*TreeEntry)
		snap.WithPrefixedGlobalTruncated(path, func(child protocol.FileInfo) bool {
			childPath := child.FileName()
			childPath = strings.TrimPrefix(childPath, path)
			logger.DefaultLogger.Infof("ENC VIRT path - %s, full: %v", childPath, child.FileName())
			parts := strings.Split(childPath, "/")
			logger.DefaultLogger.Infof("ENC VIRT ls - %s, parts: %v", childPath, parts)
			if len(parts) == 1 {
				logger.DefaultLogger.Infof("ENC VIRT ADD CHILD-FILE - %s", parts[0])
				fileMap[parts[0]] = &TreeEntry{
					Name:    parts[0],
					Type:    child.FileType(),
					ModTime: child.ModTime(),
					Size:    child.FileSize(),
				}
			} else {
				_, exists := fileMap[parts[0]]
				if !exists {
					logger.DefaultLogger.Infof("ENC VIRT ADD CHILD-DIR - %s", parts[0])
					entry := &TreeEntry{
						Name: parts[0],
						Type: protocol.FileInfoTypeDirectory,
					}
					f.directories_mu.Lock()
					f.directories[path+parts[0]] = entry
					f.directories_mu.Unlock()
					fileMap[parts[0]] = entry
				}
			}
			return true
		})

		newChildren := maps.Values(fileMap)
		logger.DefaultLogger.Infof("ENC VIRT before, after - %+v [->] %+v", children, newChildren)
		children = newChildren
	} else {
		children, err = SnapshotGlobalDirectoryTree(snap, path, levels, false)
		if err != nil {
			logger.DefaultLogger.Infof("ENC VIRT err -> %v", err)
			return nil, syscall.EFAULT
		}
	}

	return &VirtualFolderDirStream{
		root:     f,
		dirPath:  path,
		children: children,
	}, 0
}

type VirtualFileReadResult struct {
	f           *syncthingVirtualFolderFuseAdapter
	snap        db.DbSnapshotI
	fi          *protocol.FileInfo
	offset      uint64
	maxToBeRead int
}

func clamp[T constraints.Ordered](a, min, max T) T {
	if min > max {
		panic("clamp: min > max is not allowed")
	}
	if a > max {
		return max
	}
	if a < min {
		return min
	}
	return a
}

func (vf *VirtualFileReadResult) readOneBlock(offset uint64, remainingToRead int) ([]byte, fuse.Status) {

	blockSize := vf.fi.BlockSize()
	blockIndex := int(offset / uint64(blockSize))

	logger.DefaultLogger.Infof(
		"VirtualFileReadResult readOneBlock(offset, len): %v, %v. bSize, bIdx: %v, %v",
		offset, remainingToRead, blockSize, blockIndex)

	if blockIndex >= len(vf.fi.Blocks) {
		return nil, 0
	}

	block := vf.fi.Blocks[blockIndex]

	rel_pos := int64(offset) - block.Offset
	if rel_pos < 0 {
		return nil, 0
	}

	inputData, err, _ := vf.f.dataAccess.GetBlockDataFromCacheOrDownloadI(vf.fi, block)
	if err != nil {
		return nil, fuse.Status(syscall.EAGAIN)
	}

	remainingInBlock := clamp(len(inputData)-int(rel_pos), 0, len(inputData))
	maxToBeRead := clamp(remainingToRead, 0, vf.maxToBeRead)
	readAmount := clamp(remainingInBlock, 0, maxToBeRead)

	if readAmount != 0 {
		return inputData[rel_pos : rel_pos+int64(readAmount)], 0
	} else {
		return nil, 0
	}
}

func (vf *VirtualFileReadResult) Bytes(outBuf []byte) ([]byte, fuse.Status) {

	logger.DefaultLogger.Infof("VirtualFileReadResult Bytes(len): %v", len(outBuf))

	outBufSize := len(outBuf)
	initialReadData, status := vf.readOneBlock(vf.offset, outBufSize)
	if status != 0 {
		return nil, status
	}

	nextOutBufWriteBegin := len(initialReadData)
	if nextOutBufWriteBegin >= outBufSize {
		// done in one step
		return initialReadData, 0
	}

	copy(outBuf, initialReadData)

	for nextOutBufWriteBegin < outBufSize {
		remainingToBeRead := outBufSize - nextOutBufWriteBegin
		nextReadData, status := vf.readOneBlock(vf.offset+uint64(nextOutBufWriteBegin), remainingToBeRead)
		if status != 0 {
			return nil, status
		}
		if len(nextReadData) == 0 {
			break
		}
		readLen := copy(outBuf[nextOutBufWriteBegin:], nextReadData)
		nextOutBufWriteBegin += readLen
	}

	if nextOutBufWriteBegin != outBufSize {
		logger.DefaultLogger.Infof("Read incomplete: %d/%d", nextOutBufWriteBegin, len(outBuf))
	}

	if nextOutBufWriteBegin != 0 {

		// request download of remaining file data:
		vf.f.dataAccess.RequestBackgroundDownloadI(vf.fi.Name, vf.fi.Size, vf.fi.ModTime())

		return outBuf[:nextOutBufWriteBegin], 0
	} else {
		return nil, 0
	}
}
func (vf *VirtualFileReadResult) Size() int {
	return clamp(vf.maxToBeRead, 0, int(vf.fi.Size-int64(vf.offset)))
}
func (vf *VirtualFileReadResult) Done() {
	if vf.snap != nil {
		vf.snap.Release()
		vf.snap = nil
	}
}

func (f *syncthingVirtualFolderFuseAdapter) readFile(
	path string, buf []byte, off int64,
) (res fuse.ReadResult, errno syscall.Errno) {
	snap, err := f.fset.SnapshotI()
	if err != nil {
		//stf..log()
		return nil, syscall.EFAULT
	}
	cancelDefer := false
	defer func() {
		if !cancelDefer {
			snap.Release()
		}
	}()

	fi, ok := snap.GetGlobal(path)
	if !ok {
		return nil, syscall.ENOENT
	}

	cancelDefer = true
	return &VirtualFileReadResult{
		f:           f,
		snap:        snap,
		fi:          &fi,
		offset:      uint64(off),
		maxToBeRead: len(buf),
	}, 0
}
