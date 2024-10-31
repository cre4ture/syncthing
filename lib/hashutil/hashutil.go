package hashutil

import "encoding/hex"

func HashToStringMapKey(hash []byte) string {
	return hex.EncodeToString(hash)
}
