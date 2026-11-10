package managed

import (
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
)

// directResetSeed holds the prepared seed for one synchronous reset.
type directResetSeed struct {
	Old, Next                   store.EmbeddedBinding
	Identity, Role, Reason      string
	GenesisBody, ScratchpadBody []byte
	PortedEntries               []store.EmbeddedEntry
}
