// Compiles every source and resolver into the binary
package plugins

import (
	// resolvers
	_ "github.com/usrbinbryce/reap/internal/resolvers/imgur"

	// sources
	_ "github.com/usrbinbryce/reap/internal/sources/reddit"
)
