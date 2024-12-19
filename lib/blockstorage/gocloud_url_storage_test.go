package blockstorage

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

const MY_NAME = "MY-OWN-NAME"
const OTHER_NAME_1 = "OTHER-DEVICE-1"
const OTHER_NAME_2 = "OTHER-DEVICE-2"

func TestHashBlockStorageMapBuilder_addData(t *testing.T) {
	calls := 0
	lastState := HashBlockState{}
	builder := NewHashBlockStorageMapBuilder(MY_NAME, func(hash string, state HashBlockState) {
		calls += 1
		lastState = state
	})

	builder.addData("hash_1")
	builder.addData("hash_2")

	assert.Equal(t, 1, calls)
	assert.Equal(t, true, lastState.IsAvailableAndFree())
}

func TestHashBlockStorageMapBuilder_addUse(t *testing.T) {
	calls := 0
	lastState := HashBlockState{}
	builder := NewHashBlockStorageMapBuilder(MY_NAME, func(hash string, state HashBlockState) {
		calls += 1
		lastState = state
	})

	builder.addData("hash_1")
	builder.addUse("hash_1", MY_NAME)
	builder.addData("hash_2")

	assert.Equal(t, 1, calls)
	assert.Equal(t, true, lastState.IsAvailableAndReservedByMe())
}

func TestHashBlockStorageMapBuilder_addDelete(t *testing.T) {
	calls := 0
	lastState := HashBlockState{}
	builder := NewHashBlockStorageMapBuilder(MY_NAME, func(hash string, state HashBlockState) {
		calls += 1
		lastState = state
	})

	builder.addData("hash_1")
	builder.addDelete("hash_1")
	builder.addData("hash_2")

	assert.Equal(t, 1, calls)
	assert.Equal(t, false, lastState.IsAvailable())
}

func TestHashBlockStorageMapBuilder_addUseOther(t *testing.T) {
	calls := 0
	lastState := HashBlockState{}
	builder := NewHashBlockStorageMapBuilder(MY_NAME, func(hash string, state HashBlockState) {
		calls += 1
		lastState = state
	})

	builder.addData("hash_1")
	builder.addUse("hash_1", OTHER_NAME_1)
	builder.addData("hash_2")

	assert.Equal(t, 1, calls)
	assert.Equal(t, true, lastState.IsReservedBySomeone())
	assert.Equal(t, true, lastState.reservedByOthers)
	assert.Equal(t, false, lastState.reservedByMe)
}

func TestHashBlockStorageMapBuilder_addUseMeAndOther(t *testing.T) {
	calls := 0
	lastState := HashBlockState{}
	builder := NewHashBlockStorageMapBuilder(MY_NAME, func(hash string, state HashBlockState) {
		calls += 1
		lastState = state
	})

	builder.addData("hash_1")
	builder.addUse("hash_1", OTHER_NAME_1)
	builder.addUse("hash_1", MY_NAME)
	builder.addUse("hash_1", OTHER_NAME_2)
	builder.addData("hash_2")

	assert.Equal(t, 1, calls)
	assert.Equal(t, true, lastState.IsAvailableAndReservedByMe())
}
