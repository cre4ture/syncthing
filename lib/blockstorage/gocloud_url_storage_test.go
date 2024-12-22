package blockstorage

import (
	"context"
	"crypto/sha256"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/syncthing/syncthing/lib/hashutil"
)

const MY_NAME = "MY-OWN-NAME"
const OTHER_NAME_1 = "OTHER-DEVICE-1"
const OTHER_NAME_2 = "OTHER-DEVICE-2"

var byteHash_1 [32]byte = sha256.Sum256([]byte("hash_1"))
var byteHash_2 [32]byte = sha256.Sum256([]byte("hash_2"))
var hash_1 string = hashutil.HashToStringMapKey(byteHash_1[:])
var hash_2 string = hashutil.HashToStringMapKey(byteHash_2[:])
var slashedHash_1 string = hashutil.HashToSlashedStringMapKey(byteHash_1[:])
var slashedHash_2 string = hashutil.HashToSlashedStringMapKey(byteHash_2[:])

func TestHashBlockStorageMapBuilder_addData(t *testing.T) {
	calls := 0
	lastState := HashBlockState{}
	builder := NewHashBlockStorageMapBuilder(MY_NAME, hashutil.StringMapKeyToHashNoError, func(d HashAndState) {
		calls += 1
		lastState = d.state
	})

	builder.addData(hash_1)
	builder.addData(hash_2)

	assert.Equal(t, 1, calls)
	assert.Equal(t, true, lastState.IsAvailableAndFree())
}

func TestHashBlockStorageMapBuilder_addUse(t *testing.T) {
	calls := 0
	lastState := HashBlockState{}
	builder := NewHashBlockStorageMapBuilder(MY_NAME, hashutil.StringMapKeyToHashNoError, func(d HashAndState) {
		calls += 1
		lastState = d.state
	})

	builder.addData(hash_1)
	builder.addUse(hash_1, MY_NAME)
	builder.addData(hash_2)

	assert.Equal(t, 1, calls)
	assert.Equal(t, true, lastState.IsAvailableAndReservedByMe())
}

func TestHashBlockStorageMapBuilder_SlashedHashStrings_addUse(t *testing.T) {
	calls := 0
	lastState := HashBlockState{}
	builder := NewHashBlockStorageMapBuilder(MY_NAME, hashutil.SlashedStringMapKeyToHashNoError, func(d HashAndState) {
		calls += 1
		lastState = d.state
	})

	builder.addData(slashedHash_1)
	builder.addUse(slashedHash_1, MY_NAME)
	builder.addData(slashedHash_2)

	assert.Equal(t, 1, calls)
	assert.Equal(t, true, lastState.IsAvailableAndReservedByMe())
}

func TestHashBlockStorageMapBuilder_addDelete(t *testing.T) {
	calls := 0
	lastState := HashBlockState{}
	builder := NewHashBlockStorageMapBuilder(MY_NAME, hashutil.StringMapKeyToHashNoError, func(d HashAndState) {
		calls += 1
		lastState = d.state
	})

	builder.addData(hash_1)
	builder.addDelete(hash_1)
	builder.addData(hash_2)

	assert.Equal(t, 1, calls)
	assert.Equal(t, false, lastState.IsAvailable())
}

func TestHashBlockStorageMapBuilder_addUseOther(t *testing.T) {
	calls := 0
	lastState := HashBlockState{}
	builder := NewHashBlockStorageMapBuilder(MY_NAME, hashutil.StringMapKeyToHashNoError, func(d HashAndState) {
		calls += 1
		lastState = d.state
	})

	builder.addData(hash_1)
	builder.addUse(hash_1, OTHER_NAME_1)
	builder.addData(hash_2)

	assert.Equal(t, 1, calls)
	assert.Equal(t, true, lastState.IsReservedBySomeone())
	assert.Equal(t, true, lastState.reservedByOthers)
	assert.Equal(t, false, lastState.reservedByMe)
}

func TestHashBlockStorageMapBuilder_addUseMeAndOther(t *testing.T) {
	calls := 0
	lastState := HashBlockState{}
	builder := NewHashBlockStorageMapBuilder(MY_NAME, hashutil.StringMapKeyToHashNoError, func(d HashAndState) {
		calls += 1
		lastState = d.state
	})

	builder.addData(hash_1)
	builder.addUse(hash_1, OTHER_NAME_1)
	builder.addUse(hash_1, MY_NAME)
	builder.addUse(hash_1, OTHER_NAME_2)
	builder.addData(hash_2)

	assert.Equal(t, 1, calls)
	assert.Equal(t, true, lastState.IsAvailableAndReservedByMe())
}

func TestHashBlockStorage_generateSlashedAndNonSlashedHashStrings(t *testing.T) {

	hm_normal := GoCloudUrlStorage{
		url:                   "should not be used in this test",
		useSlashedHashStrings: false,
		ctx:                   context.Background(),
		bucket:                nil, /* should not be used in this test */
		myDeviceId:            "TEST_DEVICE_ID",
	}

	hm_slashed := hm_normal // copy
	hm_slashed.useSlashedHashStrings = true

	testData := "test test test"
	testHash := sha256.Sum256([]byte(testData))

	assert.Equal(t, "blocks/497b22d4e86a3caa9f5baa24435a99ac1154094a0b9302b9bcd9d6544d6efbe9",
		hm_normal.getBlockStringKey(testHash[:]))
	assert.Equal(t, "blocks/4/9/7/b/2/2/d/4/e/8/6/a/3/c/a/a/9/f/5/b/a/a/2/4/4/3/5/a/9/9/a/c/1/1/5/4/0/9/4/a/0/b/9/3/0/2/b/9/b/c/d/9/d/6/5/4/4/d/6/e/f/b/e/9",
		hm_slashed.getBlockStringKey(testHash[:]))
}
