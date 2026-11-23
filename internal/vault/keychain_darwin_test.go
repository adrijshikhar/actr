//go:build darwin

package vault

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseDumpKeychain(t *testing.T) {
	sample := `
keychain: "/Users/test/Library/Keychains/login.keychain-db"
version: 512
class: "genp"
attributes:
    0x00000007 <blob>="ProtectedCloudStorage"
    0x00000008 <blob>=<NULL>
    "acct"<blob>="default"
    "svce"<blob>="ProtectedCloudStorage"
keychain: "/Users/test/Library/Keychains/login.keychain-db"
version: 512
class: "genp"
attributes:
    "acct"<blob>="my_secret_token"
    "svce"<blob>="com.github.actr.secrets"
class: "genp"
attributes:
    "acct"<blob>=<NULL>
    "svce"<blob>="com.github.actr.secrets"
class: "genp"
attributes:
    "svce"<blob>="com.github.actr.secrets"
    "acct"<blob>="reversed_order_token"
`
	res := parseDumpKeychain(sample, "com.github.actr.secrets", "keychain")
	assert.Len(t, res, 2)
	assert.Equal(t, "my_secret_token", res[0].Key)
	assert.Equal(t, "keychain", res[0].Provider)
	assert.Equal(t, "reversed_order_token", res[1].Key)
	assert.Equal(t, "keychain", res[1].Provider)
}
