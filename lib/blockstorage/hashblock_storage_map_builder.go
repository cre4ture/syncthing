package blockstorage

type HashBlockStorageMapBuilder struct {
	ownName      string
	cb           func(d HashAndState)
	currentHash  string
	currentState HashBlockState
	strToHashFn  func(string) []byte
}

func NewHashBlockStorageMapBuilder(ownName string, strToHashFn func(string) []byte, cb func(d HashAndState)) *HashBlockStorageMapBuilder {
	return &HashBlockStorageMapBuilder{
		ownName:      ownName,
		cb:           cb,
		currentHash:  "",
		currentState: HashBlockState{},
		strToHashFn:  strToHashFn,
	}
}

func (b *HashBlockStorageMapBuilder) completeIfNextHash(hash string) bool {
	complete := hash != b.currentHash
	if complete {
		if b.currentState.dataExists {
			b.cb(HashAndState{b.strToHashFn(b.currentHash), b.currentState})
		}
		b.currentHash = hash
		b.currentState = HashBlockState{}
	}
	return false
}

func (b *HashBlockStorageMapBuilder) addData(hash string) {
	if b.completeIfNextHash(hash) {
		return
	}

	b.currentState.dataExists = true
}

func (b *HashBlockStorageMapBuilder) addUse(hash string, who string) {
	if b.completeIfNextHash(hash) {
		return
	}

	if b.currentState.IsAvailable() {
		isMe := who == b.ownName
		if isMe {
			b.currentState.reservedByMe = true
		} else {
			b.currentState.reservedByOthers = true
		}
	}
}

func (b *HashBlockStorageMapBuilder) addDelete(hash string) {
	if b.completeIfNextHash(hash) {
		return
	}

	// it will be deleted soon
	b.currentState.deletionPending = true
}

func (b *HashBlockStorageMapBuilder) close() {
	b.completeIfNextHash("") // force flush of last element
}
